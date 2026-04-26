// Package ghclient wraps the GitHub REST and GraphQL APIs for Copilot review operations.
package ghclient

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v72/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// invalidatingTransport wraps an HTTP transport and calls invalidate(token) when
// GitHub returns HTTP 401, so the auth token cache is cleared immediately rather
// than waiting for cacheTTL to expire.
type invalidatingTransport struct {
	inner      http.RoundTripper
	token      string
	invalidate func(string)
}

func (t *invalidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err == nil && resp != nil && resp.StatusCode == http.StatusUnauthorized {
		t.invalidate(t.token)
	}
	return resp, err
}

// copilotBotLogin is the login passed to the GraphQL requestReviewsByLogin mutation's
// botLogins field. The [bot] suffix is part of the actor identity on GitHub's side.
const copilotBotLogin = "copilot-pull-request-reviewer[bot]"

// copilotLogins lists the known GitHub Copilot reviewer identities used for
// detection in GetReviewData. Kept separate from copilotBotLogin (request path).
//
// "Copilot" (capitalized, no [bot] suffix) is returned by the REST
// requested_reviewers endpoint and must be included here so PENDING and
// IN_PROGRESS can be detected via IsCopilotInReviewers.
var copilotLogins = []string{
	"Copilot",
	"github-copilot[bot]",
	"copilot-pull-request-reviewer[bot]",
	"github-copilot",
}

func isCopilot(login string) bool {
	for _, l := range copilotLogins {
		if login == l {
			return true
		}
	}
	return false
}

// IsCopilotLogin reports whether login belongs to a known Copilot bot identity.
func IsCopilotLogin(login string) bool {
	return isCopilot(login)
}

// ReviewStatus represents the Copilot review lifecycle state for a PR.
type ReviewStatus string

const (
	StatusNotRequested ReviewStatus = "NOT_REQUESTED"
	StatusPending      ReviewStatus = "PENDING"
	StatusInProgress   ReviewStatus = "IN_PROGRESS"
	StatusCompleted    ReviewStatus = "COMPLETED"
	StatusBlocked      ReviewStatus = "BLOCKED"
)

// ReviewData holds the raw review information fetched from GitHub.
type ReviewData struct {
	// IsCopilotInReviewers is true when Copilot is in the PR's requested reviewers list.
	IsCopilotInReviewers bool
	// LatestCopilotReview is the most recently submitted Copilot review, or nil.
	LatestCopilotReview *github.PullRequestReview
	// RateLimitRemaining is the number of remaining API calls in the current window.
	RateLimitRemaining int
	// RateLimitReset is when the rate limit window resets.
	RateLimitReset time.Time
	// ReviewRequestedAt is the CreatedAt of the most recent "review_requested" timeline
	// event for Copilot. Used as the primary source for staleness detection and as the
	// requestedAt baseline when no trigger_log DB entry exists (AUTO trigger).
	ReviewRequestedAt *time.Time
	// WorkStartedAt is the CreatedAt of the most recent "copilot_work_started" timeline
	// event. When WorkStartedAt > ReviewRequestedAt, the review is IN_PROGRESS;
	// otherwise it is PENDING. This field is REST-timeline-only; GraphQL has no
	// corresponding __typename for this event.
	WorkStartedAt *time.Time
	// SkippedTimeline is true when the timeline fetch was skipped due to low rate-limit
	// budget. When true, ReviewRequestedAt and WorkStartedAt are nil and callers should
	// fall back to DB-recorded requestedAt for staleness decisions.
	SkippedTimeline bool
}

// Client wraps the GitHub API for Copilot review operations.
type Client struct {
	gh        *github.Client
	v4        *githubv4.Client
	threshold time.Duration // elapsed threshold for PENDING→IN_PROGRESS
}

