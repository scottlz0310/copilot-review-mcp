package autherr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
)

func TestAuthErrorImplementsError(t *testing.T) {
	tests := []struct {
		name string
		fn   func() *autherr.AuthError
	}{
		{"NewAuthRequired", autherr.NewAuthRequired},
		{"NewReauthRequired", autherr.NewReauthRequired},
		{"NewTokenRefreshFailed", autherr.NewTokenRefreshFailed},
		{"NewPermissionDenied", autherr.NewPermissionDenied},
		{"NewRateLimited(true,true)", func() *autherr.AuthError { return autherr.NewRateLimited(true, true) }},
		{"NewRateLimited(false,false)", func() *autherr.AuthError { return autherr.NewRateLimited(false, false) }},
		{"NewNotFound", autherr.NewNotFound},
		{"NewValidationError", autherr.NewValidationError},
		{"NewTransientUpstreamError", autherr.NewTransientUpstreamError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := tt.fn()
			var err error = ae // must satisfy error interface
			if err.Error() == "" {
				t.Error("Error() must not be empty")
			}
		})
	}
}

func TestAuthErrorFields(t *testing.T) {
	tests := []struct {
		name          string
		fn            func() *autherr.AuthError
		wantErrorType autherr.AuthErrorType
		wantRetryable bool
		wantUserAction bool
		wantSafeCont  bool
	}{
		{"NewAuthRequired", autherr.NewAuthRequired, autherr.AUTH_REQUIRED, false, true, false},
		{"NewReauthRequired", autherr.NewReauthRequired, autherr.REAUTH_REQUIRED, false, true, false},
		{"NewTokenRefreshFailed", autherr.NewTokenRefreshFailed, autherr.TOKEN_REFRESH_FAILED, false, true, false},
		{"NewPermissionDenied", autherr.NewPermissionDenied, autherr.PERMISSION_DENIED, false, true, false},
		{"NewRateLimited(true,true)", func() *autherr.AuthError { return autherr.NewRateLimited(true, true) }, autherr.RATE_LIMITED, true, false, true},
		{"NewRateLimited(false,false)", func() *autherr.AuthError { return autherr.NewRateLimited(false, false) }, autherr.RATE_LIMITED, false, false, false},
		{"NewNotFound", autherr.NewNotFound, autherr.NOT_FOUND, false, false, false},
		{"NewValidationError", autherr.NewValidationError, autherr.VALIDATION_ERROR, false, false, false},
		{"NewTransientUpstreamError", autherr.NewTransientUpstreamError, autherr.TRANSIENT_UPSTREAM_ERROR, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := tt.fn()

			if ae.OK {
				t.Error("OK must be false for all structured errors")
			}
			if ae.ErrorType != tt.wantErrorType {
				t.Errorf("ErrorType = %q, want %q", ae.ErrorType, tt.wantErrorType)
			}
			if ae.Severity != "blocking" {
				t.Errorf("Severity = %q, want %q", ae.Severity, "blocking")
			}
			if ae.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %v, want %v", ae.Retryable, tt.wantRetryable)
			}
			if ae.UserActionRequired != tt.wantUserAction {
				t.Errorf("UserActionRequired = %v, want %v", ae.UserActionRequired, tt.wantUserAction)
			}
			if ae.SafeToContinue != tt.wantSafeCont {
				t.Errorf("SafeToContinue = %v, want %v", ae.SafeToContinue, tt.wantSafeCont)
			}
			if ae.Message == "" {
				t.Error("Message must not be empty")
			}
			if ae.RecommendedAgentAction == "" {
				t.Error("RecommendedAgentAction must not be empty")
			}
		})
	}
}

func TestAsAuthError(t *testing.T) {
	t.Run("direct auth error", func(t *testing.T) {
		ae := autherr.NewAuthRequired()
		got, ok := autherr.AsAuthError(ae)
		if !ok {
			t.Fatal("AsAuthError() ok = false, want true")
		}
		if got.ErrorType != autherr.AUTH_REQUIRED {
			t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.AUTH_REQUIRED)
		}
	})

	t.Run("wrapped auth error is detected", func(t *testing.T) {
		ae := autherr.NewReauthRequired()
		wrapped := fmt.Errorf("outer: %w", ae)
		got, ok := autherr.AsAuthError(wrapped)
		if !ok {
			t.Fatal("AsAuthError() ok = false for wrapped error, want true")
		}
		if got.ErrorType != autherr.REAUTH_REQUIRED {
			t.Errorf("ErrorType = %q, want %q", got.ErrorType, autherr.REAUTH_REQUIRED)
		}
	})

	t.Run("non-auth error returns false", func(t *testing.T) {
		err := errors.New("some other error")
		_, ok := autherr.AsAuthError(err)
		if ok {
			t.Fatal("AsAuthError() ok = true for non-auth error, want false")
		}
	})

	t.Run("nil error returns false", func(t *testing.T) {
		_, ok := autherr.AsAuthError(nil)
		if ok {
			t.Fatal("AsAuthError() ok = true for nil, want false")
		}
	})
}

func TestAuthErrorTypes(t *testing.T) {
	cases := map[autherr.AuthErrorType]string{
		autherr.AUTH_REQUIRED:            "AUTH_REQUIRED",
		autherr.REAUTH_REQUIRED:          "REAUTH_REQUIRED",
		autherr.TOKEN_REFRESH_FAILED:     "TOKEN_REFRESH_FAILED",
		autherr.PERMISSION_DENIED:        "PERMISSION_DENIED",
		autherr.RATE_LIMITED:             "RATE_LIMITED",
		autherr.NOT_FOUND:                "NOT_FOUND",
		autherr.VALIDATION_ERROR:         "VALIDATION_ERROR",
		autherr.TRANSIENT_UPSTREAM_ERROR: "TRANSIENT_UPSTREAM_ERROR",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("%s = %q, want %q", want, got, want)
		}
	}
}
