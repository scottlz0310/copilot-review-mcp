package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/middleware"
)

type githubClientProvider func(context.Context, *mcp.CallToolRequest) (*ghclient.Client, error)

func newGitHubClientProvider(threshold time.Duration, invalidate func(string)) githubClientProvider {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*ghclient.Client, error) {
		token := tokenFromToolRequest(ctx, req)
		if token == "" {
			return nil, fmt.Errorf("authenticated GitHub token is required")
		}
		return ghclient.NewClient(ctx, token, threshold, invalidate), nil
	}
}

func loginFromToolRequest(ctx context.Context, req *mcp.CallToolRequest) string {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil && req.Extra.TokenInfo.UserID != "" {
		return req.Extra.TokenInfo.UserID
	}
	return middleware.LoginFromContext(ctx)
}

func tokenFromToolRequest(ctx context.Context, req *mcp.CallToolRequest) string {
	if req != nil && req.Extra != nil {
		if token := bearerTokenFromHeader(req.Extra.Header); token != "" {
			return token
		}
	}
	return middleware.TokenFromContext(ctx)
}

func bearerTokenFromHeader(header http.Header) string {
	if header == nil {
		return ""
	}
	fields := strings.Fields(header.Get("Authorization"))
	if len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
		return fields[1]
	}
	return ""
}
