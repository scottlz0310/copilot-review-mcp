package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Gateway delegated background access (Phase B) sentinel errors.
//
// PR-A purposefully keeps these as opaque sentinels so callers can use
// errors.Is. The mapping to higher-level failure reasons (e.g.
// FailureReasonAuthExpired) and recovery hints is deferred to PR-B per
// the design in scottlz0310/copilot-review-mcp#29.
var (
	// ErrGatewaySubjectGone indicates the gateway no longer has a cached
	// token record for the subject (HTTP 404). This is permanent until the
	// user issues a fresh client request that re-seeds the gateway cache.
	ErrGatewaySubjectGone = errors.New("gateway: subject not found (404)")

	// ErrGatewayUnauthorized indicates the shared secret was rejected by
	// the gateway (HTTP 401). This is a configuration error on this side.
	ErrGatewayUnauthorized = errors.New("gateway: invalid shared secret (401)")

	// ErrGatewayLoopbackRequired indicates the gateway rejected the request
	// because it did not originate from a loopback address (HTTP 403). This
	// generally means client and gateway are not co-located on the same host.
	ErrGatewayLoopbackRequired = errors.New("gateway: loopback required (403)")

	// ErrGatewayUpstreamFailure indicates the gateway tried to rotate the
	// token but the GitHub OAuth provider failed (HTTP 502). Retryable.
	ErrGatewayUpstreamFailure = errors.New("gateway: upstream rotation failure (502)")

	// ErrGatewayBadRequest covers 4xx responses that are neither 401/403/404.
	ErrGatewayBadRequest = errors.New("gateway: bad request (4xx)")
)

// ErrGatewayNonLoopback is returned by NewGatewayTokenSource when the
// configured URL does not resolve to a loopback host. Co-location with the
// gateway on the same host is a Phase B PoC requirement; cross-host delegated
// access is future work (see docs/spike-72-delegated-background-access.md
// "Future work").
var ErrGatewayNonLoopback = errors.New("gateway: endpoint URL must be loopback (127.0.0.1 / ::1 / localhost)")

// ErrGatewayInvalidPath is returned when the gateway endpoint URL is missing
// the expected whoami path. The README/changelog document EndpointURL as the
// full whoami endpoint (e.g. http://127.0.0.1:8081/internal/v1/whoami), so a
// URL that only carries the host:port would silently fail at runtime with a
// confusing 404. This error makes that misconfiguration fail at startup.
var ErrGatewayInvalidPath = errors.New(`gateway: endpoint URL must end with "/whoami" (e.g. http://127.0.0.1:8081/internal/v1/whoami)`)

// DefaultGatewayTimeout bounds a single whoami round-trip. The same value is
// used for both the default http.Client.Timeout (when caller does not supply
// one) and the per-call context deadline derived from Context, so request-
// level cancellation and transport-level timeout agree.
//
// Exported so callers that construct their own *http.Client (e.g.
// cmd/server/main.go's buildGatewayClientFactory) can share this single
// source of truth and avoid the context-deadline vs transport-timeout drift
// previously fixed in this package.
const DefaultGatewayTimeout = 10 * time.Second

// defaultGatewayTimeout is kept as an internal alias so the existing call
// sites in this file continue to compile unchanged.
const defaultGatewayTimeout = DefaultGatewayTimeout

// GatewayTokenSourceConfig configures a gatewayTokenSource.
type GatewayTokenSourceConfig struct {
	// EndpointURL is the full URL of the gateway's whoami endpoint,
	// e.g. "http://127.0.0.1:8081/internal/v1/whoami". Must be loopback.
	EndpointURL string

	// Secret is the shared bearer secret (MCP_GATEWAY_INTERNAL_SECRET on
	// the gateway side). Required to be non-empty.
	Secret string

	// Subject identifies which cached token entry the gateway should
	// return. In this repo, Subject is the GitHub login (gateway's docs
	// explicitly document subject as the GitHub login).
	Subject string

	// HTTPClient is the underlying transport. Optional; if nil a fresh
	// client with defaultGatewayTimeout is used. The token source itself
	// must be constructed per-subject (each watch has its own subject),
	// but the underlying *http.Client / http.Transport are designed for
	// concurrent reuse and SHOULD be shared across token sources to
	// avoid leaking idle connections.
	HTTPClient *http.Client

	// Context is the long-lived parent context (e.g., the watch
	// goroutine's context). Token() derives a per-request context with
	// defaultGatewayTimeout from this parent, so cancelling the parent
	// (watch stop / server shutdown) also cancels any in-flight whoami
	// call. Optional; defaults to context.Background() which yields the
	// previous "timeout-only" behavior.
	Context context.Context
}

type whoamiResponse struct {
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
}

// gatewayTokenSource is an oauth2.TokenSource that resolves the current
// access token for a fixed subject via the gateway's internal delegated
// background access API (Phase B PoC).
//
// Each call to Token() issues a POST /internal/v1/whoami request and returns
// the fresh access_token along with its expires_at parsed into oauth2.Token.Expiry.
// Wrap this source in oauth2.ReuseTokenSource(nil, ts) so that whoami is only
// hit when the cached token is near expiry.
//
// Limitation: client and gateway must be co-located on the same host because
// the gateway's listener binds to 127.0.0.1 and rejects non-loopback peers.
// In particular, Docker Compose deployments that place client and gateway in
// separate containers cannot use this PoC as-is — see
// docs/spike-72-delegated-background-access.md "Trust boundary" / "Future work".
type gatewayTokenSource struct {
	endpoint string
	secret   string
	subject  string
	httpc    *http.Client
	parent   context.Context
}

