package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"log/slog"
)

// githubClient is a shared HTTP client with timeouts to avoid hanging requests.
var githubClient = &http.Client{Timeout: 15 * time.Second}

// Config holds OAuth façade configuration.
type Config struct {
	GitHubClientID       string
	GitHubClientSecret   string
	BaseURL              string // e.g. http://localhost:8083
	Scopes               string // e.g. "repo,user"
	SessionTTL           time.Duration
	CacheTTL             time.Duration
	AllowedRedirectHosts []string // allowlist of permitted redirect_uri hostnames; defaults to ["localhost","127.0.0.1","vscode.dev"]
	// ExpiresIn is the lifetime advertised to MCP clients via the token response
	// expires_in field (RFC 6749 §5.1). GitHub classic OAuth tokens do not expire
	// on GitHub's side, so this value only controls how long the MCP client trusts
	// its cached copy before re-authenticating. Defaults to 7776000 s (90 days).
	ExpiresIn time.Duration
}

// UpstreamError represents a failure contacting an upstream service (e.g. GitHub API
// network error or 5xx response). The auth middleware uses this to return 503 instead of 401.
type UpstreamError struct {
	err error
}

func (e *UpstreamError) Error() string         { return e.err.Error() }
func (e *UpstreamError) Unwrap() error         { return e.err }
func (e *UpstreamError) IsUpstreamError() bool { return true }

// Handler implements the OAuth façade endpoints.
type Handler struct {
	cfg   Config
	store *Store
}

func NewHandler(cfg Config) *Handler {
	// Normalize BaseURL: strip trailing slash to prevent double-slash in endpoint URLs.
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if len(cfg.AllowedRedirectHosts) == 0 {
		cfg.AllowedRedirectHosts = []string{"localhost", "127.0.0.1", "vscode.dev"}
	}
	if cfg.ExpiresIn <= 0 {
		cfg.ExpiresIn = 90 * 24 * time.Hour // 90 days default
	}
	return &Handler{
		cfg:   cfg,
		store: NewStore(cfg.SessionTTL, cfg.CacheTTL),
	}
}

