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
		// wantID is the JSON-RPC id the rejection must carry, as raw JSON.
		wantID string
	}{
		{
			name: "2025-06-18 handshake",
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
				`"protocolVersion":"2025-06-18","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`,
			wantRequested: "2025-06-18",
			wantID:        `1`,
		},
		{
			name: "2025-11-25 handshake with a string id",
			body: `{"jsonrpc":"2.0","id":"str-id","method":"initialize","params":{` +
				`"protocolVersion":"2025-11-25","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`,
			wantRequested: "2025-11-25",
			wantID:        `"str-id"`,
		},
		{
			// One response rejects the whole batch, so it can claim no single
			// request's id — JSON-RPC 2.0 requires null there.
			name: "batched handshake answers with a null id",
			body: `[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
				`"protocolVersion":"2025-06-18","capabilities":{},` +
				`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}]`,
			wantRequested: "2025-06-18",
			wantID:        `null`,
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

			// Decoded as raw members so a missing id is distinguishable from a
			// null one: the spec requires the member on every response, and a
			// struct field cannot tell the two apart.
			assertJSONRPCResponseID(t, respBody, tt.wantID)

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

// TestStreamableHandlerForwardsUnclassifiableBodies covers the cases the gate
// deliberately does not answer itself. A body it cannot parse, or one larger
// than the SDK's own request limit, belongs to the SDK — the peek must hand it
// on rather than guess at a rejection. What matters is that none of these come
// back as our -32022; the SDK owns whatever status they do get.
func TestStreamableHandlerForwardsUnclassifiableBodies(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	legacyInitialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}`

	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "not JSON at all", body: "not json"},
		{name: "unterminated batch", body: `[{"jsonrpc":"2.0","id":1,`},
		{
			// Padding past mcp.DefaultMaxRequestBodyBytes: the peek stops at the
			// limit, so the handshake inside is never classified and the SDK
			// rejects the request on size instead.
			name: "handshake past the SDK request size limit",
			body: legacyInitialize + strings.Repeat(" ", mcp.DefaultMaxRequestBodyBytes),
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
			if strings.Contains(string(respBody), "unsupported protocol version") {
				t.Fatalf("gate answered a body it must not classify (status %d): %s", resp.StatusCode, respBody)
			}
		})
	}
}

// TestStreamableHandlerRejectsLegacyInitializeAmongUndecodableBatchEntries
// pins that an entry the JSON-RPC decoder rejects does not shadow a handshake
// later in the same batch, and that the rejection still carries a null id.
func TestStreamableHandlerRejectsLegacyInitializeAmongUndecodableBatchEntries(t *testing.T) {
	db := openServerTestDB(t)
	handler := BuildStreamableHandler(db, 30*time.Second)
	t.Cleanup(handler.Close)

	httpServer := httptest.NewServer(withAuthContext(handler, map[string]string{
		"token-a": "alice",
	}))
	t.Cleanup(httpServer.Close)

	body := `[{"not":"a jsonrpc message"},` +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"legacy-client","version":"1.0.0"}}}]`
	req := newLegacyRequest(t, httpServer.URL, "token-a", body)
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
		t.Fatalf("batched initialize status = %d, want 400; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "unsupported protocol version") {
		t.Fatalf("batched initialize response = %s, want the -32022 rejection", respBody)
	}

	assertJSONRPCResponseID(t, respBody, "null")
}

// assertJSONRPCResponseID checks the raw id member of a JSON-RPC response.
// Decoding into a map rather than a struct is what makes an omitted id
// distinguishable from a null one — encoding/json gives both the same zero
// value in a struct field.
func assertJSONRPCResponseID(t *testing.T, respBody []byte, wantID string) {
	t.Helper()

	var members map[string]json.RawMessage
	if err := json.Unmarshal(respBody, &members); err != nil {
		t.Fatalf("decode response error = %v; raw=%s", err, respBody)
	}
	if got := string(members["jsonrpc"]); got != `"2.0"` {
		t.Fatalf("jsonrpc = %s, want \"2.0\"; raw=%s", got, respBody)
	}
	id, ok := members["id"]
	if !ok {
		t.Fatalf("response omits the id member; raw=%s", respBody)
	}
	if string(id) != wantID {
		t.Fatalf("response id = %s, want %s; raw=%s", id, wantID, respBody)
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
