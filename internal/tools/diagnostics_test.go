package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/review-raven/internal/autherr"
)

func TestDiagnoseTokenHandlerAuthRequired(t *testing.T) {
	handler := diagnoseTokenHandler()
	// nil request → no token in context → AUTH_REQUIRED
	result, out, err := handler(context.Background(), nil, DiagnoseTokenInput{})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
	if out.Scopes == nil {
		t.Error("out.Scopes = nil, want non-nil empty slice (marshals to [] not null)")
	}
}

// TestDiagnoseTokenToolMCPRoundTripAuthRequired exercises the tool through the
// real MCP server/client wire path (not just the raw handler), so output
// schema validation in toolForErr (go-sdk) actually runs. This guards against
// regressions where a nil Scopes slice could cause schema validation to
// replace the structured AUTH_REQUIRED error with a generic
// "validating tool output" error (review-raven#90 review discussion).
func TestDiagnoseTokenToolMCPRoundTripAuthRequired(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	RegisterDiagnoseTokenTool(srv)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("srv.Connect() error = %v", err)
	}
	defer func() { _ = ss.Wait() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "diagnose_github_token",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() returned protocol error (want structured AUTH_REQUIRED result): %v", err)
	}
	if !res.IsError {
		t.Error("res.IsError = false, want true")
	}
	if len(res.Content) == 0 {
		t.Fatal("res.Content is empty")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("res.Content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	var ae autherr.AuthError
	if err := json.Unmarshal([]byte(tc.Text), &ae); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, text = %s", err, tc.Text)
	}
	if ae.ErrorType != autherr.AUTH_REQUIRED {
		t.Errorf("ErrorType = %q, want %q", ae.ErrorType, autherr.AUTH_REQUIRED)
	}
}