// NewClient creates a new GitHub API client authenticated with the given token.
// ctx should be the request context so GitHub API calls are cancelled when the
// request is cancelled or times out.
// invalidate is called with the token when GitHub returns HTTP 401, allowing the
// auth layer to clear its cache immediately. May be nil to disable invalidation.
// threshold is the elapsed time after which PENDING becomes IN_PROGRESS.
func NewClient(ctx context.Context, token string, threshold time.Duration, invalidate func(string)) *Client {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, src)
	if invalidate != nil {
		httpClient.Transport = &invalidatingTransport{
			inner:      httpClient.Transport,
			token:      token,
			invalidate: invalidate,
		}
	}
	return &Client{
		gh:        github.NewClient(httpClient),
		v4:        githubv4.NewClient(httpClient),
		threshold: threshold,
	}
}

// NewWithClients creates a Client from pre-built REST and GraphQL API clients.
// Intended for tests that need to inject mock HTTP servers in place of api.github.com.
func NewWithClients(gh *github.Client, v4 *githubv4.Client, threshold time.Duration) *Client {
	return &Client{gh: gh, v4: v4, threshold: threshold}
}

// GetReviewData fetches raw reviewer and review data for a PR from GitHub.
func (c *Client) GetReviewData(ctx context.Context, owner, repo string, prNumber int) (*ReviewData, error) {
	data := &ReviewData{}

	// Fetch current requested reviewers.
	reviewers, resp, err := c.gh.PullRequests.ListReviewers(ctx, owner, repo, prNumber, nil)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		data.RateLimitRemaining = resp.Rate.Remaining
		data.RateLimitReset = resp.Rate.Reset.Time
	}
	for _, u := range reviewers.Users {
		if isCopilot(u.GetLogin()) {
			data.IsCopilotInReviewers = true
			break
		}
	}

	// Short-circuit if rate limit is already low to avoid consuming the second call.
	if data.RateLimitRemaining < 10 {
		return data, nil
	}

	// Fetch submitted reviews across all pages.
	reviewOpts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp2, err := c.gh.PullRequests.ListReviews(ctx, owner, repo, prNumber, reviewOpts)
		if err != nil {
			return nil, err
		}
		if resp2 != nil {
			data.RateLimitRemaining = resp2.Rate.Remaining
			data.RateLimitReset = resp2.Rate.Reset.Time
		}
		for _, r := range reviews {
			if !isCopilot(r.GetUser().GetLogin()) {
				continue
			}
			if data.LatestCopilotReview == nil ||
				r.GetSubmittedAt().After(data.LatestCopilotReview.GetSubmittedAt().Time) {
				r2 := r // avoid loop-variable capture
				data.LatestCopilotReview = r2
			}
		}
		if resp2 == nil || resp2.NextPage == 0 {
			break
		}
		reviewOpts.Page = resp2.NextPage
	}

	// Timeline is needed when Copilot is currently in requested_reviewers (PENDING vs
	// IN_PROGRESS distinction) OR when a Copilot review already exists and we need the
	// most recent review_requested event as the authoritative staleness baseline.
	needsTimeline := data.IsCopilotInReviewers || data.LatestCopilotReview != nil
	if needsTimeline {
		// Short-circuit before timeline fetch if rate limit is too low.
		if data.RateLimitRemaining < 10 {
			data.SkippedTimeline = true
			return data, nil
		}

		// Fetch REST timeline events for event-based PENDING/IN_PROGRESS detection
		// and review_requested timestamps. copilot_work_started exists only in the
		// REST timeline (no GraphQL equivalent).
		timelineOpts := &github.ListOptions{PerPage: 100}
		for {
			events, resp3, err := c.gh.Issues.ListIssueTimeline(ctx, owner, repo, prNumber, timelineOpts)
			if err != nil {
				// Non-fatal: timeline is best-effort; proceed with what we have.
				break
			}
			if resp3 != nil {
				data.RateLimitRemaining = resp3.Rate.Remaining
				data.RateLimitReset = resp3.Rate.Reset.Time
			}
			for _, ev := range events {
				switch ev.GetEvent() {
				case "review_requested":
					// Reviewer is json:"requested_reviewer" on the Timeline struct.
					if r := ev.GetReviewer(); r != nil && isCopilot(r.GetLogin()) {
						t := ev.GetCreatedAt().Time
						if data.ReviewRequestedAt == nil || t.After(*data.ReviewRequestedAt) {
							data.ReviewRequestedAt = &t
						}
					}
				case "copilot_work_started":
					t := ev.GetCreatedAt().Time
					if data.WorkStartedAt == nil || t.After(*data.WorkStartedAt) {
						data.WorkStartedAt = &t
					}
				}
			}
			if resp3 == nil || resp3.NextPage == 0 {
				break
			}
			timelineOpts.Page = resp3.NextPage
		}
	} // end if needsTimeline

	return data, nil
}

