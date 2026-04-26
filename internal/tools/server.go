package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/middleware"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
	"github.com/scottlz0310/copilot-review-mcp/internal/watch"
)

var schemaCache = mcp.NewSchemaCache()

const (
	defaultStreamableSessionTimeout = 30 * time.Minute
	defaultSessionPruneInterval     = 5 * time.Minute
	mcpSessionIDHeader              = "Mcp-Session-Id"
	sessionUserMismatchError        = "session_user_mismatch"
)

// TokenInvalidator is implemented by auth.Handler to clear a token from the
// validation cache when a downstream GitHub API call returns HTTP 401.
type TokenInvalidator interface {
	InvalidateCachedToken(token string)
}

// StreamableHandler serves MCP over Streamable HTTP and owns shared background state.
type StreamableHandler struct {
	handler      http.Handler
	watchManager *watch.Manager
	server       *mcp.Server

	mu sync.Mutex

	sessionLogins map[string]string
	stopPruner    chan struct{}
	closeOnce     sync.Once
}

// ServeHTTP proxies requests to the underlying MCP streamable handler.
func (h *StreamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	login := middleware.LoginFromContext(r.Context())
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID != "" && login != "" && !h.authorizeSession(sessionID, login) {
		writeJSONError(w, http.StatusForbidden, sessionUserMismatchError)
		return
	}

	h.handler.ServeHTTP(w, r)

	if responseSessionID := w.Header().Get(mcpSessionIDHeader); responseSessionID != "" && login != "" {
		h.rememberSession(responseSessionID, login)
	}
	if r.Method == http.MethodDelete && sessionID != "" {
		h.forgetSession(sessionID)
	}
}

// Close stops background review watches owned by this handler.
func (h *StreamableHandler) Close() {
	if h == nil {
		return
	}

	h.closeOnce.Do(func() {
		close(h.stopPruner)

		h.mu.Lock()
		server := h.server
		h.sessionLogins = make(map[string]string)
		h.mu.Unlock()

		if server != nil {
			for session := range server.Sessions() {
				sessionID := session.ID()
				if err := session.Close(); err != nil {
					slog.Warn("failed to close MCP session", "session_id", sessionID, "err", err)
				}
			}
		}
		if h.watchManager != nil {
			h.watchManager.Close()
		}
	})
}

// BuildStreamableHandler returns a handler that serves MCP over Streamable HTTP.
// getServer is called for new stateful MCP sessions and returns the shared
// long-lived *mcp.Server. GitHub clients are created per tool call from the
// authenticated request headers.
// inv is called to invalidate the cached token when GitHub returns HTTP 401.
func BuildStreamableHandler(db *store.DB, threshold time.Duration, inv TokenInvalidator) *StreamableHandler {
	var invalidate func(string)
	if inv != nil {
		invalidate = inv.InvalidateCachedToken
	}

	clientProvider := newGitHubClientProvider(threshold, invalidate)
	// watchManager is declared before srv so the SubscribeHandler closure can reference it
	// for authorization. At the time any subscribe request arrives the server is already
	// fully initialized, so watchManager is always non-nil.
	var watchManager *watch.Manager
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "copilot-review-mcp", Version: "2.4.0"},
		&mcp.ServerOptions{
			SchemaCache: schemaCache,
			SubscribeHandler: func(ctx context.Context, req *mcp.SubscribeRequest) error {
				if watchManager == nil || req == nil || req.Params == nil {
					return nil
				}
				uri := req.Params.URI
				const watchPrefix = "copilot-review://watch/"
				if !strings.HasPrefix(uri, watchPrefix) {
					return nil // not a watch URI; allow subscription for other resource types
				}
				// URI has the watch prefix — parse it strictly so malformed URIs are rejected.
				watchID, err := parseWatchIDFromURI(uri)
				if err != nil {
					return mcp.ResourceNotFoundError(uri)
				}
				login := middleware.LoginFromContext(ctx)
				if login == "" {
					return fmt.Errorf("authenticated GitHub login is required to subscribe")
				}
				snap, ok := watchManager.GetByID(watchID)
				if !ok || snap.Login != login {
					return mcp.ResourceNotFoundError(uri)
				}
				return nil
			},
			UnsubscribeHandler: func(_ context.Context, _ *mcp.UnsubscribeRequest) error {
				return nil
			},
		},
	)
	watchManager = watch.NewManager(db, watch.Options{
		Threshold:       threshold,
		InvalidateToken: invalidate,
		NotifyResourceUpdated: func(uri string) {
			if err := srv.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: uri}); err != nil {
				slog.Warn("resource updated notification failed", "uri", uri, "err", err)
			}
		},
	})
	RegisterStatusTool(srv, clientProvider, db)
	RegisterWatchTools(srv, watchManager)
	RegisterWatchResources(srv, watchManager)
	RegisterWaitTool(srv, clientProvider, db)
	RegisterRequestTool(srv, clientProvider, db)
	RegisterThreadTools(srv, clientProvider)
	RegisterCycleTool(srv, clientProvider, db)

	streamableHandler := &StreamableHandler{
		watchManager:  watchManager,
		server:        srv,
		sessionLogins: make(map[string]string),
		stopPruner:    make(chan struct{}),
	}

	getServer := func(r *http.Request) *mcp.Server {
		if middleware.TokenFromContext(r.Context()) == "" {
			return nil
		}
		return srv
	}
	streamableHandler.handler = mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		EventStore:     mcp.NewMemoryEventStore(nil),
		SessionTimeout: defaultStreamableSessionTimeout,
		// DisableLocalhostProtection is opt-in via MCP_DISABLE_LOCALHOST_PROTECTION=true.
		// Enable when the server runs behind a reverse proxy or inside a Docker network.
		DisableLocalhostProtection: os.Getenv("MCP_DISABLE_LOCALHOST_PROTECTION") == "true",
	})
	go streamableHandler.pruneSessionLoginsLoop(defaultSessionPruneInterval)
	return streamableHandler
}

func (h *StreamableHandler) rememberSession(sessionID, login string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionLogins[sessionID] = login
}

func (h *StreamableHandler) forgetSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessionLogins, sessionID)
}

func (h *StreamableHandler) authorizeSession(sessionID, login string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	expected, ok := h.sessionLogins[sessionID]
	return !ok || expected == login
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (h *StreamableHandler) pruneSessionLoginsLoop(interval time.Duration) {
	if interval <= 0 {
		interval = defaultSessionPruneInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.pruneSessionLogins()
		case <-h.stopPruner:
			return
		}
	}
}

func (h *StreamableHandler) pruneSessionLogins() {
	server := h.server
	if server == nil {
		return
	}

	active := make(map[string]struct{})
	for session := range server.Sessions() {
		if id := session.ID(); id != "" {
			active[id] = struct{}{}
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for sessionID := range h.sessionLogins {
		if _, ok := active[sessionID]; !ok {
			delete(h.sessionLogins, sessionID)
		}
	}
}
