package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubValidator is a minimal TokenValidator for testing.
type stubValidator struct {
	login string
	err   error
}

func (s *stubValidator) ValidateToken(_ context.Context, _ string) (string, error) {
	return s.login, s.err
}

func TestAuth_StandaloneMode(t *testing.T) {
	tests := []struct {
		name           string
		authHeader     string
		validatorLogin string
		validatorErr   error
		wantStatus     int
		wantLogin      string
	}{
		{
			name:       "missing token",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid token",
			authHeader:     "Bearer my-token",
			validatorLogin: "alice",
			wantStatus:     http.StatusOK,
			wantLogin:      "alice",
		},
		{
			name:         "invalid token",
			authHeader:   "Bearer bad-token",
			validatorErr: errFakeInvalid,
			wantStatus:   http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &stubValidator{login: tt.validatorLogin, err: tt.validatorErr}
			mw := Auth(v, AuthModeStandalone)

			var capturedLogin string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedLogin = LoginFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			mw(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantLogin != "" && capturedLogin != tt.wantLogin {
				t.Fatalf("login = %q, want %q", capturedLogin, tt.wantLogin)
			}
		})
	}
}

func TestAuth_GatewayMode(t *testing.T) {
	tests := []struct {
		name           string
		proxyLogin     string
		authHeader     string
		wantStatus     int
		wantLogin      string
		wantTokenInCtx string
	}{
		{
			name:       "missing X-Authenticated-User returns 401",
			proxyLogin: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid proxy identity without bearer returns 401",
			proxyLogin: "bob",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid proxy identity with bearer token propagated to context",
			proxyLogin:     "carol",
			authHeader:     "Bearer proxy-token",
			wantStatus:     http.StatusOK,
			wantLogin:      "carol",
			wantTokenInCtx: "proxy-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// In gateway mode, the validator should never be called.
			v := &stubValidator{err: errShouldNotCall}
			mw := Auth(v, AuthModeGateway)

			var capturedLogin, capturedToken string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedLogin = LoginFromContext(r.Context())
				capturedToken = TokenFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			if tt.proxyLogin != "" {
				req.Header.Set("X-Authenticated-User", tt.proxyLogin)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			mw(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantLogin != "" && capturedLogin != tt.wantLogin {
				t.Fatalf("login = %q, want %q", capturedLogin, tt.wantLogin)
			}
			if capturedToken != tt.wantTokenInCtx {
				t.Fatalf("token in context = %q, want %q", capturedToken, tt.wantTokenInCtx)
			}
		})
	}
}

// errFakeInvalid is a sentinel error representing an invalid-token validation failure.
type fakeError struct{ msg string }

func (e fakeError) Error() string { return e.msg }

var errFakeInvalid = fakeError{"invalid token"}
var errShouldNotCall = fakeError{"validator should not be called in gateway mode"}
