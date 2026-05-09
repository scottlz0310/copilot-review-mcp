package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
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

// newReplySucceedResolveFail401Server returns a server where ReplyToThread succeeds
// but the resolveReviewThread GraphQL mutation returns 401 Unauthorized.
func newReplySucceedResolveFail401Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// REST: CreateCommentInReplyTo — path starts with /repos/
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			_, _ = fmt.Fprint(w, `{"id":999,"body":"reply"}`)
			return
		}

		// GraphQL: distinguish queries by body content.
		body, _ := io.ReadAll(r.Body)
		bs := string(body)

		switch {
		case strings.Contains(bs, "resolveReviewThread"):
			// Resolve mutation → 401
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"message":"Bad credentials"}`)
		case strings.Contains(bs, "databaseId"):
			// threadMetadataQuery → return minimal metadata
			_, _ = fmt.Fprint(w, `{"data":{"node":{"pullRequest":{"number":1,"repository":{"name":"r","owner":{"login":"o"}}},"comments":{"nodes":[{"databaseId":42}]}}}}`)
		default:
			// threadNodeQuery (isResolved check) → not yet resolved
			_, _ = fmt.Fprint(w, `{"data":{"node":{"isResolved":false}}}`)
		}
	}))
	return srv
}

// ── helpers for non-401 server responses ──────────────────────────────────────

// newStatusServer returns a test server that responds with the given HTTP status code.
func newStatusServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"message":"%s"}`, http.StatusText(status))
	}))
}

// makeStatusGitHubClient creates a *ghclient.Client pointing at the given server.
func makeStatusGitHubClient(srv *httptest.Server) *ghclient.Client {
	restClient := github.NewClient(srv.Client())
	base, _ := url.Parse(srv.URL + "/")
	restClient.BaseURL = base
	restClient.UploadURL = base
	gqlClient := githubv4.NewEnterpriseClient(srv.URL, srv.Client())
	return ghclient.NewWithClients(restClient, gqlClient, 30*time.Second)
}

// assertBlockingResult checks result is a non-nil error result with the given error type.
// Unlike assertAuthResult, it does not enforce UserActionRequired = true (some blocking
// error types like NOT_FOUND and TRANSIENT_UPSTREAM_ERROR have UserActionRequired = false).
func assertBlockingResult(t *testing.T, result *mcp.CallToolResult, err error, wantType autherr.AuthErrorType) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("handler returned nil result, want structured blocking error")
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
}

// ── PERMISSION_DENIED (403) ───────────────────────────────────────────────────

func TestStatusHandlerPermissionDenied(t *testing.T) {
	srv := newStatusServer(http.StatusForbidden)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(staticProvider(makeStatusGitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertBlockingResult(t, result, err, autherr.PERMISSION_DENIED)
}

// ── NOT_FOUND (404) ───────────────────────────────────────────────────────────

func TestStatusHandlerNotFound(t *testing.T) {
	srv := newStatusServer(http.StatusNotFound)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(staticProvider(makeStatusGitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertBlockingResult(t, result, err, autherr.NOT_FOUND)
}

// ── VALIDATION_ERROR (422) ────────────────────────────────────────────────────

func TestStatusHandlerValidationError(t *testing.T) {
	srv := newStatusServer(http.StatusUnprocessableEntity)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(staticProvider(makeStatusGitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertBlockingResult(t, result, err, autherr.VALIDATION_ERROR)
}

// ── TRANSIENT_UPSTREAM_ERROR (503) ────────────────────────────────────────────

func TestStatusHandlerTransientUpstreamError(t *testing.T) {
	srv := newStatusServer(http.StatusServiceUnavailable)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := statusHandler(staticProvider(makeStatusGitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, GetStatusInput{Owner: "o", Repo: "r", PR: 1})
	assertBlockingResult(t, result, err, autherr.TRANSIENT_UPSTREAM_ERROR)
}

// ── tryAuthResult covers new error types ─────────────────────────────────────

func TestTryAuthResultNewErrorTypes(t *testing.T) {
	cases := []struct {
		name     string
		fn       func() *autherr.AuthError
		wantType autherr.AuthErrorType
	}{
		{"PERMISSION_DENIED", autherr.NewPermissionDenied, autherr.PERMISSION_DENIED},
		{"NOT_FOUND", autherr.NewNotFound, autherr.NOT_FOUND},
		{"VALIDATION_ERROR", autherr.NewValidationError, autherr.VALIDATION_ERROR},
		{"TRANSIENT_UPSTREAM_ERROR", autherr.NewTransientUpstreamError, autherr.TRANSIENT_UPSTREAM_ERROR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, ok := tryAuthResult(tc.fn())
			if !ok {
				t.Fatalf("tryAuthResult() ok = false for %s", tc.name)
			}
			ae := decodeAuthError(t, result)
			if ae.ErrorType != tc.wantType {
				t.Errorf("ErrorType = %q, want %q", ae.ErrorType, tc.wantType)
			}
		})
	}
}

// ── TestReplyAndResolveHandlerResolveReauthRequired ───────────────────────────

// TestReplyAndResolveHandlerResolveReauthRequired verifies that when the
// resolveReviewThread mutation returns 401, the handler sets ResolveError to a
// canonical REAUTH_REQUIRED string instead of a hard-coded message.
func TestReplyAndResolveHandlerResolveReauthRequired(t *testing.T) {
	srv := newReplySucceedResolveFail401Server(t)
	t.Cleanup(srv.Close)

	handler := replyAndResolveHandler(staticProvider(make401GitHubClient(srv)))
	result, out, err := handler(
		context.Background(), nil,
		ReplyAndResolveInput{ThreadID: "PRRT_abc", Body: "hello", Resolve: true},
	)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("result = %v, want nil", result)
	}
	if !out.Replied {
		t.Error("out.Replied = false, want true")
	}
	if out.CommentID == nil {
		t.Error("out.CommentID = nil, want non-nil")
	}
	if out.Resolved {
		t.Error("out.Resolved = true, want false")
	}
	if out.ResolveError == nil {
		t.Fatal("out.ResolveError = nil, want REAUTH_REQUIRED error string")
	}
	if !strings.HasPrefix(*out.ResolveError, string(autherr.REAUTH_REQUIRED)) {
		t.Errorf("ResolveError = %q, want prefix %q", *out.ResolveError, autherr.REAUTH_REQUIRED)
	}
}
