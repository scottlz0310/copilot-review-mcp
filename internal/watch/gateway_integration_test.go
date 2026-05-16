package watch

// Phase B PR-C (Issue #29 / #40): end-to-end integration tests that exercise
// the real gateway-backed token source through the full watch loop.
//
// Distinct from manager_test.go, which feeds gateway sentinel errors into the
// manager via a fakeFetcher and therefore never runs the wiring:
//
//     gatewayTokenSource → oauth2.ReuseTokenSource → oauth2.Transport →
//     *ghclient.Client → watch.Manager.pollOnce
//
// Here we stand up a fake gateway whoami endpoint and a minimal fake GitHub
// REST surface so the whole chain executes for real. The watch terminal state
// (and the recovery_hint on FAILED/AUTH_EXPIRED) is the assertion target.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v85/github"
	"golang.org/x/oauth2"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
)

const (
	integTestGatewaySecret = "integration-shared-secret"
	integTestLogin         = "alice"
	integTestOwner         = "octo"
	integTestRepo          = "demo"
	integTestPR            = 42
)

// whoamiScript describes a single response the fake gateway will serve on a
// /internal/v1/whoami request. After exhausting the slice the last entry is
// replayed, mirroring the fakeFetcher convention used elsewhere in this
// package so error-only and success-only scenarios stay compact.
type whoamiScript struct {
	status int
	// token / expiresAt are used when status == 200. expiresAt is relative to
	// time.Now() at the moment of the request; a zero value leaves the token
	// without an expiry, which gateway_token_source.go rejects with
	// ErrGatewayInvalidExpiry.
	token        string
	expiresIn    time.Duration
	errorBodyTag string // 502 body: "rotation_failed" or "upstream_failure"
}

// fakeGateway is a controllable POST /internal/v1/whoami server.
type fakeGateway struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	scripts []whoamiScript
	calls   int32
}

func newFakeGateway(t *testing.T, scripts []whoamiScript) *fakeGateway {
	t.Helper()
	g := &fakeGateway{t: t, scripts: scripts}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) endpoint() string { return g.srv.URL + "/internal/v1/whoami" }

func (g *fakeGateway) callCount() int { return int(atomic.LoadInt32(&g.calls)) }

func (g *fakeGateway) serve(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&g.calls, 1)

	if r.Method != http.MethodPost {
		g.t.Errorf("fake gateway: method=%q, want POST", r.Method)
	}
	if r.URL.Path != "/internal/v1/whoami" {
		g.t.Errorf("fake gateway: path=%q, want /internal/v1/whoami", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+integTestGatewaySecret {
		g.t.Errorf("fake gateway: Authorization=%q", got)
	}
	var body struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		g.t.Errorf("fake gateway: decode subject: %v", err)
	}
	if body.Subject != integTestLogin {
		g.t.Errorf("fake gateway: subject=%q, want %q", body.Subject, integTestLogin)
	}

	g.mu.Lock()
	idx := int(atomic.LoadInt32(&g.calls)) - 1
	if idx >= len(g.scripts) {
		idx = len(g.scripts) - 1
	}
	script := g.scripts[idx]
	g.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch script.status {
	case http.StatusOK:
		expiry := time.Now().Add(script.expiresIn).UTC()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": script.token,
			"token_type":   "bearer",
			"expires_at":   expiry.Format(time.RFC3339),
			"scopes":       []string{"repo"},
		})
	case http.StatusBadGateway:
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": script.errorBodyTag})
	default:
		w.WriteHeader(script.status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "x"})
	}
}

// githubReviewState configures the snapshot the fake GitHub will return.
type githubReviewState struct {
	// approvedAt: when set, fake GitHub returns a single Copilot APPROVED
	// review with this submitted_at. When zero, the reviews list is empty.
	approvedAt time.Time
}

// fakeGitHub is a tiny REST stand-in covering only the three endpoints
// GetReviewData consults: requested_reviewers, reviews, issues/timeline. It
// records every Authorization header it observes so rotation tests can verify
// the watch loop picked up a new token mid-flight.
type fakeGitHub struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	state   githubReviewState
	tokens  []string
	pathHit map[string]int
}

func newFakeGitHub(t *testing.T, state githubReviewState) *fakeGitHub {
	t.Helper()
	gh := &fakeGitHub{t: t, state: state, pathHit: map[string]int{}}
	gh.srv = httptest.NewServer(http.HandlerFunc(gh.serve))
	t.Cleanup(gh.srv.Close)
	return gh
}

func (gh *fakeGitHub) observedTokens() []string {
	gh.mu.Lock()
	defer gh.mu.Unlock()
	out := make([]string, len(gh.tokens))
	copy(out, gh.tokens)
	return out
}

