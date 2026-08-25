package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStreamableHandlerRejectsLegacyInitialize is the wire-level contract test
// for #117: review-raven serves 2026-07-28 only, so the initialize handshake
// that SEP-2575 replaced must fail with -32022 instead of negotiating down.
// Without the gate go-sdk answers initialize with a legacy InitializeResult —
// which is what a Codex A/B run against thread-owl exposed. It is asserted over
// raw HTTP because go-sdk keeps its client-side protocol version override
// unexported, so no SDK client can be made to send a legacy handshake.
func TestStreamableHandlerRejectsLegacyInitialize(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	tests := []struct {
		name          string
		body          string
		wantRequested string
	}{
		{
			name: "2025-06-18 handshake",
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
				`"protocolVersion":"2025-06-18","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`,
			wantRequested: "2025-06-18",
		},
		{
			name: "2025-11-25 handshake with a string id",
			body: `{"jsonrpc":"2.0","id":"str-id","method":"initialize","params":{` +
				`"protocolVersion":"2025-11-25","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`,
			wantRequested: "2025-11-25",
		},
		{
			name: "batched handshake",
			body: `[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
				`"protocolVersion":"2025-06-18","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}]`,
			wantRequested: "2025-06-18",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newLegacyRequest(t, httpServer.URL, "token-a", tt.body)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("http.Do() error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response body error = %v", err)
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("initialize status = %d, want 400; body=%s", resp.StatusCode, respBody)
			}

			var decoded struct {
				Error *struct {
					Code    int64                              `json:"code"`
					Message string                             `json:"message"`
					Data    mcp.UnsupportedProtocolVersionData `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal(respBody, &decoded); err != nil {
				t.Fatalf("decode response error = %v; raw=%s", err, respBody)
			}
			if decoded.Error == nil {
				t.Fatalf("initialize response carries no JSON-RPC error; raw=%s", respBody)
			}
			if decoded.Error.Code != mcp.CodeUnsupportedProtocolVersion {
				t.Fatalf("error code = %d, want %d; raw=%s",
					decoded.Error.Code, mcp.CodeUnsupportedProtocolVersion, respBody)
			}
			if decoded.Error.Data.Requested != tt.wantRequested {
				t.Fatalf("error data requested = %q, want %q; raw=%s",
					decoded.Error.Data.Requested, tt.wantRequested, respBody)
			}
			if want := []string{testProtocolVersion20260728}; !slices.Equal(decoded.Error.Data.Supported, want) {
				t.Fatalf("error data supported = %v, want %v; raw=%s",
					decoded.Error.Data.Supported, want, respBody)
			}
		})
	}
}

// TestStreamableHandlerForwardsNonInitializeBodies guards the gate against
// over-reach: it must hand the body on untouched, so a modern request still
// reaches the SDK handler after the peek.
func TestStreamableHandlerForwardsNonInitializeBodies(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + testNewProtocolMeta + `}}`)
	req := newProtocolRequest(t, context.Background(), httpServer.URL, "token-a", "tools/list", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http.Do() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), `"tools"`) {
		t.Fatalf("tools/list response does not carry a tool list: %s", respBody)
	}
}

// newLegacyRequest builds a raw initialize POST the way a pre-SEP-2575 client
// sends it: no per-request _meta envelope and no 2026-07-28 standard headers.
func newLegacyRequest(t *testing.T, endpoint, token, body string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	return req
}
