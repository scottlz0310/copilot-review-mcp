// Package autherr defines structured blocking errors for MCP tool responses.
// These errors signal to AI agents that an operation cannot safely continue
// and provide guidance on the appropriate next action (re-authentication,
// retry after back-off, or stopping the review cycle).
package autherr

import "errors"

// AuthErrorType identifies the category of blocking error.
type AuthErrorType string

const (
	// AUTH_REQUIRED is returned when no authentication credentials exist.
	AUTH_REQUIRED AuthErrorType = "AUTH_REQUIRED"
	// REAUTH_REQUIRED is returned when credentials have expired or been rejected.
	REAUTH_REQUIRED AuthErrorType = "REAUTH_REQUIRED"
	// TOKEN_REFRESH_FAILED is returned when a token refresh attempt has failed.
	TOKEN_REFRESH_FAILED AuthErrorType = "TOKEN_REFRESH_FAILED"
	// PERMISSION_DENIED is returned when the token lacks sufficient permission (HTTP 403).
	PERMISSION_DENIED AuthErrorType = "PERMISSION_DENIED"
	// RATE_LIMITED is returned when GitHub's primary or secondary rate limit is hit.
	RATE_LIMITED AuthErrorType = "RATE_LIMITED"
	// NOT_FOUND is returned when the requested resource (repo, PR, thread) does not exist.
	NOT_FOUND AuthErrorType = "NOT_FOUND"
	// VALIDATION_ERROR is returned when the request arguments are invalid (HTTP 400/422).
	VALIDATION_ERROR AuthErrorType = "VALIDATION_ERROR"
	// TRANSIENT_UPSTREAM_ERROR is returned for temporary errors on the GitHub API side (5xx).
	TRANSIENT_UPSTREAM_ERROR AuthErrorType = "TRANSIENT_UPSTREAM_ERROR"
)

// AuthError is a structured blocking error returned when a GitHub API call fails in a
// way that prevents the review cycle from continuing safely. It implements the error
// interface and serializes to JSON for AI agent consumption.
type AuthError struct {
	OK                     bool          `json:"ok"`
	ErrorType              AuthErrorType `json:"error_type"`
	Severity               string        `json:"severity"`
	Retryable              bool          `json:"retryable"`
	UserActionRequired     bool          `json:"user_action_required"`
	SafeToContinue         bool          `json:"safe_to_continue"`
	Message                string        `json:"message"`
	RecommendedAgentAction string        `json:"recommended_agent_action"`
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	return e.Message
}

// NewAuthRequired returns an AUTH_REQUIRED error for missing authentication context.
// Use when the request carries no GitHub token or the user identity (login) is absent.
func NewAuthRequired() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              AUTH_REQUIRED,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     true,
		SafeToContinue:         false,
		Message:                "GitHub authentication context is missing. Please sign in with a valid GitHub account before continuing.",
		RecommendedAgentAction: "Stop the current operation and ask the user to provide GitHub authentication credentials.",
	}
}

// NewReauthRequired returns a REAUTH_REQUIRED error for expired or rejected credentials.
// Use when GitHub responds with HTTP 401 Unauthorized.
func NewReauthRequired() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              REAUTH_REQUIRED,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     true,
		SafeToContinue:         false,
		Message:                "GitHub authentication has expired. Please re-authenticate before continuing.",
		RecommendedAgentAction: "Stop the review cycle and ask the user to re-authenticate.",
	}
}

// NewTokenRefreshFailed returns a TOKEN_REFRESH_FAILED error when token refresh fails.
// Use when a gateway or OAuth refresh attempt is explicitly rejected.
func NewTokenRefreshFailed() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              TOKEN_REFRESH_FAILED,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     true,
		SafeToContinue:         false,
		Message:                "GitHub token refresh failed. Please re-authenticate before continuing.",
		RecommendedAgentAction: "Stop the review cycle and ask the user to re-authenticate.",
	}
}

// NewPermissionDenied returns a PERMISSION_DENIED error for HTTP 403 responses.
// Use when GitHub rejects the request because the token lacks the required permission.
func NewPermissionDenied() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              PERMISSION_DENIED,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     true,
		SafeToContinue:         false,
		Message:                "GitHub permission denied. The token does not have sufficient permission for this operation.",
		RecommendedAgentAction: "Stop the current operation and ask the user to grant the required GitHub permission.",
	}
}

// NewRateLimited returns a RATE_LIMITED error for GitHub primary or secondary rate limits.
// retryable should be true for primary rate limits (retry after the reset window)
// and false for secondary / abuse rate limits.
// safeToContinue should be true only for primary rate limits where a timed retry is feasible.
func NewRateLimited(retryable, safeToContinue bool) *AuthError {
	msg := "GitHub secondary rate limit hit. Backing off before retrying is required."
	action := "Stop the review cycle and wait before retrying. Do not retry immediately."
	if retryable && safeToContinue {
		msg = "GitHub rate limit exceeded. Wait until the rate-limit window resets before retrying."
		action = "Wait until the rate-limit window resets, then retry the operation."
	}
	return &AuthError{
		OK:                     false,
		ErrorType:              RATE_LIMITED,
		Severity:               "blocking",
		Retryable:              retryable,
		UserActionRequired:     false,
		SafeToContinue:         safeToContinue,
		Message:                msg,
		RecommendedAgentAction: action,
	}
}

// NewNotFound returns a NOT_FOUND error when the requested resource does not exist.
// Use when GitHub responds with HTTP 404 for a repo, PR, or review thread.
func NewNotFound() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              NOT_FOUND,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     false,
		SafeToContinue:         false,
		Message:                "The requested GitHub resource was not found. The repo, PR, or thread may not exist or the token may not have read access.",
		RecommendedAgentAction: "Verify that the owner, repo, and PR number are correct, then stop the review cycle.",
	}
}

// NewValidationError returns a VALIDATION_ERROR for invalid request arguments (HTTP 400/422).
// Use when GitHub rejects the request because the parameters are malformed or out of range.
func NewValidationError() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              VALIDATION_ERROR,
		Severity:               "blocking",
		Retryable:              false,
		UserActionRequired:     false,
		SafeToContinue:         false,
		Message:                "The request was rejected by GitHub due to invalid arguments.",
		RecommendedAgentAction: "Check the request parameters for correctness and stop the current operation.",
	}
}

// NewTransientUpstreamError returns a TRANSIENT_UPSTREAM_ERROR for temporary GitHub API failures (5xx).
// Use when GitHub returns a 5xx error that is not directly caused by the request itself.
func NewTransientUpstreamError() *AuthError {
	return &AuthError{
		OK:                     false,
		ErrorType:              TRANSIENT_UPSTREAM_ERROR,
		Severity:               "blocking",
		Retryable:              true,
		UserActionRequired:     false,
		SafeToContinue:         false,
		Message:                "A transient error occurred on the GitHub API side. Retrying after a short delay may succeed.",
		RecommendedAgentAction: "Wait a moment and retry the operation. If the error persists, stop the review cycle and report to the user.",
	}
}

// AsAuthError reports whether err (or any wrapped error) is an *AuthError.
// Returns the *AuthError and true if found, otherwise nil and false.
func AsAuthError(err error) (*AuthError, bool) {
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
