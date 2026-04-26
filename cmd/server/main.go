package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/scottlz0310/copilot-review-mcp/internal/auth"
	"github.com/scottlz0310/copilot-review-mcp/internal/middleware"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
	"github.com/scottlz0310/copilot-review-mcp/internal/tools"
)

func main() {
	cfg := loadConfig()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.logLevel),
	})))

	// Open (or create) the SQLite trigger_log database.
	db, err := store.Open(cfg.sqlitePath)
	if err != nil {
		slog.Error("failed to open SQLite database", "path", cfg.sqlitePath, "err", err)
		os.Exit(1)
	}
	defer db.Close()

	oauthHandler := auth.NewHandler(auth.Config{
		GitHubClientID:     cfg.githubClientID,
		GitHubClientSecret: cfg.githubClientSecret,
		BaseURL:            cfg.baseURL,
		Scopes:             cfg.oauthScopes,
		SessionTTL:         time.Duration(cfg.sessionTTLMin) * time.Minute,
		CacheTTL:           time.Duration(cfg.tokenCacheTTLMin) * time.Minute,
		ExpiresIn:          time.Duration(cfg.tokenExpiresInSec) * time.Second,
	})

	authMiddleware := middleware.Auth(oauthHandler)

	mux := http.NewServeMux()

	// OAuth façade endpoints (no auth required)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", oauthHandler.Discovery)
	mux.HandleFunc("GET /authorize", oauthHandler.Authorize)
	mux.HandleFunc("GET /callback", oauthHandler.Callback)
	mux.HandleFunc("POST /token", oauthHandler.Token)
	mux.HandleFunc("POST /register", oauthHandler.Register)

	// Health check (no auth required)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// MCP endpoints (auth required) — Streamable HTTP transport (stateful, shared server)
	threshold := time.Duration(cfg.inProgressThresholdSec) * time.Second
	mcpHandler := tools.BuildStreamableHandler(db, threshold, oauthHandler)
	defer mcpHandler.Close()
	mux.Handle("/mcp", authMiddleware(mcpHandler))
	mux.Handle("/mcp/", authMiddleware(mcpHandler))

	addr := ":" + cfg.port
	slog.Info("copilot-review-mcp starting", "addr", addr, "base_url", cfg.baseURL)
	// WriteTimeout remains unlimited because legacy wait_for_copilot_review still exists
	// as a blocking fallback and may occupy one tool call for up to 30 minutes.
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
	server.RegisterOnShutdown(mcpHandler.Close)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		mcpHandler.Close()
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

type config struct {
	githubClientID         string
	githubClientSecret     string
	baseURL                string
	oauthScopes            string
	port                   string
	logLevel               string
	sessionTTLMin          int
	tokenCacheTTLMin       int
	tokenExpiresInSec      int
	sqlitePath             string
	inProgressThresholdSec int
}

func loadConfig() config {
	c := config{
		githubClientID:         mustEnv("GITHUB_CLIENT_ID"),
		githubClientSecret:     mustEnv("GITHUB_CLIENT_SECRET"),
		baseURL:                getEnv("BASE_URL", "http://localhost:8083"),
		oauthScopes:            getEnv("GITHUB_OAUTH_SCOPES", "repo,user"),
		port:                   getEnv("MCP_PORT", "8083"),
		logLevel:               getEnv("LOG_LEVEL", "info"),
		sessionTTLMin:          getEnvInt("SESSION_TTL_MIN", 10),
		tokenCacheTTLMin:       getEnvInt("TOKEN_CACHE_TTL_MIN", 30),
		tokenExpiresInSec:      getEnvInt("TOKEN_EXPIRES_IN_SEC", 7776000), // 90 days
		sqlitePath:             getEnv("SQLITE_PATH", "/data/copilot-review.db"),
		inProgressThresholdSec: getEnvInt("IN_PROGRESS_THRESHOLD_SEC", 30),
	}
	return c
}

func mustEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