func (gh *fakeGitHub) baseURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(gh.srv.URL + "/")
	if err != nil {
		t.Fatalf("parse fake GitHub URL: %v", err)
	}
	return u
}

func (gh *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	gh.mu.Lock()
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		gh.tokens = append(gh.tokens, strings.TrimPrefix(auth, "Bearer "))
	}
	gh.pathHit[r.URL.Path]++
	state := gh.state
	gh.mu.Unlock()

	prPrefix := fmt.Sprintf("/repos/%s/%s/pulls/%d/", integTestOwner, integTestRepo, integTestPR)
	timelinePath := fmt.Sprintf("/repos/%s/%s/issues/%d/timeline", integTestOwner, integTestRepo, integTestPR)

	w.Header().Set("Content-Type", "application/json")
	// Without rate-limit headers go-github reports Remaining=0, which the
	// watch loop interprets as "low budget" and terminates with RATE_LIMITED
	// — that would mask every assertion downstream of GetReviewData. Set a
	// healthy budget so the watch can poll freely.
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))
	switch {
	case strings.HasPrefix(r.URL.Path, prPrefix+"requested_reviewers"):
		_, _ = io.WriteString(w, `{"users":[],"teams":[]}`)
	case strings.HasPrefix(r.URL.Path, prPrefix+"reviews"):
		if state.approvedAt.IsZero() {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		// Use the "Copilot" capital-C identity that matches copilotLogins.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":           int64(101),
				"user":         map[string]string{"login": "Copilot"},
				"state":        "APPROVED",
				"submitted_at": state.approvedAt.UTC().Format(time.RFC3339),
			},
		})
	case strings.HasPrefix(r.URL.Path, timelinePath):
		_, _ = io.WriteString(w, `[]`)
	default:
		http.Error(w, "unknown path: "+r.URL.Path, http.StatusNotFound)
	}
}

// integFactoryArgs is what gatewayClientFactoryFor needs to wire the real
// gatewayTokenSource into the manager's ClientFactory. Keeping the argument
// list named avoids the positional-blindness of long factory signatures.
type integFactoryArgs struct {
	gateway   *fakeGateway
	github    *fakeGitHub
	threshold time.Duration
}

// gatewayClientFactoryFor returns a watch.Options.ClientFactory that mirrors
// cmd/server/main.go's buildGatewayClientFactory: it stands up the real
// gatewayTokenSource (with loopback-validated endpoint), wraps it in
// oauth2.ReuseTokenSource, and routes the resulting *http.Client at the fake
// GitHub server by overriding go-github's BaseURL.
//
// The factory deliberately matches production wiring so that this test fails
// closed if anyone refactors buildGatewayClientFactory in a way that breaks
// the chain.
func gatewayClientFactoryFor(t *testing.T, args integFactoryArgs) func(ctx context.Context, _, login string) ReviewDataFetcher {
	t.Helper()
	sharedHTTP := &http.Client{Timeout: ghclient.DefaultGatewayTimeout}
	return func(ctx context.Context, _, login string) ReviewDataFetcher {
		ts, err := ghclient.NewGatewayTokenSource(ghclient.GatewayTokenSourceConfig{
			EndpointURL: args.gateway.endpoint(),
			Secret:      integTestGatewaySecret,
			Subject:     login,
			HTTPClient:  sharedHTTP,
			Context:     ctx,
		})
		if err != nil {
			t.Fatalf("NewGatewayTokenSource: %v", err)
		}
		reuse := oauth2.ReuseTokenSource(nil, ts)
		httpClient := oauth2.NewClient(ctx, reuse)
		gh := github.NewClient(httpClient)
		gh.BaseURL = args.github.baseURL(t)
		// v4 (GraphQL) is unused by GetReviewData; passing nil is safe and
		// asserts that nothing on the watch path inadvertently reaches for it.
		return ghclient.NewWithClients(gh, nil, args.threshold)
	}
}

// waitFailed waits until the watch reaches FAILED. Reuses the existing
// waitForWatch helper from manager_test.go so the polling cadence and timeout
// stay consistent across the test suite.
func waitFailed(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	return waitForWatch(t, m, id, func(s Snapshot) bool {
		return s.WatchStatus == StatusFailed
	})
}

func waitCompleted(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	return waitForWatch(t, m, id, func(s Snapshot) bool {
		return s.WatchStatus == StatusCompleted
	})
}

