package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuth_TrustProxyHeaders(t *testing.T) {
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
			mw := Auth("", "")

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

func TestAuth_Fallback(t *testing.T) {
	tests := []struct {
		name           string
		defaultLogin   string
		defaultToken   string
		proxyLogin     string
		authHeader     string
		wantStatus     int
		wantLogin      string
		wantTokenInCtx string
	}{
		{
			name:           "missing headers falls back to default values",
			defaultLogin:   "default-user",
			defaultToken:   "default-token",
			proxyLogin:     "",
			authHeader:     "",
			wantStatus:     http.StatusOK,
			wantLogin:      "default-user",
			wantTokenInCtx: "default-token",
		},
		{
			name:           "headers take precedence over default values",
			defaultLogin:   "default-user",
			defaultToken:   "default-token",
			proxyLogin:     "override-user",
			authHeader:     "Bearer override-token",
			wantStatus:     http.StatusOK,
			wantLogin:      "override-user",
			wantTokenInCtx: "override-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := Auth(tt.defaultLogin, tt.defaultToken)

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