// DeriveStatus resolves the ReviewStatus from raw data and optional trigger_log requestedAt.
// requestedAt is the DB trigger_log entry time (MANUAL trigger); nil for AUTO trigger.
// prevReviewID, when non-nil, enables ID-based staleness detection: the existing review is
// treated as stale only if its ID matches prevReviewID (same review seen before the request).
//
// PENDING vs IN_PROGRESS is determined by the timeline events in data:
//   - WorkStartedAt > ReviewRequestedAt → IN_PROGRESS
//   - otherwise (WorkStartedAt absent, or before the latest request) → PENDING
//
// Staleness detection uses data.ReviewRequestedAt (REST timeline) as the primary source,
// falling back to the requestedAt parameter when timeline data is unavailable.
func (c *Client) DeriveStatus(data *ReviewData, requestedAt *time.Time, prevReviewID *string) ReviewStatus {
	return DeriveStatus(data, requestedAt, prevReviewID)
}

// DeriveStatus resolves the ReviewStatus from raw ReviewData.
// It is the package-level counterpart of (*Client).DeriveStatus for callers that do
// not hold a Client reference (e.g. the watch manager).
func DeriveStatus(data *ReviewData, requestedAt *time.Time, prevReviewID *string) ReviewStatus {
	// Prefer timeline-sourced requestedAt for accurate staleness detection.
	// Fall back to the DB trigger_log entry only when timeline data is unavailable.
	effectiveRequestedAt := data.ReviewRequestedAt
	if effectiveRequestedAt == nil {
		effectiveRequestedAt = requestedAt
	}

	if data.LatestCopilotReview != nil {
		// Only treat this review as relevant if it was submitted at/after the request time,
		// or (when prevReviewID is set) if its ID differs from the pre-request review.
		relevant := true
		if effectiveRequestedAt != nil {
			if prevReviewID != nil {
				// ID-based: relevant only if this is a different review from when the
				// request was made. Same ID means the old review is still present (stale).
				currentID := strconv.FormatInt(data.LatestCopilotReview.GetID(), 10)
				relevant = currentID != *prevReviewID
			} else {
				// Timestamp-based fallback for entries without prevReviewID (backward compat).
				// - Use !Before (≥) instead of After (>) to include same-second events.
				// - Zero submittedAt means the review has no timestamp → treat as stale.
				sat := data.LatestCopilotReview.GetSubmittedAt()
				relevant = !sat.IsZero() && !sat.Before(*effectiveRequestedAt)
			}
		}
		if relevant {
			if data.LatestCopilotReview.GetState() == "CHANGES_REQUESTED" {
				return StatusBlocked
			}
			return StatusCompleted
		}
	}
	if data.IsCopilotInReviewers {
		// Event-based PENDING vs IN_PROGRESS: copilot_work_started after review_requested
		// means work has begun on the current cycle.
		if data.WorkStartedAt != nil &&
			(data.ReviewRequestedAt == nil || data.WorkStartedAt.After(*data.ReviewRequestedAt)) {
			return StatusInProgress
		}
		return StatusPending
	}
	return StatusNotRequested
}

// DeriveStatusWithThreshold is kept for backward compatibility.
// The threshold parameter is ignored; use DeriveStatus instead.
func DeriveStatusWithThreshold(_ time.Duration, data *ReviewData, requestedAt *time.Time, prevReviewID *string) ReviewStatus {
	return DeriveStatus(data, requestedAt, prevReviewID)
}