func startIntegrationWatch(t *testing.T, m *Manager) string {
	t.Helper()
	snap, _, err := m.Start(StartInput{
		Login: integTestLogin,
		Token: "ignored-by-gateway-factory",
		Owner: integTestOwner,
		Repo:  integTestRepo,
		PR:    integTestPR,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return snap.WatchID
}

// TestGatewayIntegration_HappyPath: gateway returns a valid token; fake GitHub
// reports a Copilot APPROVED review submitted now → watch reaches COMPLETED.
//
// Key assertion beyond the terminal state: the GitHub request actually carried
// the token minted by the gateway, proving the whole oauth2.Transport chain
// executed (and was not short-circuited by some unintended fallback).
func TestGatewayIntegration_HappyPath(t *testing.T) {
	gw := newFakeGateway(t, []whoamiScript{
		{status: http.StatusOK, token: "gho_happy", expiresIn: time.Hour},
	})
	gh := newFakeGitHub(t, githubReviewState{approvedAt: time.Now().Add(-time.Second)})

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:  10 * time.Millisecond,
		Threshold:     30 * time.Second,
		ClientFactory: gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	id := startIntegrationWatch(t, m)
	snap := waitCompleted(t, m, id)

	if snap.WatchStatus != StatusCompleted {
		t.Fatalf("WatchStatus=%q, want %q", snap.WatchStatus, StatusCompleted)
	}
	tokens := gh.observedTokens()
	if len(tokens) == 0 {
		t.Fatalf("fake GitHub observed no Authorization headers; gateway chain may not have been used")
	}
	if tokens[0] != "gho_happy" {
		t.Errorf("first observed token=%q, want gho_happy", tokens[0])
	}
}

// TestGatewayIntegration_SubjectGoneFailsAuthExpired: gateway returns 404,
// which the chain surfaces as ErrGatewaySubjectGone — the watch must fail with
// FailureReasonAuthExpired and carry the subject-gone recovery hint.
//
// The fake GitHub should never be reached because oauth2.Transport short-
// circuits on Token() error; we assert that too as a regression guard.
func TestGatewayIntegration_SubjectGoneFailsAuthExpired(t *testing.T) {
	gw := newFakeGateway(t, []whoamiScript{{status: http.StatusNotFound}})
	gh := newFakeGitHub(t, githubReviewState{})

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:  10 * time.Millisecond,
		Threshold:     30 * time.Second,
		ClientFactory: gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	id := startIntegrationWatch(t, m)
	snap := waitFailed(t, m, id)

	if snap.FailureReason == nil || *snap.FailureReason != FailureReasonAuthExpired {
		t.Fatalf("FailureReason=%v, want %v", snap.FailureReason, FailureReasonAuthExpired)
	}
	if snap.RecoveryHint == nil || !strings.Contains(*snap.RecoveryHint, "re-seed the gateway cache") {
		t.Fatalf("RecoveryHint=%v, want subject-gone hint", snap.RecoveryHint)
	}
	if len(gh.observedTokens()) != 0 {
		t.Errorf("fake GitHub observed %d requests; gateway 404 should short-circuit before GitHub", len(gh.observedTokens()))
	}
}

// TestGatewayIntegration_RotationFailedFailsAuthExpired: gateway returns 502
// with body {"error":"rotation_failed"} → ErrGatewayRotationFailed →
// FAILED/AUTH_EXPIRED with the rotation-failed recovery hint.
func TestGatewayIntegration_RotationFailedFailsAuthExpired(t *testing.T) {
	gw := newFakeGateway(t, []whoamiScript{{status: http.StatusBadGateway, errorBodyTag: "rotation_failed"}})
	gh := newFakeGitHub(t, githubReviewState{})

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:  10 * time.Millisecond,
		Threshold:     30 * time.Second,
		ClientFactory: gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	id := startIntegrationWatch(t, m)
	snap := waitFailed(t, m, id)

	if snap.FailureReason == nil || *snap.FailureReason != FailureReasonAuthExpired {
		t.Fatalf("FailureReason=%v, want %v", snap.FailureReason, FailureReasonAuthExpired)
	}
	if snap.RecoveryHint == nil || !strings.Contains(*snap.RecoveryHint, "refresh token was rejected") {
		t.Fatalf("RecoveryHint=%v, want rotation-failed hint", snap.RecoveryHint)
	}
}

// TestGatewayIntegration_UpstreamFailureBelowThresholdKeepsWatching: a single
// 502/upstream_failure must keep the watch in WATCHING with last_error set, not
// escalate to AUTH_EXPIRED until the configured threshold is reached.
//
// We script 1 failure then valid tokens so subsequent polls (which will not
// terminate by themselves because GitHub returns no review) confirm the watch
// is still alive and re-fetching from the gateway.
func TestGatewayIntegration_UpstreamFailureBelowThresholdKeepsWatching(t *testing.T) {
	gw := newFakeGateway(t, []whoamiScript{
		{status: http.StatusBadGateway, errorBodyTag: "upstream_failure"},
		{status: http.StatusOK, token: "gho_recovered", expiresIn: time.Hour},
	})
	gh := newFakeGitHub(t, githubReviewState{}) // no review → keeps WATCHING

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:             10 * time.Millisecond,
		Threshold:                30 * time.Second,
		UpstreamFailureThreshold: 5,
		ClientFactory:            gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	id := startIntegrationWatch(t, m)

	// Wait until the GitHub call has happened at least once (i.e. the watch
	// recovered past the initial upstream_failure). That implies the watch
	// is still in WATCHING and the gateway returned the second scripted
	// token successfully.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(gh.observedTokens()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(gh.observedTokens()) == 0 {
		t.Fatalf("expected GitHub call after recovery; gateway calls=%d", gw.callCount())
	}
	snap, ok := m.GetByID(id)
	if !ok {
		t.Fatalf("watch %q not found", id)
	}
	if snap.WatchStatus != StatusWatching {
		t.Fatalf("WatchStatus=%q, want %q", snap.WatchStatus, StatusWatching)
	}
	if snap.FailureReason != nil {
		t.Errorf("FailureReason=%v, want nil while below threshold", snap.FailureReason)
	}
}

// TestGatewayIntegration_UpstreamFailureEscalatesAfterThreshold: N consecutive
// 502/upstream_failure responses must escalate to FAILED/AUTH_EXPIRED with the
// "consecutive polls" recovery hint produced by handleUpstreamFailure.
func TestGatewayIntegration_UpstreamFailureEscalatesAfterThreshold(t *testing.T) {
	const threshold = 3
	scripts := make([]whoamiScript, 0, threshold)
	for range threshold {
		scripts = append(scripts, whoamiScript{status: http.StatusBadGateway, errorBodyTag: "upstream_failure"})
	}
	gw := newFakeGateway(t, scripts)
	gh := newFakeGitHub(t, githubReviewState{})

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:             5 * time.Millisecond,
		Threshold:                30 * time.Second,
		UpstreamFailureThreshold: threshold,
		ClientFactory:            gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	id := startIntegrationWatch(t, m)
	snap := waitFailed(t, m, id)

	if snap.FailureReason == nil || *snap.FailureReason != FailureReasonAuthExpired {
		t.Fatalf("FailureReason=%v, want %v", snap.FailureReason, FailureReasonAuthExpired)
	}
	if snap.RecoveryHint == nil || !strings.Contains(*snap.RecoveryHint, "consecutive polls") {
		t.Fatalf("RecoveryHint=%v, want consecutive-polls hint", snap.RecoveryHint)
	}
	if gw.callCount() < threshold {
		t.Errorf("gateway calls=%d, want >=%d before escalation", gw.callCount(), threshold)
	}
}

// TestGatewayIntegration_TokenRotationVisibleToGitHub: the first whoami call
// returns a token with an already-elapsed expiry (Issued in the past), forcing
// oauth2.ReuseTokenSource to call the gateway again on the very next poll and
// pick up the rotated value. We assert two distinct tokens are observed on the
// GitHub side.
//
// Using an already-expired expiry rather than a tight real-time deadline keeps
// this test deterministic on slow CI runners — ReuseTokenSource's "valid"
// check is `expiry.After(time.Now())`, so a past-or-equal expiry triggers an
// immediate refetch without depending on wall-clock pacing.
func TestGatewayIntegration_TokenRotationVisibleToGitHub(t *testing.T) {
	gw := newFakeGateway(t, []whoamiScript{
		// Already-expired (negative expiresIn) → ReuseTokenSource will call
		// the gateway again on the next poll.
		{status: http.StatusOK, token: "gho_v1", expiresIn: -time.Minute},
		// Subsequent calls return the rotated value with a healthy expiry.
		{status: http.StatusOK, token: "gho_v2", expiresIn: time.Hour},
	})
	gh := newFakeGitHub(t, githubReviewState{}) // keeps WATCHING so we get multiple polls

	db := openTestDB(t)
	m := NewManager(db, Options{
		PollInterval:  5 * time.Millisecond,
		Threshold:     30 * time.Second,
		ClientFactory: gatewayClientFactoryFor(t, integFactoryArgs{gateway: gw, github: gh, threshold: 30 * time.Second}),
	})
	t.Cleanup(m.Close)

	_ = startIntegrationWatch(t, m)

	// Wait until both rotated values have been observed by GitHub.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tokens := gh.observedTokens()
		if containsBoth(tokens, "gho_v1", "gho_v2") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("rotation not observed: tokens=%v, gateway calls=%d", gh.observedTokens(), gw.callCount())
}

func containsBoth(tokens []string, a, b string) bool {
	var sawA, sawB bool
	for _, tok := range tokens {
		if tok == a {
			sawA = true
		}
		if tok == b {
			sawB = true
		}
	}
	return sawA && sawB
}