// Discovery returns RFC 8414 metadata.
func (h *Handler) Discovery(w http.ResponseWriter, r *http.Request) {
	doc := map[string]any{
		"issuer":                           h.cfg.BaseURL,
		"authorization_endpoint":           h.cfg.BaseURL + "/authorize",
		"token_endpoint":                   h.cfg.BaseURL + "/token",
		"registration_endpoint":            h.cfg.BaseURL + "/register",
		"response_types_supported":         []string{"code"},
		"grant_types_supported":            []string{"authorization_code"},
		"code_challenge_methods_supported": []string{"S256"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// Register implements RFC 7591 Dynamic Client Registration (pseudo).
// Always returns the pre-configured GitHub OAuth App client_id; no new client
// is created. This allows MCP clients (e.g. VS Code) to proceed automatically
// without requiring the user to enter a client_id manually.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	// Limit body to 64 KB to prevent memory exhaustion from oversized requests.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

	meta := map[string]json.RawMessage{}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&meta); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_client_metadata",
			"error_description": "request body must be valid JSON client metadata",
		})
		return
	}
	// Reject payloads that contain more than a single JSON object (RFC 7591).
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_client_metadata",
			"error_description": "request body must contain a single JSON object",
		})
		return
	}

	resp := map[string]any{
		"client_id":                  h.cfg.GitHubClientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_secret_expires_at":   0,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	}
	// Echo back optional metadata fields if provided by the client.
	for _, field := range []string{"redirect_uris", "client_name", "scope"} {
		if v, ok := meta[field]; ok {
			resp[field] = v
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// Authorize redirects the MCP client to GitHub OAuth.
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	codeChallenge := q.Get("code_challenge")
	responseType := q.Get("response_type")
	codeChallengeMethod := q.Get("code_challenge_method")

	// Validate required OAuth 2.0 parameters.
	if responseType != "code" {
		oauthError(w, "unsupported_response_type", "response_type must be 'code'", http.StatusBadRequest)
		return
	}
	if state == "" || redirectURI == "" {
		oauthError(w, "invalid_request", "missing state or redirect_uri", http.StatusBadRequest)
		return
	}
	// If code_challenge is provided, enforce S256 method.
	if codeChallenge != "" && codeChallengeMethod != "S256" {
		oauthError(w, "invalid_request", "code_challenge_method must be S256", http.StatusBadRequest)
		return
	}

	// Validate redirect_uri: must be an absolute URL with http/https scheme,
	// non-empty host, no fragment (RFC 6749 §3.1.2), and host in the allowlist.
	parsedRedirect, err := url.Parse(redirectURI)
	if err != nil ||
		(parsedRedirect.Scheme != "http" && parsedRedirect.Scheme != "https") ||
		parsedRedirect.Host == "" ||
		parsedRedirect.Fragment != "" {
		oauthError(w, "invalid_request", "invalid redirect_uri: must be absolute http/https URL without fragment", http.StatusBadRequest)
		return
	}
	if !isAllowedRedirectHost(parsedRedirect.Hostname(), h.cfg.AllowedRedirectHosts) {
		oauthError(w, "invalid_request", "redirect_uri host not permitted", http.StatusBadRequest)
		return
	}

	h.store.SaveSession(state, redirectURI, codeChallenge)

	ghURL, _ := url.Parse("https://github.com/login/oauth/authorize")
	ghq := ghURL.Query()
	ghq.Set("client_id", h.cfg.GitHubClientID)
	ghq.Set("redirect_uri", h.cfg.BaseURL+"/callback")
	ghq.Set("state", state)
	ghq.Set("scope", h.cfg.Scopes)
	ghURL.RawQuery = ghq.Encode()

	http.Redirect(w, r, ghURL.String(), http.StatusFound)
}

// Callback receives GitHub's authorization code and exchanges it for an access token.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")

	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	// Verify state maps to a live session BEFORE calling GitHub to avoid unnecessary outbound calls.
	if !h.store.HasSession(state) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	accessToken, grantedScope, err := h.exchangeGitHubCode(r.Context(), code)
	if err != nil {
		slog.Error("GitHub token exchange failed", "err", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	internalCode, err := h.store.CompleteCallback(state, accessToken, grantedScope)
	if err != nil {
		slog.Error("session completion failed", "err", err)
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// Retrieve redirect_uri from session to redirect back to MCP client.
	// We need to look it up before ExchangeCode consumes it.
	// Re-read via a separate lookup (session still exists at this point).
	sess := h.store.lookupByCode(internalCode)
	if sess == nil {
		http.Error(w, "session lost", http.StatusInternalServerError)
		return
	}

	redirect, _ := url.Parse(sess.RedirectURI)
	rq := redirect.Query()
	rq.Set("code", internalCode)
	rq.Set("state", state)
	redirect.RawQuery = rq.Encode()

	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// Token handles the authorization_code grant and returns the access token.
// Note on client_id: this façade authenticates exclusively via GitHub OAuth App (single-client).
// The client_id presented by MCP clients is the GitHub OAuth App Client ID, which is
// validated by GitHub during the code-exchange step. No separate façade-level client
// registry is needed for phase 1. Multi-client support can be added in a future iteration.
func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "malformed request body", http.StatusBadRequest)
		return
	}
	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" {
		oauthError(w, "unsupported_grant_type", "only authorization_code is supported", http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	codeVerifier := r.FormValue("code_verifier")

	token, grantedScope, err := h.store.ExchangeCode(code, redirectURI, codeVerifier)
	if err != nil {
		slog.Warn("token exchange rejected", "err", err)
		oauthError(w, "invalid_grant", err.Error(), http.StatusBadRequest)
		return
	}

	// Use integer division to avoid float64 precision issues from Duration.Seconds().
	// Clamp to at least 1 so expires_in is always a positive integer per RFC 6749 §5.1.
	expiresInSeconds := int64(h.cfg.ExpiresIn / time.Second)
	if expiresInSeconds < 1 {
		expiresInSeconds = 1
	}
	tokenResp := map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		// RFC 6749 §5.1: expires_in tells MCP clients how long to trust the cached token.
		// GitHub classic OAuth tokens do not expire server-side; this value controls
		// client-side re-authentication frequency only.
		"expires_in": expiresInSeconds,
	}
	// RFC 6749 §5.1: include the scope actually granted by GitHub rather than the requested scope.
	if grantedScope != "" {
		tokenResp["scope"] = grantedScope
	}
	w.Header().Set("Content-Type", "application/json")
	// RFC 6749 §5.1: token responses MUST NOT be cached.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(tokenResp)
}

// InvalidateCachedToken removes a token from the validation cache immediately.
// Call when a downstream GitHub API call returns HTTP 401 so the next request
// forces a fresh GitHub API validation instead of using a stale cached entry.
func (h *Handler) InvalidateCachedToken(token string) {
	h.store.InvalidateCachedToken(token)
}

// ValidateToken checks the bearer token against GitHub API (with cache).
func (h *Handler) ValidateToken(ctx context.Context, token string) (string, error) {
	if login, ok := h.store.LookupToken(token); ok {
		return login, nil
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubClient.Do(req)
	if err != nil {
		return "", &UpstreamError{err: fmt.Errorf("GitHub API unreachable: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return "", &UpstreamError{err: fmt.Errorf("GitHub API returned %d", resp.StatusCode)}
		}
		return "", fmt.Errorf("invalid token: GitHub returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &UpstreamError{err: fmt.Errorf("reading GitHub user response: %w", err)}
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("unexpected GitHub user response")
	}

	h.store.CacheToken(token, user.Login)
	return user.Login, nil
}

// exchangeGitHubCode exchanges GitHub's authorization code for an access token and scope.
func (h *Handler) exchangeGitHubCode(ctx context.Context, code string) (string, string, error) {
	form := url.Values{
		"client_id":     {h.cfg.GitHubClientID},
		"client_secret": {h.cfg.GitHubClientSecret},
		"code":          {code},
		"redirect_uri":  {h.cfg.BaseURL + "/callback"},
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := githubClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", "", fmt.Errorf("GitHub OAuth returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var result struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decoding GitHub OAuth response: %w", err)
	}
	if result.Error != "" {
		return "", "", fmt.Errorf("GitHub OAuth error: %s", result.Error)
	}
	if result.AccessToken == "" {
		return "", "", fmt.Errorf("empty access_token from GitHub")
	}
	return result.AccessToken, result.Scope, nil
}

// lookupByCode is a helper for Callback to read redirect_uri without consuming the code.
func (s *Store) lookupByCode(code string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.codes[code]
}

// isAllowedRedirectHost returns true when hostname is in the configured allowlist.
func isAllowedRedirectHost(hostname string, allowed []string) bool {
	for _, h := range allowed {
		if h == hostname {
			return true
		}
	}
	return false
}

// oauthError writes an RFC 6749-compliant JSON error response.
func oauthError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}