// ValidateGatewayEndpoint performs subject-independent validation of the
// gateway endpoint URL and shared secret. It is intended for startup-time
// fail-fast checks (e.g., in cmd/server/main.go) where the GitHub login
// (Subject) is not yet known but the deployment-level configuration must be
// rejected if it is malformed.
//
// Returns the same error sentinels as NewGatewayTokenSource for URL/scheme/
// loopback violations (ErrGatewayNonLoopback for non-loopback hosts).
func ValidateGatewayEndpoint(endpointURL, secret string) error {
	if strings.TrimSpace(endpointURL) == "" {
		return errors.New("gateway: endpoint URL is required")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("gateway: shared secret is required")
	}
	u, err := url.Parse(endpointURL)
	if err != nil {
		return fmt.Errorf("gateway: invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("gateway: endpoint URL scheme must be http or https, got %q", u.Scheme)
	}
	if !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("%w: got host %q", ErrGatewayNonLoopback, u.Hostname())
	}
	if !strings.HasSuffix(u.Path, "/whoami") {
		return fmt.Errorf("%w: got path %q", ErrGatewayInvalidPath, u.Path)
	}
	return nil
}

// NewGatewayTokenSource constructs a gatewayTokenSource after validating the
// loopback constraint and required fields. Returns an error rather than a
// silent no-op so misconfigurations surface at startup.
func NewGatewayTokenSource(cfg GatewayTokenSourceConfig) (oauth2.TokenSource, error) {
	if strings.TrimSpace(cfg.EndpointURL) == "" {
		return nil, errors.New("gateway: endpoint URL is required")
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, errors.New("gateway: shared secret is required")
	}
	if strings.TrimSpace(cfg.Subject) == "" {
		return nil, errors.New("gateway: subject is required")
	}
	u, err := url.Parse(cfg.EndpointURL)
	if err != nil {
		return nil, fmt.Errorf("gateway: invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("gateway: endpoint URL scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("%w: got host %q", ErrGatewayNonLoopback, host)
	}
	if !strings.HasSuffix(u.Path, "/whoami") {
		return nil, fmt.Errorf("%w: got path %q", ErrGatewayInvalidPath, u.Path)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultGatewayTimeout}
	}
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	return &gatewayTokenSource{
		endpoint: cfg.EndpointURL,
		secret:   cfg.Secret,
		subject:  cfg.Subject,
		httpc:    hc,
		parent:   parent,
	}, nil
}

// isLoopbackHost reports whether host refers to the local machine. Accepts
// the literal hostnames "localhost", "127.0.0.1", "::1" (and bracketed form
// from URLs already stripped by url.Hostname), as well as any IP that
// net.ParseIP confirms as loopback.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Token fetches the current access token from the gateway whoami endpoint.
// Implements oauth2.TokenSource.
//
// The request context is derived from the configured parent context with
// defaultGatewayTimeout, so cancellation of the watch (or server shutdown)
// propagates into the in-flight whoami call.
func (g *gatewayTokenSource) Token() (*oauth2.Token, error) {
	body, err := json.Marshal(map[string]string{"subject": g.subject})
	if err != nil {
		return nil, fmt.Errorf("gateway: marshal whoami body: %w", err)
	}
	ctx, cancel := context.WithTimeout(g.parent, defaultGatewayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gateway: build whoami request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway: whoami request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded portion of the body so net/http can reuse the
		// underlying connection on keep-alive. Errors here are ignored;
		// connection reuse is best-effort, and we still want to surface
		// the original status-derived error to the caller below.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// fallthrough below
	case http.StatusUnauthorized:
		return nil, ErrGatewayUnauthorized
	case http.StatusForbidden:
		return nil, ErrGatewayLoopbackRequired
	case http.StatusNotFound:
		return nil, ErrGatewaySubjectGone
	case http.StatusBadGateway:
		return nil, ErrGatewayUpstreamFailure
	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, fmt.Errorf("%w: status=%d", ErrGatewayBadRequest, resp.StatusCode)
		}
		return nil, fmt.Errorf("gateway: unexpected status %d", resp.StatusCode)
	}

	// Cap response body to a defensive size; whoami responses are tiny.
	rbody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("gateway: read whoami body: %w", err)
	}
	var r whoamiResponse
	if err := json.Unmarshal(rbody, &r); err != nil {
		return nil, fmt.Errorf("gateway: decode whoami body: %w", err)
	}
	if r.AccessToken == "" {
		return nil, errors.New("gateway: whoami response missing access_token")
	}
	tok := &oauth2.Token{
		AccessToken: r.AccessToken,
		TokenType:   r.TokenType,
	}
	if r.ExpiresAt != "" {
		// Parse RFC3339; on parse failure leave Expiry zero (treated as
		// "no expiry known"), which causes ReuseTokenSource to call Token
		// only on first use and on explicit invalidation. Logging is left
		// to the caller because this package must remain log-agnostic.
		if t, perr := time.Parse(time.RFC3339, r.ExpiresAt); perr == nil {
			tok.Expiry = t
		}
	}
	return tok, nil
}
