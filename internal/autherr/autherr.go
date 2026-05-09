// Package autherr defines structured authentication errors for MCP tool responses.
// These errors signal to AI agents that the review cycle cannot safely continue
// and that user action (re-authentication) is required.
package autherr

import "errors"

// AuthErrorType identifies the category of authentication error.
type AuthErrorType string

const (
	// AUTH_REQUIRED is returned when no authentication credentials exist.
	AUTH_REQUIRED AuthErrorType = "AUTH_REQUIRED"
	// REAUTH_REQUIRED is returned when credentials have expired or been rejected.
	REAUTH_REQUIRED AuthErrorType = "REAUTH_REQUIRED"
	// TOKEN_REFRESH_FAILED is returned when a token refresh attempt has failed.
	TOKEN_REFRESH_FAILED AuthErrorType = "TOKEN_REFRESH_FAILED"
)

// AuthError is a structured blocking error returned when authentication fails.
// It implements the error interface and serializes to JSON for AI agent consumption.
// All auth errors are non-retryable and require explicit user action.
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

// AsAuthError reports whether err (or any wrapped error) is an *AuthError.
// Returns the *AuthError and true if found, otherwise nil and false.
func AsAuthError(err error) (*AuthError, bool) {
	var ae *AuthError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
