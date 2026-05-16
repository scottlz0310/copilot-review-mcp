package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestNewGatewayTokenSource_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  GatewayTokenSourceConfig
	}{
		{name: "missing url", cfg: GatewayTokenSourceConfig{Secret: "s", Subject: "alice"}},
		{name: "missing secret", cfg: GatewayTokenSourceConfig{EndpointURL: "http://127.0.0.1:1/x", Subject: "alice"}},
		{name: "missing subject", cfg: GatewayTokenSourceConfig{EndpointURL: "http://127.0.0.1:1/x", Secret: "s"}},
		{name: "bad scheme", cfg: GatewayTokenSourceConfig{EndpointURL: "ftp://127.0.0.1/x", Secret: "s", Subject: "alice"}},
		{name: "non-loopback", cfg: GatewayTokenSourceConfig{EndpointURL: "http://example.com/x", Secret: "s", Subject: "alice"}},
		{name: "non-loopback ip", cfg: GatewayTokenSourceConfig{EndpointURL: "http://10.0.0.1/x", Secret: "s", Subject: "alice"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewGatewayTokenSource(tc.cfg); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestNewGatewayTokenSource_NonLoopbackError(t *testing.T) {
	t.Parallel()
	_, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: "http://example.com/internal/v1/whoami",
		Secret:      "supersecretsupersecretsupersecret",
		Subject:     "alice",
	})
	if !errors.Is(err, ErrGatewayNonLoopback) {
		t.Fatalf("expected ErrGatewayNonLoopback, got %v", err)
	}
}

// TestNewGatewayTokenSource_InvalidPathError verifies that a loopback URL
// missing the /whoami suffix is rejected at construction so the misconfiguration
// surfaces at startup rather than at the first watch's first whoami round-trip
// (where it would manifest as a confusing 404 / ErrGatewaySubjectGone).
func TestNewGatewayTokenSource_InvalidPathError(t *testing.T) {
	t.Parallel()
	cases := []string{
		"http://127.0.0.1:8081",
		"http://127.0.0.1:8081/",
		"http://127.0.0.1:8081/internal/v1",
		"http://127.0.0.1:8081/health",
	}
	for _, u := range cases {
		u := u
		t.Run(u, func(t *testing.T) {
			t.Parallel()
			_, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
				EndpointURL: u,
				Secret:      "s",
				Subject:     "alice",
			})
			if !errors.Is(err, ErrGatewayInvalidPath) {
				t.Fatalf("expected ErrGatewayInvalidPath, got %v", err)
			}
		})
	}
}

// TestValidateGatewayEndpoint covers the subject-independent startup-time
// validation used by cmd/server/main.go before any watch exists.
func TestValidateGatewayEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		secret  string
		wantErr error
	}{
		{name: "ok", url: "http://127.0.0.1:8081/internal/v1/whoami", secret: "s", wantErr: nil},
		{name: "ok localhost", url: "http://localhost:8081/internal/v1/whoami", secret: "s", wantErr: nil},
		{name: "missing url", url: "", secret: "s", wantErr: errors.New("required")},
		{name: "missing secret", url: "http://127.0.0.1:8081/internal/v1/whoami", secret: "", wantErr: errors.New("required")},
		{name: "bad scheme", url: "ftp://127.0.0.1/internal/v1/whoami", secret: "s", wantErr: errors.New("scheme")},
		{name: "non-loopback", url: "http://example.com/internal/v1/whoami", secret: "s", wantErr: ErrGatewayNonLoopback},
		{name: "no path", url: "http://127.0.0.1:8081", secret: "s", wantErr: ErrGatewayInvalidPath},
		{name: "wrong path", url: "http://127.0.0.1:8081/health", secret: "s", wantErr: ErrGatewayInvalidPath},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateGatewayEndpoint(tc.url, tc.secret)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			// Sentinel errors use errors.Is; ad-hoc errors are matched on substring.
			if errors.Is(tc.wantErr, ErrGatewayNonLoopback) || errors.Is(tc.wantErr, ErrGatewayInvalidPath) {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected sentinel %v, got %v", tc.wantErr, err)
				}
				return
			}
			if !strings.Contains(err.Error(), tc.wantErr.Error()) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr.Error(), err)
			}
		})
	}
}

