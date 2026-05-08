package tools

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
)

// authErrResult converts an *autherr.AuthError into a *mcp.CallToolResult with JSON
// text content. IsError is set to true so AI agents treat it as a blocking failure.
func authErrResult(ae *autherr.AuthError) *mcp.CallToolResult {
	b, _ := json.Marshal(ae)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
		IsError: true,
	}
}

// tryAuthResult checks whether err represents an authentication failure and, if so,
// returns a structured *mcp.CallToolResult and true.
//
// It handles:
//   - *autherr.AuthError (e.g. AUTH_REQUIRED from the client provider)
//   - GitHub HTTP 401 errors detected by ghclient.IsAuthError → REAUTH_REQUIRED
func tryAuthResult(err error) (*mcp.CallToolResult, bool) {
	if err == nil {
		return nil, false
	}
	if ae, ok := autherr.AsAuthError(err); ok {
		return authErrResult(ae), true
	}
	if ghclient.IsAuthError(err) {
		return authErrResult(autherr.NewReauthRequired()), true
	}
	return nil, false
}