// IsAuthError reports whether err is a GitHub authentication failure.
func IsAuthError(err error) bool {
	var ghErr *github.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusUnauthorized
}

// prNodeIDQuery fetches the GraphQL node ID for a pull request.
// Used by RequestCopilotReview to obtain the ID required by the mutation.
type prNodeIDQuery struct {
	Repository struct {
		PullRequest struct {
			ID githubv4.ID
		} `graphql:"pullRequest(number: $pr)"`
	} `graphql:"repository(owner: $owner, name: $repo)"`
}

// requestReviewsByLoginMutation is the GraphQL mutation for requesting PR reviews by login.
type requestReviewsByLoginMutation struct {
	RequestReviewsByLogin struct {
		ClientMutationID string `graphql:"clientMutationId"`
	} `graphql:"requestReviewsByLogin(input: $input)"`
}

// buildCopilotReviewInput constructs the mutation input for adding Copilot as a reviewer.
// union: true preserves existing reviewers (additive); false would replace the entire set.
func buildCopilotReviewInput(prNodeID githubv4.ID) githubv4.RequestReviewsByLoginInput {
	botLogins := []githubv4.String{githubv4.String(copilotBotLogin)}
	userLogins := []githubv4.String{}
	teamSlugs := []githubv4.String{}
	union := githubv4.Boolean(true)

	return githubv4.RequestReviewsByLoginInput{
		PullRequestID: prNodeID,
		BotLogins:     &botLogins,
		UserLogins:    &userLogins,
		TeamSlugs:     &teamSlugs,
		Union:         &union,
	}
}

// RequestCopilotReview adds Copilot as a reviewer on the given PR using the GraphQL
// requestReviewsByLogin mutation. This is the only reliable path on github.com;
// the REST requested_reviewers endpoint silently no-ops for bot actors (#47).
func (c *Client) RequestCopilotReview(ctx context.Context, owner, repo string, prNumber int) error {
	if prNumber <= 0 || prNumber > math.MaxInt32 {
		return fmt.Errorf("pr number out of valid range: %d", prNumber)
	}

	// Step 1: resolve the PR's GraphQL node ID (distinct from the REST integer ID).
	var nodeQ prNodeIDQuery
	if err := c.v4.Query(ctx, &nodeQ, map[string]interface{}{
		"owner": githubv4.String(owner),
		"repo":  githubv4.String(repo),
		"pr":    githubv4.Int(int32(prNumber)), //nolint:gosec // range checked above
	}); err != nil {
		return fmt.Errorf("failed to fetch PR node ID: %w", err)
	}
	prNodeID := nodeQ.Repository.PullRequest.ID
	// Guard: Query may succeed with a zero-value struct when the PR doesn't exist
	// or the token lacks permission. Catch both nil and empty-string cases.
	if prNodeID == nil || prNodeID == "" {
		return fmt.Errorf("pull request node ID is empty (PR #%d may not exist or token lacks permission)", prNumber)
	}

	// Step 2: request review via GraphQL mutation.
	var m requestReviewsByLoginMutation
	input := buildCopilotReviewInput(prNodeID)
	if err := c.v4.Mutate(ctx, &m, input, nil); err != nil {
		return fmt.Errorf("requestReviewsByLogin mutation failed: %w", err)
	}
	return nil
}

// ─── GraphQL types for review thread operations ───────────────────────────────

// reviewThreadsPageQuery fetches one page of PR review threads via GraphQL.
type reviewThreadsPageQuery struct {
	Repository struct {
		PullRequest struct {
			ReviewThreads struct {
				Nodes    []reviewThreadNode
				PageInfo struct {
					HasNextPage githubv4.Boolean
					EndCursor   githubv4.String
				}
			} `graphql:"reviewThreads(first: 100, after: $cursor)"`
		} `graphql:"pullRequest(number: $pr)"`
	} `graphql:"repository(owner: $owner, name: $repo)"`
}

