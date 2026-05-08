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
	}{
		{"NewAuthRequired", autherr.NewAuthRequired, autherr.AUTH_REQUIRED},
		{"NewReauthRequired", autherr.NewReauthRequired, autherr.REAUTH_REQUIRED},
		{"NewTokenRefreshFailed", autherr.NewTokenRefreshFailed, autherr.TOKEN_REFRESH_FAILED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := tt.fn()

			if ae.OK {
				t.Error("OK must be false for auth errors")
			}
			if ae.ErrorType != tt.wantErrorType {
				t.Errorf("ErrorType = %q, want %q", ae.ErrorType, tt.wantErrorType)
			}
			if ae.Severity != "blocking" {
				t.Errorf("Severity = %q, want %q", ae.Severity, "blocking")
			}
			if ae.Retryable {
				t.Error("Retryable must be false")
			}
			if !ae.UserActionRequired {
				t.Error("UserActionRequired must be true")
			}
			if ae.SafeToContinue {
				t.Error("SafeToContinue must be false")
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
	if autherr.AUTH_REQUIRED != "AUTH_REQUIRED" {
		t.Errorf("AUTH_REQUIRED = %q, want %q", autherr.AUTH_REQUIRED, "AUTH_REQUIRED")
	}
	if autherr.REAUTH_REQUIRED != "REAUTH_REQUIRED" {
		t.Errorf("REAUTH_REQUIRED = %q, want %q", autherr.REAUTH_REQUIRED, "REAUTH_REQUIRED")
	}
	if autherr.TOKEN_REFRESH_FAILED != "TOKEN_REFRESH_FAILED" {
		t.Errorf("TOKEN_REFRESH_FAILED = %q, want %q", autherr.TOKEN_REFRESH_FAILED, "TOKEN_REFRESH_FAILED")
	}
}
