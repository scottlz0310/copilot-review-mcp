package ghclient_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-github/v85/github"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
)

// fakeResponse constructs a minimal http.Response with the given status code for
// embedding in github.ErrorResponse.
func fakeResponse(status int) *http.Response {
	return &http.Response{StatusCode: status}
}

// ghErrorResponse returns a *github.ErrorResponse with the given HTTP status.
func ghErrorResponse(status int) error {
	return &github.ErrorResponse{Response: fakeResponse(status)}
}

func TestClassifyGitHubError_Nil(t *testing.T) {
	if got := ghclient.ClassifyGitHubError(nil); got != nil {
		t.Errorf("ClassifyGitHubError(nil) = %v, want nil", got)
	}
}

func TestClassifyGitHubError_AlreadyClassified(t *testing.T) {
	ae := autherr.NewNotFound()
	got := ghclient.ClassifyGitHubError(ae)
	if got == nil {
		t.Fatal("ClassifyGitHubError(*AuthError) = nil, want non-nil")
	}
	if got.ErrorType != autherr.NOT_FOUND {
		t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.NOT_FOUND)
	}
}

func TestClassifyGitHubError_WrappedAlreadyClassified(t *testing.T) {
	ae := autherr.NewPermissionDenied()
	wrapped := fmt.Errorf("outer: %w", ae)
	got := ghclient.ClassifyGitHubError(wrapped)
	if got == nil {
		t.Fatal("ClassifyGitHubError(wrapped *AuthError) = nil, want non-nil")
	}
	if got.ErrorType != autherr.PERMISSION_DENIED {
		t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.PERMISSION_DENIED)
	}
}

func TestClassifyGitHubError_RateLimitError(t *testing.T) {
	rlErr := &github.RateLimitError{
		Rate:     github.Rate{},
		Response: fakeResponse(http.StatusForbidden),
		Message:  "rate limit exceeded",
	}
	got := ghclient.ClassifyGitHubError(rlErr)
	if got == nil {
		t.Fatal("ClassifyGitHubError(RateLimitError) = nil, want non-nil")
	}
	if got.ErrorType != autherr.RATE_LIMITED {
		t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.RATE_LIMITED)
	}
	if !got.Retryable {
		t.Error("Retryable = false, want true for primary rate limit")
	}
	if !got.SafeToContinue {
		t.Error("SafeToContinue = false, want true for primary rate limit")
	}
}

func TestClassifyGitHubError_AbuseRateLimitError(t *testing.T) {
	abuseErr := &github.AbuseRateLimitError{
		Response: fakeResponse(http.StatusForbidden),
		Message:  "secondary rate limit",
	}
	got := ghclient.ClassifyGitHubError(abuseErr)
	if got == nil {
		t.Fatal("ClassifyGitHubError(AbuseRateLimitError) = nil, want non-nil")
	}
	if got.ErrorType != autherr.RATE_LIMITED {
		t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.RATE_LIMITED)
	}
	if got.Retryable {
		t.Error("Retryable = true, want false for secondary rate limit")
	}
	if got.SafeToContinue {
		t.Error("SafeToContinue = true, want false for secondary rate limit")
	}
}

func TestClassifyGitHubError_HTTPStatusCodes(t *testing.T) {
	cases := []struct {
		status    int
		wantType  autherr.AuthErrorType
	}{
		{http.StatusUnauthorized, autherr.REAUTH_REQUIRED},
		{http.StatusForbidden, autherr.PERMISSION_DENIED},
		{http.StatusNotFound, autherr.NOT_FOUND},
		{http.StatusBadRequest, autherr.VALIDATION_ERROR},
		{http.StatusUnprocessableEntity, autherr.VALIDATION_ERROR},
		{http.StatusInternalServerError, autherr.TRANSIENT_UPSTREAM_ERROR},
		{http.StatusBadGateway, autherr.TRANSIENT_UPSTREAM_ERROR},
		{http.StatusServiceUnavailable, autherr.TRANSIENT_UPSTREAM_ERROR},
		{http.StatusGatewayTimeout, autherr.TRANSIENT_UPSTREAM_ERROR},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			got := ghclient.ClassifyGitHubError(ghErrorResponse(tc.status))
			if got == nil {
				t.Fatalf("ClassifyGitHubError(HTTP %d) = nil, want %q", tc.status, tc.wantType)
			}
			if got.ErrorType != tc.wantType {
				t.Errorf("ErrorType = %q, want %q", got.ErrorType, tc.wantType)
			}
		})
	}
}

func TestClassifyGitHubError_GraphQLStrings(t *testing.T) {
	cases := []struct {
		msg      string
		wantType autherr.AuthErrorType
	}{
		{"non-200 OK status code: 401 Unauthorized body: bad creds", autherr.REAUTH_REQUIRED},
		{"non-200 OK status code: 403 Forbidden body: denied", autherr.PERMISSION_DENIED},
		{"non-200 OK status code: 404 Not Found body: missing", autherr.NOT_FOUND},
		{"non-200 OK status code: 422 Unprocessable Entity body: invalid", autherr.VALIDATION_ERROR},
		{"non-200 OK status code: 500 Internal Server Error body: oops", autherr.TRANSIENT_UPSTREAM_ERROR},
		{"non-200 OK status code: 502 Bad Gateway body: upstream", autherr.TRANSIENT_UPSTREAM_ERROR},
		{"non-200 OK status code: 503 Service Unavailable body: down", autherr.TRANSIENT_UPSTREAM_ERROR},
		{"non-200 OK status code: 504 Gateway Timeout body: slow", autherr.TRANSIENT_UPSTREAM_ERROR},
	}
	for _, tc := range cases {
		t.Run(tc.msg[:30], func(t *testing.T) {
			got := ghclient.ClassifyGitHubError(fmt.Errorf("%s", tc.msg))
			if got == nil {
				t.Fatalf("ClassifyGitHubError(%q) = nil, want %q", tc.msg, tc.wantType)
			}
			if got.ErrorType != tc.wantType {
				t.Errorf("ErrorType = %q, want %q", got.ErrorType, tc.wantType)
			}
		})
	}
}

func TestClassifyGitHubError_UnknownError(t *testing.T) {
	got := ghclient.ClassifyGitHubError(fmt.Errorf("something completely different"))
	if got != nil {
		t.Errorf("ClassifyGitHubError(unknown) = %v, want nil", got)
	}
}
