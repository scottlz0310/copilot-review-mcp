package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-github/v85/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shurcooL/githubv4"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
	"github.com/scottlz0310/copilot-review-mcp/internal/watch"
)

// errorProvider returns a githubClientProvider that always returns the given error.
func errorProvider(err error) githubClientProvider {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*ghclient.Client, error) {
		return nil, err
	}
}

// new401Server returns an httptest.Server that responds to every request with HTTP 401.
func new401Server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
}

// make401GitHubClient creates a *ghclient.Client pointing at a server that returns 401.
func make401GitHubClient(srv *httptest.Server) *ghclient.Client {
	restClient := github.NewClient(srv.Client())
	base, _ := url.Parse(srv.URL + "/")
	restClient.BaseURL = base
	restClient.UploadURL = base
	gqlClient := githubv4.NewEnterpriseClient(srv.URL, srv.Client())
	return ghclient.NewWithClients(restClient, gqlClient, 30*time.Second)
}

// decodeAuthError unmarshals the text content of a *mcp.CallToolResult into *autherr.AuthError.
func decodeAuthError(t *testing.T, result *mcp.CallToolResult) *autherr.AuthError {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(result.Content) == 0 {
		t.Fatal("result.Content is empty")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("result.Content[0] is %T, want *mcp.TextContent", result.Content[0])
	}
	var ae autherr.AuthError
	if err := json.Unmarshal([]byte(tc.Text), &ae); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, text = %s", err, tc.Text)
	}
	return &ae
}

// assertAuthResult checks that result is a non-nil auth error result with IsError set.
func assertAuthResult(t *testing.T, result *mcp.CallToolResult, err error, wantType autherr.AuthErrorType) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("handler returned nil result, want structured auth error")
	}
	if !result.IsError {
		t.Error("result.IsError = false, want true")
	}
	ae := decodeAuthError(t, result)
	if ae.OK {
		t.Error("AuthError.OK = true, want false")
	}
	if ae.ErrorType != wantType {
		t.Errorf("AuthError.ErrorType = %q, want %q", ae.ErrorType, wantType)
	}
	if ae.Severity != "blocking" {
		t.Errorf("AuthError.Severity = %q, want %q", ae.Severity, "blocking")
	}
	if ae.Retryable {
		t.Error("AuthError.Retryable = true, want false")
	}
	if !ae.UserActionRequired {
		t.Error("AuthError.UserActionRequired = false, want true")
	}
	if ae.SafeToContinue {
		t.Error("AuthError.SafeToContinue = true, want false")
	}
}

// ── AUTH_REQUIRED tests (missing token → provider returns *autherr.AuthError) ──

