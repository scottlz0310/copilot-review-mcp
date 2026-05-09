package tools

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
)

// authErrResult converts an *autherr.AuthError into a *mcp.CallToolResult.
// Content carries a JSON text representation and StructuredContent provides
// the same data as a parsed object. IsError is set to true so AI agents
// treat it as a blocking failure that requires user action.
func authErrResult(ae *autherr.AuthError) *mcp.CallToolResult {
	b, _ := json.Marshal(ae)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: ae,
		IsError:           true,
	}
}

// authErrString returns a canonical "<ErrorType>: <Message>" string for classified
// GitHub errors. It mirrors tryAuthResult's detection logic but returns a plain string
// suitable for embedding in output fields rather than a full MCP result.
func authErrString(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if ae, ok := autherr.AsAuthError(err); ok {
		return fmt.Sprintf("%s: %s", ae.ErrorType, ae.Message), true
	}
	if ae := ghclient.ClassifyGitHubError(err); ae != nil {
		return fmt.Sprintf("%s: %s", ae.ErrorType, ae.Message), true
	}
	return "", false
}

// tryAuthResult checks whether err represents a classified GitHub API failure and,
// if so, returns a structured *mcp.CallToolResult and true.
//
// It handles:
//   - *autherr.AuthError (e.g. AUTH_REQUIRED from the client provider)
//   - Any GitHub API error classifiable by ghclient.ClassifyGitHubError
//     (401→REAUTH_REQUIRED, 403→PERMISSION_DENIED, 404→NOT_FOUND, etc.)
func tryAuthResult(err error) (*mcp.CallToolResult, bool) {
	if err == nil {
		return nil, false
	}
	if ae, ok := autherr.AsAuthError(err); ok {
		return authErrResult(ae), true
	}
	if ae := ghclient.ClassifyGitHubError(err); ae != nil {
		return authErrResult(ae), true
	}
	return nil, false
}