func TestNewGatewayTokenSource_AcceptsLoopbackVariants(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"127.0.0.1", "[::1]", "localhost"} {
		host := host
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			_, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
				EndpointURL: "http://" + host + ":1234/internal/v1/whoami",
				Secret:      "s",
				Subject:     "alice",
			})
			if err != nil {
				t.Fatalf("unexpected error for host %q: %v", host, err)
			}
		})
	}
}

// fakeWhoamiServer returns a test server that asserts the request shape and
// responds with the configured status/body. respBody may be nil to omit body.
func fakeWhoamiServer(t *testing.T, wantSecret, wantSubject string, status int, respBody any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%q, want POST", r.Method)
		}
		if r.URL.Path != "/internal/v1/whoami" {
			t.Errorf("path=%q, want /internal/v1/whoami", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantSecret {
			t.Errorf("Authorization=%q, want Bearer %s", got, wantSecret)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type=%q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["subject"] != wantSubject {
			t.Errorf("subject=%q, want %q", body["subject"], wantSubject)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respBody != nil {
			_ = json.NewEncoder(w).Encode(respBody)
		}
	}))
}

func TestGatewayTokenSource_HappyPath(t *testing.T) {
	t.Parallel()
	expiresAt := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	srv := fakeWhoamiServer(t, "secret-32-chars-long-XXXXXXXXXX01", "alice", http.StatusOK, whoamiResponse{
		AccessToken: "gho_abc",
		TokenType:   "bearer",
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		Scopes:      []string{"repo"},
	})
	t.Cleanup(srv.Close)

	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "secret-32-chars-long-XXXXXXXXXX01",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "gho_abc" {
		t.Errorf("AccessToken=%q", tok.AccessToken)
	}
	if tok.TokenType != "bearer" {
		t.Errorf("TokenType=%q", tok.TokenType)
	}
	if !tok.Expiry.Equal(expiresAt) {
		t.Errorf("Expiry=%v, want %v", tok.Expiry, expiresAt)
	}
}

func TestGatewayTokenSource_ErrorMappings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   any
		want   error
	}{
		{"401", http.StatusUnauthorized, map[string]string{"code": "x"}, ErrGatewayUnauthorized},
		{"403", http.StatusForbidden, map[string]string{"code": "x"}, ErrGatewayLoopbackRequired},
		{"404", http.StatusNotFound, map[string]string{"code": "x"}, ErrGatewaySubjectGone},
		// 502 body parse: rotation_failed → ErrGatewayRotationFailed (#33)
		{"502/rotation_failed", http.StatusBadGateway, map[string]string{"error": "rotation_failed"}, ErrGatewayRotationFailed},
		// 502 body parse: upstream_failure → ErrGatewayUpstreamFailure (#33)
		{"502/upstream_failure", http.StatusBadGateway, map[string]string{"error": "upstream_failure"}, ErrGatewayUpstreamFailure},
		// 502 with unknown / non-JSON body → fallback to ErrGatewayUpstreamFailure
		{"502/unknown_body", http.StatusBadGateway, map[string]string{"code": "x"}, ErrGatewayUpstreamFailure},
		{"400", http.StatusBadRequest, map[string]string{"code": "x"}, ErrGatewayBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := fakeWhoamiServer(t, "s", "alice", tc.status, tc.body)
			t.Cleanup(srv.Close)
			ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
				EndpointURL: srv.URL + "/internal/v1/whoami",
				Secret:      "s",
				Subject:     "alice",
				HTTPClient:  srv.Client(),
			})
			if err != nil {
				t.Fatalf("NewGatewayTokenSource: %v", err)
			}
			_, err = ts.Token()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want errors.Is %v", err, tc.want)
			}
		})
	}
}

func TestGatewayTokenSource_MissingAccessToken(t *testing.T) {
	t.Parallel()
	srv := fakeWhoamiServer(t, "s", "alice", http.StatusOK, whoamiResponse{TokenType: "bearer"})
	t.Cleanup(srv.Close)
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	if _, err := ts.Token(); err == nil || !strings.Contains(err.Error(), "access_token") {
		t.Fatalf("expected access_token error, got %v", err)
	}
}

func TestGatewayTokenSource_MissingExpiry_Errors(t *testing.T) {
	t.Parallel()
	// Gateway response with no expires_at must fail closed: returning a
	// zero Expiry would let oauth2.ReuseTokenSource cache the token
	// forever and silently disable refresh after the real token expires.
	srv := fakeWhoamiServer(t, "s", "alice", http.StatusOK, whoamiResponse{
		AccessToken: "gho_x",
		TokenType:   "bearer",
	})
	t.Cleanup(srv.Close)
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	if _, err := ts.Token(); !errors.Is(err, ErrGatewayInvalidExpiry) {
		t.Fatalf("expected ErrGatewayInvalidExpiry, got %v", err)
	}
}

