package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// protocolVersion20260728 is the only MCP revision this server serves.
const protocolVersion20260728 = "2026-07-28"

// legacyInitializeMethod is the handshake that SEP-2575 replaced with
// server/discover.
const legacyInitializeMethod = "initialize"

// rejectLegacyInitialize refuses the deprecated initialize handshake before it
// reaches the SDK handler.
//
// go-sdk offers no server-side switch for this: a stateless server still
// answers initialize and negotiates down to a legacy revision, so a client that
// never learned server/discover keeps working against us even though the whole
// review stack has moved to 2026-07-28 (see #117). The reply is the SEP-2575
// error a modern-only endpoint owes a legacy client — the same shape thread-owl
// serves via the TS SDK's legacy: "reject" — so the caller can read the
// versions we do serve out of the error alone.
func rejectLegacyInitialize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Peek one byte past the SDK's own limit so an oversized body stays the
		// SDK's business (it answers 413) instead of becoming an unbounded read
		// here. Either way the body is handed on whole.
		original := r.Body
		peeked, err := io.ReadAll(io.LimitReader(original, mcp.DefaultMaxRequestBodyBytes+1))
		r.Body = peekedBody{
			Reader: io.MultiReader(bytes.NewReader(peeked), original),
			Closer: original,
		}
		if err != nil || int64(len(peeked)) > mcp.DefaultMaxRequestBodyBytes {
			next.ServeHTTP(w, r)
			return
		}

		id, requested, found := legacyInitializeFromBody(peeked)
		if !found {
			next.ServeHTTP(w, r)
			return
		}
		writeUnsupportedProtocolVersion(w, id, requested)
	})
}

// peekedBody re-attaches an already-read prefix to the unread remainder of an
// http.Request body, so the downstream handler still sees the whole body.
type peekedBody struct {
	io.Reader
	io.Closer
}

// jsonNull is the JSON-RPC id for a response that answers no single request.
var jsonNull = json.RawMessage("null")

// legacyInitializeFromBody reports whether body carries an initialize call,
// returning the JSON-RPC id the rejection must carry and the protocol version
// the call asked for. Anything it cannot parse is left for the SDK to answer.
//
// A batched body yields a null id: one response rejects the whole batch, so no
// single request's id can honestly claim it — JSON-RPC 2.0 requires null
// wherever the responding id cannot be determined.
func legacyInitializeFromBody(body []byte) (id json.RawMessage, requested string, found bool) {
	msgs, batched := jsonRPCMessages(body)
	for _, raw := range msgs {
		msg, err := jsonrpc.DecodeMessage(raw)
		if err != nil {
			continue
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok || req.Method != legacyInitializeMethod {
			continue
		}
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		// A missing or malformed params block still identifies the handshake;
		// only the version we echo back is lost.
		_ = json.Unmarshal(req.Params, &params)
		if batched {
			return jsonNull, params.ProtocolVersion, true
		}
		// ID.Raw is nil, a float64, or a string — all of which marshal, and
		// nil (a notification) marshals to the null the spec asks for.
		encoded, _ := json.Marshal(req.ID.Raw())
		return encoded, params.ProtocolVersion, true
	}
	return nil, "", false
}

// jsonRPCMessages splits a POST body into its JSON-RPC messages, accepting both
// the single-message and the batched form, and reports which form it was.
func jsonRPCMessages(body []byte) (msgs []json.RawMessage, batched bool) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, false
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return nil, true
		}
		return batch, true
	}
	return []json.RawMessage{trimmed}, false
}

// jsonRPCErrorResponse is written by hand rather than through
// jsonrpc.EncodeMessage because that encoder omits a null id, and JSON-RPC 2.0
// requires the member to be present on every response.
type jsonRPCErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   jsonRPCError    `json:"error"`
}

type jsonRPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// writeUnsupportedProtocolVersion answers with the SEP-2575
// UnsupportedProtocolVersionError. HTTP 400 is the status the spec assigns to
// -32022 on the 2026-07-28 transport, and mcp-gateway forwards it verbatim.
// id is the raw JSON-RPC id from legacyInitializeFromBody, which is null
// rather than empty wherever no single request owns the response.
func writeUnsupportedProtocolVersion(w http.ResponseWriter, id json.RawMessage, requested string) {
	message := "unsupported protocol version"
	if requested != "" {
		message += ": " + requested
	}
	// Every value on this response is a fixed shape, so neither marshal step
	// has a failure mode to handle. go-sdk drops the same error for the same
	// reason where it builds this error itself.
	data, _ := json.Marshal(mcp.UnsupportedProtocolVersionData{
		Supported: []string{protocolVersion20260728},
		Requested: requested,
	})
	body, _ := json.Marshal(jsonRPCErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: jsonRPCError{
			Code:    mcp.CodeUnsupportedProtocolVersion,
			Message: message,
			Data:    data,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(body)
}
