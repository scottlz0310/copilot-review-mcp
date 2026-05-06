package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const ContextKeyLogin contextKey = "github_login"
const ContextKeyToken contextKey = "github_token"

// Auth returns a middleware that trusts the X-Authenticated-User header and
// Bearer token injected by an upstream proxy (mcp-gateway). Standalone OAuth
// has been removed; mcp-gateway is required.
func Auth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			login := r.Header.Get("X-Authenticated-User")
			if login == "" {
				writeUnauthorized(w, "missing_proxy_identity")
				return
			}
			token := extractBearer(r)
			if token == "" {
				writeUnauthorized(w, "missing_token")
				return
			}
			ctx := context.WithValue(r.Context(), ContextKeyLogin, login)
			ctx = context.WithValue(ctx, ContextKeyToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnauthorized(w http.ResponseWriter, errCode string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="copilot-review-mcp"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": errCode})
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	fields := strings.Fields(h)
	if len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
		return fields[1]
	}
	return ""
}

// TokenFromContext retrieves the GitHub token injected by Auth middleware.
func TokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ContextKeyToken).(string)
	return v
}

// LoginFromContext retrieves the GitHub login injected by Auth middleware.
func LoginFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ContextKeyLogin).(string)
	return v
}