func TestGatewayTokenSource_InvalidExpiry_Errors(t *testing.T) {
	t.Parallel()
	srv := fakeWhoamiServer(t, "s", "alice", http.StatusOK, whoamiResponse{
		AccessToken: "gho_x",
		TokenType:   "bearer",
		ExpiresAt:   "not-a-timestamp",
	})
	t.Cleanup(srv.Close)
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	if _, err := ts.Token(); !errors.Is(err, ErrGatewayInvalidExpiry) {
		t.Fatalf("expected ErrGatewayInvalidExpiry, got %v", err)
	}
}

func TestGatewayTokenSource_ReuseTokenSourceCachesUntilExpiry(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(whoamiResponse{
			AccessToken: "gho_n",
			TokenType:   "bearer",
			ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	reuse := oauth2.ReuseTokenSource(nil, ts)
	for i := 0; i < 5; i++ {
		if _, err := reuse.Token(); err != nil {
			t.Fatalf("Token #%d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("whoami calls=%d, want 1 (ReuseTokenSource should cache)", calls)
	}
}

// slowResponder pauses for the configured duration to simulate a slow gateway,
// allowing the test to assert that the source's per-call timeout fires.
// The body is a fully valid whoamiResponse (with expires_at) so any failure
// must come from the timeout itself, not from validation falling through.
type slowResponder struct {
	delay time.Duration
}

func (s slowResponder) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	time.Sleep(s.delay)
	w.WriteHeader(http.StatusOK)
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_, _ = io.WriteString(w, `{"access_token":"x","token_type":"bearer","expires_at":"`+exp+`"}`)
}

func TestGatewayTokenSource_HTTPClientTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(slowResponder{delay: 200 * time.Millisecond})
	t.Cleanup(srv.Close)
	hc := &http.Client{Timeout: 20 * time.Millisecond}
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  hc,
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	_, err = ts.Token()
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// Must be a timeout, not (for example) ErrGatewayInvalidExpiry from a
	// request that unexpectedly completed. http.Client.Timeout surfaces as
	// a url.Error whose underlying err satisfies net.Error.Timeout(); the
	// context deadline path satisfies errors.Is(err, context.DeadlineExceeded).
	var nerr net.Error
	isTimeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout())
	if !isTimeout {
		t.Fatalf("expected timeout error (context.DeadlineExceeded or net.Error.Timeout()=true), got %v", err)
	}
}

// Verify that watch cancellation propagates: when the parent context passed
// via GatewayTokenSourceConfig.Context is cancelled, Token() must fail rather
// than complete the whoami round-trip. Aligns Token's semantics with the
// Phase B PR-A goal of letting watch/server shutdown stop in-flight token
// refresh.
func TestGatewayTokenSource_ParentContextCancelPropagates(t *testing.T) {
	t.Parallel()
	srv := fakeWhoamiServer(t, "s", "alice", http.StatusOK, whoamiResponse{
		AccessToken: "gho_x",
		TokenType:   "bearer",
		ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	t.Cleanup(srv.Close)
	parent, cancel := context.WithCancel(context.Background())
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
		Context:     parent,
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	cancel()
	_, err = ts.Token()
	if err == nil {
		t.Fatal("expected Token() to fail when parent context is cancelled, got nil")
	}
	// Assert the cancellation actually propagated — not that the request
	// completed and then failed validation (e.g. ErrGatewayInvalidExpiry).
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// When no parent context is configured (nil Context), Token() must still
// succeed using context.Background(): existing callers that pre-date the
// Context field continue to work.
func TestGatewayTokenSource_NoParentContext_StillWorks(t *testing.T) {
	t.Parallel()
	srv := fakeWhoamiServer(t, "s", "alice", http.StatusOK, whoamiResponse{
		AccessToken: "gho_x",
		TokenType:   "bearer",
		ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	t.Cleanup(srv.Close)
	ts, err := NewGatewayTokenSource(GatewayTokenSourceConfig{
		EndpointURL: srv.URL + "/internal/v1/whoami",
		Secret:      "s",
		Subject:     "alice",
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewGatewayTokenSource: %v", err)
	}
	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
}