type reviewThreadNode struct {
	ID         githubv4.ID
	IsResolved githubv4.Boolean
	IsOutdated githubv4.Boolean
	Path       githubv4.String
	Line       *githubv4.Int
	StartLine  *githubv4.Int
	Comments   struct {
		Nodes []struct {
			Body      githubv4.String
			CreatedAt githubv4.DateTime
			Author    struct {
				Login githubv4.String
			}
		}
	} `graphql:"comments(first: 10)"`
}

// threadNodeQuery fetches a single thread's resolved status by global node ID.
type threadNodeQuery struct {
	Node struct {
		PullRequestReviewThread struct {
			IsResolved githubv4.Boolean
		} `graphql:"... on PullRequestReviewThread"`
	} `graphql:"node(id: $id)"`
}

// threadMetadataQuery fetches owner, repo, PR number, and first comment database ID
// from a review thread node ID. Used to reply via REST API.
type threadMetadataQuery struct {
	Node struct {
		PullRequestReviewThread struct {
			PullRequest struct {
				Number     githubv4.Int
				Repository struct {
					Name  githubv4.String
					Owner struct {
						Login githubv4.String
					}
				}
			}
			Comments struct {
				Nodes []struct {
					DatabaseId int64 // githubv4.Int (int32) overflows for large comment IDs
				}
			} `graphql:"comments(first: 1)"`
		} `graphql:"... on PullRequestReviewThread"`
	} `graphql:"node(id: $id)"`
}

// resolveThreadMutation is the GraphQL mutation for resolving a review thread.
type resolveThreadMutation struct {
	ResolveReviewThread struct {
		Thread struct {
			IsResolved githubv4.Boolean
		}
	} `graphql:"resolveReviewThread(input: $input)"`
}

// ResolveReviewThreadInput is the input for resolveThreadMutation.
// Must be PascalCase so shurcooL/githubv4 sends the correct GraphQL type name.
type ResolveReviewThreadInput struct {
	ThreadID githubv4.ID `json:"threadId"`
}

// ─── Public review thread types ──────────────────────────────────────────────

// ThreadComment is a single comment within a review thread.
type ThreadComment struct {
	Author    string
	Body      string
	CreatedAt string
}

// ReviewThread is the parsed representation of a PR review thread.
type ReviewThread struct {
	ID         string
	IsResolved bool
	IsOutdated bool
	Path       string
	Line       *int32
	StartLine  *int32
	Comments   []ThreadComment
}

// ReplyResult holds the result of a reply-to-thread operation.
type ReplyResult struct {
	CommentID string
	CreatedAt string
}

// ─── GraphQL methods ──────────────────────────────────────────────────────────

// GetReviewThreads fetches all review threads for a PR using GraphQL (paginated).
func (c *Client) GetReviewThreads(ctx context.Context, owner, repo string, pr int) ([]ReviewThread, error) {
	if pr <= 0 || pr > math.MaxInt32 {
		return nil, fmt.Errorf("pr number out of valid range: %d", pr)
	}
	vars := map[string]interface{}{
		"owner":  githubv4.String(owner),
		"repo":   githubv4.String(repo),
		"pr":     githubv4.Int(int32(pr)), //nolint:gosec // range checked above
		"cursor": (*githubv4.String)(nil),
	}

	var allNodes []reviewThreadNode
	for {
		var q reviewThreadsPageQuery
		if err := c.v4.Query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("graphql query failed: %w", err)
		}
		allNodes = append(allNodes, q.Repository.PullRequest.ReviewThreads.Nodes...)
		if !bool(q.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage) {
			break
		}
		cursor := q.Repository.PullRequest.ReviewThreads.PageInfo.EndCursor
		vars["cursor"] = &cursor
	}

	threads := make([]ReviewThread, 0, len(allNodes))
	for _, n := range allNodes {
		t := ReviewThread{
			ID:         fmt.Sprintf("%v", n.ID),
			IsResolved: bool(n.IsResolved),
			IsOutdated: bool(n.IsOutdated),
			Path:       string(n.Path),
		}
		if n.Line != nil {
			v := int32(*n.Line)
			t.Line = &v
		}
		if n.StartLine != nil {
			v := int32(*n.StartLine)
			t.StartLine = &v
		}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, ThreadComment{
				Author:    string(c.Author.Login),
				Body:      string(c.Body),
				CreatedAt: c.CreatedAt.Format(time.RFC3339),
			})
		}
		threads = append(threads, t)
	}
	return threads, nil
}