func TestStatusHandlerAuthRequired(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(errorProvider(autherr.NewAuthRequired()), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestRequestHandlerAuthRequired(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := requestHandler(errorProvider(autherr.NewAuthRequired()), db)
	result, _, err := handler(context.Background(), nil, RequestInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestGetReviewThreadsHandlerAuthRequired(t *testing.T) {
	handler := getReviewThreadsHandler(errorProvider(autherr.NewAuthRequired()))
	result, _, err := handler(context.Background(), nil, GetReviewThreadsInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestReplyToThreadHandlerAuthRequired(t *testing.T) {
	handler := replyToThreadHandler(errorProvider(autherr.NewAuthRequired()))
	result, _, err := handler(context.Background(), nil, ReplyToThreadInput{ThreadID: "PRRT_abc", Body: "hello"})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestResolveThreadHandlerAuthRequired(t *testing.T) {
	handler := resolveThreadHandler(errorProvider(autherr.NewAuthRequired()))
	result, _, err := handler(context.Background(), nil, ResolveThreadInput{ThreadID: "PRRT_abc"})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestReplyAndResolveHandlerAuthRequired(t *testing.T) {
	handler := replyAndResolveHandler(errorProvider(autherr.NewAuthRequired()))
	result, _, err := handler(context.Background(), nil, ReplyAndResolveInput{ThreadID: "PRRT_abc", Body: "hello", Resolve: true})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestWaitHandlerAuthRequired(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := waitHandler(errorProvider(autherr.NewAuthRequired()), db)
	result, _, err := handler(context.Background(), nil, WaitInput{Owner: "o", Repo: "r", PR: 1, PollIntervalSeconds: 1, MaxPolls: 1})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

// ── REAUTH_REQUIRED tests (GitHub returns 401 → tryAuthResult wraps as REAUTH_REQUIRED) ──

func TestStatusHandlerReauthRequired(t *testing.T) {
	srv := new401Server()
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(staticProvider(make401GitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.REAUTH_REQUIRED)
}

func TestRequestHandlerReauthRequired(t *testing.T) {
	srv := new401Server()
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := requestHandler(staticProvider(make401GitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, RequestInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.REAUTH_REQUIRED)
}

func TestGetReviewThreadsHandlerReauthRequired(t *testing.T) {
	srv := new401Server()
	t.Cleanup(srv.Close)

	handler := getReviewThreadsHandler(staticProvider(make401GitHubClient(srv)))
	result, _, err := handler(context.Background(), nil, GetReviewThreadsInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.REAUTH_REQUIRED)
}

// ── watch handler AUTH_REQUIRED tests ──

// newMinimalWatchManager returns a watch.Manager for unit tests.
// It uses an in-memory DB and no background polling.
func newMinimalWatchManager(t *testing.T) *watch.Manager {
	t.Helper()
	db := openWatchToolsTestDB(t)
	return watch.NewManager(db, watch.Options{Threshold: 30 * time.Second})
}

func TestStartWatchHandlerAuthRequired(t *testing.T) {
	manager := newMinimalWatchManager(t)
	handler := startWatchHandler(manager)
	// nil request → no token/login in context → AUTH_REQUIRED
	result, _, err := handler(context.Background(), nil, StartReviewWatchInput{Owner: "o", Repo: "r", PR: 1})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestGetWatchStatusHandlerAuthRequired(t *testing.T) {
	manager := newMinimalWatchManager(t)
	handler := getWatchStatusHandler(manager)
	result, _, err := handler(context.Background(), nil, GetReviewWatchStatusInput{WatchID: "some-id"})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestListWatchesHandlerAuthRequired(t *testing.T) {
	manager := newMinimalWatchManager(t)
	handler := listWatchesHandler(manager)
	result, _, err := handler(context.Background(), nil, ListReviewWatchesInput{})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestCancelWatchHandlerAuthRequired(t *testing.T) {
	manager := newMinimalWatchManager(t)
	handler := cancelWatchHandler(manager)
	result, _, err := handler(context.Background(), nil, CancelReviewWatchInput{WatchID: "some-id"})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

// ── StructuredContent is populated ──

func TestAuthErrResultHasStructuredContent(t *testing.T) {
	ae := autherr.NewReauthRequired()
	result := authErrResult(ae)
	if result.StructuredContent == nil {
		t.Fatal("StructuredContent is nil, want *autherr.AuthError")
	}
	sc, ok := result.StructuredContent.(*autherr.AuthError)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want *autherr.AuthError", result.StructuredContent)
	}
	if sc.ErrorType != autherr.REAUTH_REQUIRED {
		t.Errorf("StructuredContent.ErrorType = %q, want %q", sc.ErrorType, autherr.REAUTH_REQUIRED)
	}
}

// ── tryAuthResult unit test ──

func TestTryAuthResult(t *testing.T) {
	t.Run("nil error returns false", func(t *testing.T) {
		result, ok := tryAuthResult(nil)
		if ok || result != nil {
			t.Error("tryAuthResult(nil) should return (nil, false)")
		}
	})

	t.Run("AUTH_REQUIRED error returns structured result", func(t *testing.T) {
		result, ok := tryAuthResult(autherr.NewAuthRequired())
		if !ok {
			t.Fatal("tryAuthResult() ok = false for AUTH_REQUIRED error")
		}
		ae := decodeAuthError(t, result)
		if ae.ErrorType != autherr.AUTH_REQUIRED {
			t.Errorf("ErrorType = %q, want %q", ae.ErrorType, autherr.AUTH_REQUIRED)
		}
	})

	t.Run("non-auth error returns false", func(t *testing.T) {
		result, ok := tryAuthResult(context.Canceled)
		if ok || result != nil {
			t.Error("tryAuthResult(context.Canceled) should return (nil, false)")
		}
	})
}
