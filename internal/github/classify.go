package ghclient

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/go-github/v85/github"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
)

// ClassifyGitHubError maps a GitHub API or gateway error to a structured *autherr.AuthError.
// Returns nil when the error is not a recognized error type (e.g. context.Canceled).
//
// Detection order (most-specific first):
//  1. *autherr.AuthError — already classified upstream; returned as-is.
//  2. Gateway sentinel errors — ErrGateway* from gatewayTokenSource.Token().
//  3. *github.RateLimitError — primary rate limit; retryable, safe to continue after reset.
//  4. *github.AbuseRateLimitError — secondary / abuse rate limit; not retryable.
//  5. *github.ErrorResponse — classified by HTTP status code.
//  6. String-based fallback for shurcooL/githubv4 plain errors.
func ClassifyGitHubError(err error) *autherr.AuthError {
	if err == nil {
		return nil
	}

	// Already classified.
	if ae, ok := autherr.AsAuthError(err); ok {
		return ae
	}

	// Gateway sentinel errors from gatewayTokenSource.Token().
	// Checked before generic HTTP-status checks because sentinels carry more
	// precise semantics than a plain HTTP status code would.
	switch {
	case errors.Is(err, ErrGatewayRotationFailed):
		// Refresh token rotation rejected by GitHub OAuth provider; must re-auth.
		return autherr.NewTokenRefreshFailed()
	case errors.Is(err, ErrGatewaySubjectGone):
		// GitHub removed or revoked the subject; re-authentication is required.
		return autherr.NewReauthRequired()
	case errors.Is(err, ErrGatewayUpstreamFailure):
		// Transient resolver or upstream failure; retry may succeed.
		return autherr.NewTransientUpstreamError()
	case errors.Is(err, ErrGatewayUnauthorized):
		// Shared-secret misconfiguration; no usable token is available.
		return autherr.NewAuthRequired()
	case errors.Is(err, ErrGatewayLoopbackRequired):
		// Gateway endpoint is not on loopback; configuration error.
		return autherr.NewAuthRequired()
	case errors.Is(err, ErrGatewayBadRequest):
		// Request arguments rejected by the gateway; caller should not retry.
		return autherr.NewValidationError()
	}

	// Primary rate limit (go-github wraps 403 + X-RateLimit-Remaining: 0).
	var rateLimitErr *github.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return autherr.NewRateLimited(true, true)
	}

	// Secondary / abuse rate limit (go-github wraps 403 + abuse message).
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		return autherr.NewRateLimited(false, false)
	}

	// REST API errors with explicit HTTP status codes.
	var ghErr *github.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		switch ghErr.Response.StatusCode {
		case http.StatusUnauthorized:
			return autherr.NewReauthRequired()
		case http.StatusForbidden:
			return autherr.NewPermissionDenied()
		case http.StatusNotFound:
			return autherr.NewNotFound()
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return autherr.NewValidationError()
		case http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return autherr.NewTransientUpstreamError()
		}
	}

	// shurcooL/githubv4 plain-error fallbacks (string-based).
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401 Unauthorized"):
		return autherr.NewReauthRequired()
	case strings.Contains(msg, "403 Forbidden"):
		return autherr.NewPermissionDenied()
	case strings.Contains(msg, "404 Not Found"):
		return autherr.NewNotFound()
	case strings.Contains(msg, "422 Unprocessable"), strings.Contains(msg, "400 Bad Request"):
		return autherr.NewValidationError()
	case strings.Contains(msg, "500 Internal Server Error"),
		strings.Contains(msg, "502 Bad Gateway"),
		strings.Contains(msg, "503 Service Unavailable"),
		strings.Contains(msg, "504 Gateway Timeout"):
		return autherr.NewTransientUpstreamError()
	}

	return nil
}
