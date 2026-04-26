package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v72/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shurcooL/githubv4"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

// newGitHubAPIMock builds a minimal httptest.Server that serves the three GitHub
// API calls issued by requestHandler:
//  1. GET …/requested_reviewers  → empty list (Copilot not in reviewers)
//  2. GET …/reviews              → single Copilot review with the given submittedAt
//  3. POST / (GraphQL)           → PR node ID query + requestReviewsByLogin mutation
func newGitHubAPIMock(t *testing.T, submittedAt time.Time) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "1000")
		switch {
		case strings.Contains(r.URL.Path, "/requested_reviewers") && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"users":[],"teams":[]}`)
		case strings.Contains(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
			fmt.Fprintf(w, `[{"id":1,"user":{"login":"copilot-pull-request-reviewer[bot]"},"state":"APPROVED","submitted_at":%q}]`,
				submittedAt.UTC().Format(time.RFC3339))
		default:
			// GraphQL: distinguish node-ID query from requestReviewsByLogin mutation by
			// looking for the mutation field name in the query body. The node-ID query
			// contains "repository" in the query string but NOT "requestReviewsByLogin".
			// The mutation body always contains "requestReviewsByLogin" as the operation.
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "requestReviewsByLogin") {
				fmt.Fprint(w, `{"data":{"requestReviewsByLogin":{"clientMutationId":""}}}`)
			} else {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_test123"}}}}`)
			}
		}
	}))
}

// makeGitHubClient creates a *ghclient.Client that routes all API calls to srv.
func makeGitHubClient(srv *httptest.Server) *ghclient.Client {
	restClient := github.NewClient(srv.Client())
	base, _ := url.Parse(srv.URL + "/")
	restClient.BaseURL = base
	restClient.UploadURL = base
	gqlClient := githubv4.NewEnterpriseClient(srv.URL, srv.Client())
	return ghclient.NewWithClients(restClient, gqlClient, 30*time.Second)
}

// staticProvider returns a githubClientProvider that always returns the given client.
func staticProvider(c *ghclient.Client) githubClientProvider {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*ghclient.Client, error) {
		return c, nil
	}
}

// TestRequestHandlerUsesInsertWithTimeWhenReviewPostdatesAllEntries verifies Bug B
// fix: when LatestCopilotReview.SubmittedAt post-dates every existing trigger_log entry,
// requestHandler calls InsertWithPrevReviewID with (SubmittedAt+1s, prevID) so that:
//   - the existing review is stale (sat < sat+1s = requestedAt → stale-guard rejects it as irrelevant)
//   - any new review Copilot posts (different ID, or SubmittedAt ≥ sat+1s) passes the check → COMPLETED
func TestRequestHandlerUsesInsertWithTimeWhenReviewPostdatesAllEntries(t *testing.T) {
	submittedAt := time.Now().UTC().Add(-5 * time.Second).Truncate(time.Second)
	srv := newGitHubAPIMock(t, submittedAt)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "req-insert-with-time.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := requestHandler(staticProvider(makeGitHubClient(srv)), db)
	_, out, err := handler(context.Background(), nil, RequestInput{Owner: "octo", Repo: "demo", PR: 1})
	if err != nil {
		t.Fatalf("requestHandler() error = %v", err)
	}
	if !out.OK {
		t.Fatalf("requestHandler() OK = false, reason = %q", out.Reason)
	}

	entry, err := db.GetLatest("octo", "demo", 1)
	if err != nil {
		t.Fatalf("GetLatest() error = %v", err)
	}
	if entry == nil {
		t.Fatal("GetLatest() = nil, want trigger_log entry")
	}

	// InsertWithPrevReviewID is called with sat+1s; epoch-second storage truncates sub-seconds.
	// submittedAt is already truncated so want = submittedAt + 1s exactly.
	want := submittedAt.Add(time.Second)
	if !entry.RequestedAt.Equal(want) {
		t.Fatalf("RequestedAt = %v, want %v (SubmittedAt+1s via InsertWithPrevReviewID)", entry.RequestedAt, want)
	}
	// Stale-guard must reject the existing review: submittedAt < requestedAt (old review is stale).
	if !submittedAt.Before(entry.RequestedAt) {
		t.Fatalf("existing review is NOT stale: SubmittedAt(%v) >= RequestedAt(%v), old review would be immediately relevant (COMPLETED)", submittedAt, entry.RequestedAt)
	}
	// PrevReviewID must be recorded for ID-based staleness detection.
	if entry.PrevReviewID == nil {
		t.Fatal("PrevReviewID = nil, want non-nil (ID-based staleness detection requires prev_review_id)")
	}
}

// TestRequestHandlerFallsBackToNormalInsertWhenReviewPredatesExistingEntry verifies
// that when the Copilot review is older than a prior completed trigger_log entry,
// requestHandler uses the normal Insert (requested_at = now()) so that the new row
// remains the most-recent entry returned by GetLatest().
func TestRequestHandlerFallsBackToNormalInsertWhenReviewPredatesExistingEntry(t *testing.T) {
	// The GitHub review is older than the pre-existing DB entry.
	priorEntryTime := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Second)
	olderReviewTime := priorEntryTime.Add(-time.Minute) // review happened before the prior entry

	srv := newGitHubAPIMock(t, olderReviewTime)
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "req-fallback-insert.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-insert a completed entry that is newer than the Copilot review.
	if _, err := db.InsertWithTime("octo", "demo", 2, "MANUAL", priorEntryTime); err != nil {
		t.Fatalf("InsertWithTime(priorEntry) error = %v", err)
	}
	priorEntry, err := db.GetLatest("octo", "demo", 2)
	if err != nil || priorEntry == nil {
		t.Fatalf("GetLatest(priorEntry): err = %v, entry = %v", err, priorEntry)
	}
	if err := db.UpdateCompletedAt(priorEntry.ID); err != nil {
		t.Fatalf("UpdateCompletedAt(priorEntry) error = %v", err)
	}

	before := time.Now().UTC().Truncate(time.Second)
	handler := requestHandler(staticProvider(makeGitHubClient(srv)), db)
	_, out, err := handler(context.Background(), nil, RequestInput{Owner: "octo", Repo: "demo", PR: 2})
	after := time.Now().UTC().Add(time.Second)
	if err != nil {
		t.Fatalf("requestHandler() error = %v", err)
	}
	if !out.OK {
		t.Fatalf("requestHandler() OK = false, reason = %q", out.Reason)
	}

	entry, err := db.GetLatest("octo", "demo", 2)
	if err != nil {
		t.Fatalf("GetLatest() error = %v", err)
	}
	if entry == nil {
		t.Fatal("GetLatest() = nil, want trigger_log entry")
	}
	// Normal Insert uses now(): RequestedAt must be in [before, after].
	if entry.RequestedAt.Before(before) {
		t.Fatalf("RequestedAt = %v, want >= %v (normal Insert, not InsertWithTime)", entry.RequestedAt, before)
	}
	if entry.RequestedAt.After(after) {
		t.Fatalf("RequestedAt = %v, want <= %v", entry.RequestedAt, after)
	}
	// The new entry must be pending (completed_at = nil).
	if entry.CompletedAt != nil {
		t.Fatal("CompletedAt = non-nil, want nil for the fresh pending entry")
	}
}
