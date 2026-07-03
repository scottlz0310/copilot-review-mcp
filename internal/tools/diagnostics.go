package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/review-raven/internal/autherr"
	ghclient "github.com/scottlz0310/review-raven/internal/github"
)

// DiagnoseTokenInput is the (empty) input schema for diagnose_github_token.
type DiagnoseTokenInput struct{}

// DiagnoseTokenOutput is the output schema for diagnose_github_token.
type DiagnoseTokenOutput struct {
	Login  string   `json:"login"`
	Scopes []string `json:"scopes"`
}

var diagnoseTokenTool = &mcp.Tool{
	Name: "diagnose_github_token",
	Description: "現在のリクエストで使われている GitHub トークンの login と OAuth スコープ" +
		"(GET /user の X-OAuth-Scopes レスポンスヘッダー由来)を返す。" +
		"read 系ツールは成功するのに write 系ツールが PERMISSION_DENIED になる場合の原因切り分けに使う。" +
		"fine-grained PAT や GitHub App トークンではヘッダーが存在せず scopes が空配列になることがある。" +
		"トークン生値は返さない。",
}

// emptyDiagnoseTokenOutput is returned alongside error results. Scopes is an
// explicit empty slice (not nil) so JSON marshaling produces [] rather than
// null, matching the tool description's documented output shape.
var emptyDiagnoseTokenOutput = DiagnoseTokenOutput{Scopes: []string{}}

func diagnoseTokenHandler() func(context.Context, *mcp.CallToolRequest, DiagnoseTokenInput) (*mcp.CallToolResult, DiagnoseTokenOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, _ DiagnoseTokenInput) (*mcp.CallToolResult, DiagnoseTokenOutput, error) {
		token := tokenFromToolRequest(ctx, req)
		if token == "" {
			return authErrResult(autherr.NewAuthRequired()), emptyDiagnoseTokenOutput, nil
		}

		diag, err := ghclient.GetTokenDiagnostics(ctx, token)
		if err != nil {
			if result, ok := tryAuthResult(err); ok {
				return result, emptyDiagnoseTokenOutput, nil
			}
			return nil, emptyDiagnoseTokenOutput, err
		}

		scopes := diag.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		return nil, DiagnoseTokenOutput{Login: diag.Login, Scopes: scopes}, nil
	}
}

// RegisterDiagnoseTokenTool adds diagnose_github_token to the MCP server.
func RegisterDiagnoseTokenTool(server *mcp.Server) {
	mcp.AddTool(server, diagnoseTokenTool, diagnoseTokenHandler())
}