// IsThreadResolved checks whether a review thread is already resolved.
// threadID must be a PRRT_xxx global node ID.
func (c *Client) IsThreadResolved(ctx context.Context, threadID string) (bool, error) {
	var q threadNodeQuery
	vars := map[string]interface{}{
		"id": githubv4.ID(threadID),
	}
	if err := c.v4.Query(ctx, &q, vars); err != nil {
		return false, fmt.Errorf("graphql query failed: %w", err)
	}
	return bool(q.Node.PullRequestReviewThread.IsResolved), nil
}

// ReplyToThread adds a reply to a review thread via the REST API.
// It first queries the thread node to resolve owner/repo/PR/commentID,
// then calls CreateCommentInReplyTo. This avoids the deprecated
// addPullRequestReviewCommentReply GraphQL mutation.
func (c *Client) ReplyToThread(ctx context.Context, threadID, body string) (ReplyResult, error) {
	// Resolve thread metadata via GraphQL.
	var q threadMetadataQuery
	if err := c.v4.Query(ctx, &q, map[string]interface{}{"id": githubv4.ID(threadID)}); err != nil {
		return ReplyResult{}, fmt.Errorf("graphql query failed: %w", err)
	}
	meta := q.Node.PullRequestReviewThread
	if len(meta.Comments.Nodes) == 0 {
		return ReplyResult{}, fmt.Errorf("thread has no comments")
	}
	owner := string(meta.PullRequest.Repository.Owner.Login)
	repo := string(meta.PullRequest.Repository.Name)
	prNum := int(meta.PullRequest.Number)
	commentID := int64(meta.Comments.Nodes[0].DatabaseId)

	// Post reply via REST API.
	comment, _, err := c.gh.PullRequests.CreateCommentInReplyTo(ctx, owner, repo, prNum, body, commentID)
	if err != nil {
		return ReplyResult{}, fmt.Errorf("REST API reply failed: %w", err)
	}
	return ReplyResult{
		CommentID: strconv.FormatInt(comment.GetID(), 10),
		CreatedAt: comment.GetCreatedAt().Format(time.RFC3339),
	}, nil
}

// GetCIStatus returns true when all GitHub Check Runs for the PR's head commit have
// passed (conclusion: success, skipped, or neutral). Returns true when no check runs exist.
// Returns false when any run is not yet completed or has a failing conclusion.
func (c *Client) GetCIStatus(ctx context.Context, owner, repo string, prNumber int) (bool, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return false, fmt.Errorf("failed to get PR: %w", err)
	}
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return false, fmt.Errorf("PR #%d head SHA is empty", prNumber)
	}

	opts := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		result, resp, err := c.gh.Checks.ListCheckRunsForRef(ctx, owner, repo, sha, opts)
		if err != nil {
			return false, fmt.Errorf("failed to list check runs: %w", err)
		}
		for _, r := range result.CheckRuns {
			if r.GetStatus() != "completed" {
				return false, nil
			}
			switch r.GetConclusion() {
			case "success", "skipped", "neutral":
			default:
				return false, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return true, nil
}

// ResolveThread resolves a review thread. Returns true if it was already resolved before the call.
func (c *Client) ResolveThread(ctx context.Context, threadID string) (alreadyResolved bool, err error) {
	alreadyResolved, err = c.IsThreadResolved(ctx, threadID)
	if err != nil {
		return false, err
	}
	if alreadyResolved {
		return true, nil
	}
	var m resolveThreadMutation
	input := ResolveReviewThreadInput{ThreadID: githubv4.ID(threadID)}
	if err := c.v4.Mutate(ctx, &m, input, nil); err != nil {
		return false, fmt.Errorf("graphql mutation failed: %w", err)
	}
	return false, nil
}
