package tools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/review-raven/internal/middleware"
	"github.com/scottlz0310/review-raven/internal/store"
	"github.com/scottlz0310/review-raven/internal/watch"
)

// testMcpSessionIDHeader mirrors the SDK's session header name for tests that
// need to assert on its (non-)presence in stateless mode.
const testMcpSessionIDHeader = "Mcp-Session-Id"

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

	handler := BuildStreamableHandler(db, 30*time.Second)
	handler.Close()
	handler.Close()

	_, _, err = handler.watchManager.Start(watch.StartInput{
		Login: "alice",
		Token: "token-a",
		Owner: "octo",
		Repo:  "demo",
		PR:    1,
	})
	if !errors.Is(err, watch.ErrManagerClosed) {
		t.Fatalf("Start() after Close() error = %v, want %v", err, watch.ErrManagerClosed)
	}
}

// TestStreamableHandlerStatelessDoesNotIssueSessionID is a regression test for
// MCP 2026-07-28 stateless negotiation: go-sdk only accepts the new protocol
// version on Streamable HTTP when Stateless is true, and a stateless server
// must not mint or echo Mcp-Session-Id.
func TestStreamableHandlerStatelessDoesNotIssueSessionID(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`)
	resp := postMCP(t, httpServer.URL, "token-a", "", initBody)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}
	if sid := resp.Header.Get(testMcpSessionIDHeader); sid != "" {
		t.Fatalf("initialize response Mcp-Session-Id = %q, want empty in stateless mode", sid)
	}
}

// TestStreamableHandlerStatelessRejectsGet confirms Stateless: true is wired
// through to the SDK handler: per spec, stateless servers reject standalone
// GET (and DELETE) with 405 rather than opening a session-bound SSE stream.
func TestStreamableHandlerStatelessRejectsGet(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	req, err := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405 (stateless servers reject standalone GET)", resp.StatusCode)
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
		req.Header.Set(testMcpSessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.Do() error = %v", err)
	}
	return resp
}

// TestSubscribeHandlerRejectsLegacyURIScheme is a regression test for the ghost
// subscription bug: before the fix, copilot-review://watch/... URIs fell through
// the watchPrefix check and returned nil (success), causing go-sdk to register a
// subscription that would never receive notifications. The fix returns
// mcp.ResourceNotFoundError for the legacy scheme.
//
// This sends the raw subscriptions/listen JSON-RPC request instead of going
// through mcp.ClientSession.Subscribe(): under protocol 2026-07-28,
// ClientSession.Subscribe() (go-sdk v1.7.0) discards the server's JSON-RPC
// error for this method — verified by wire capture that the server correctly
// returns HTTP 400 with the ResourceNotFound error while Subscribe() itself
// returns nil. That is an upstream go-sdk client-side bug, not a review-raven
// authorization gap; asserting on the raw HTTP response is what actually
// exercises our SubscribeHandler's security boundary.
func TestSubscribeHandlerRejectsLegacyURIScheme(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0.0"}},"notifications":{"resourceSubscriptions":["copilot-review://watch/abc123"]}}}`)
	req, err := http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "subscriptions/listen")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body error = %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("subscriptions/listen for legacy URI status = 200, want an error status; body=%s", respBody)
	}
	if !strings.Contains(string(respBody), "Resource not found") {
		t.Fatalf("subscriptions/listen response = %q, want to contain \"Resource not found\"", respBody)
	}
}
