package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/middleware"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
	"github.com/scottlz0310/copilot-review-mcp/internal/watch"
)

func TestStreamableHandlerCloseClosesWatchManager(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "server-test.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close() error = %v", err)
		}
	})

	handler := BuildStreamableHandler(db, 30*time.Second, nil)
	handler.Close()
	handler.Close()

	_, _, err = handler.watchManager.Start(watch.StartInput{
		Login: "alice",
		Token: "token-a",
		Owner: "octo",
		Repo:  "demo",
		PR:    1,
	})
	if err == nil {
		t.Fatal("Start() after Close() = nil error, want watch manager is closed")
	}
	if err.Error() != "watch manager is closed" {
		t.Fatalf("Start() after Close() error = %q, want %q", err.Error(), "watch manager is closed")
	}
}

func TestStreamableHandlerReusesStatefulSessionServer(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second, nil)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           bearerTokenHTTPClient("token-a"),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Fatalf("session.Close() error = %v", err)
		}
	})

	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("first ListTools() error = %v", err)
	}
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("second ListTools() error = %v", err)
	}

	if got := handlerServerSessionCount(handler); got != 1 {
		t.Fatalf("server session count = %d, want 1 stateful session reused across requests", got)
	}
	if got := handlerSessionLoginCount(handler); got != 1 {
		t.Fatalf("session login count = %d, want 1", got)
	}
}

func TestStreamableHandlerRejectsSessionUserMismatch(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second, nil)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
		"token-b": "bob",
	}))
	t.Cleanup(httpServer.Close)

	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`)
	resp := postMCP(t, httpServer.URL, "token-a", "", initBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("initialize status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}
	sessionID := resp.Header.Get(mcpSessionIDHeader)
	resp.Body.Close()
	if sessionID == "" {
		t.Fatal("initialize response missing Mcp-Session-Id")
	}

	initializedBody := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	resp = postMCP(t, httpServer.URL, "token-b", sessionID, initializedBody)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("mismatched session status = %d, want 403; body=%s", resp.StatusCode, string(body))
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("mismatched session content type = %q, want application/json", contentType)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode mismatched session response error = %v", err)
	}
	if got := body["error"]; got != sessionUserMismatchError {
		t.Fatalf("mismatched session error = %q, want %q", got, sessionUserMismatchError)
	}
}

func TestTokenFromToolRequestPrefersCurrentAuthorizationHeader(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.ContextKeyToken, "old-token")
	req := &mcp.CallToolRequest{
		Extra: &mcp.RequestExtra{
			Header: http.Header{"Authorization": {"Bearer fresh-token"}},
		},
	}

	if got := tokenFromToolRequest(ctx, req); got != "fresh-token" {
		t.Fatalf("tokenFromToolRequest() = %q, want fresh-token", got)
	}
}

func TestStreamableHandlerPrunesStaleSessionLogins(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second, nil)
	t.Cleanup(handler.Close)

	handler.rememberSession("stale-session", "alice")
	handler.pruneSessionLogins()

	if got := handlerSessionLoginCount(handler); got != 0 {
		t.Fatalf("session login count = %d, want stale session pruned", got)
	}
}

func openServerTestDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "server-test.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close() error = %v", err)
		}
	})
	return db
}

func withAuthContext(next http.Handler, tokenLogins map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		login := tokenLogins[token]
		ctx := context.WithValue(r.Context(), middleware.ContextKeyToken, token)
		ctx = context.WithValue(ctx, middleware.ContextKeyLogin, login)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func postMCP(t *testing.T, endpoint, token, sessionID string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.Do() error = %v", err)
	}
	return resp
}

func bearerTokenHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: bearerTokenRoundTripper{
			token: token,
			base:  http.DefaultTransport,
		},
	}
}

type bearerTokenRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerTokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(req)
}

func handlerServerSessionCount(handler *StreamableHandler) int {
	count := 0
	for range handler.server.Sessions() {
		count++
	}
	return count
}

func handlerSessionLoginCount(handler *StreamableHandler) int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return len(handler.sessionLogins)
}
