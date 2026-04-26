package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

// RequestInput is the input schema for request_copilot_review.
type RequestInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
}

// RequestOutput is the output schema for request_copilot_review.
type RequestOutput struct {
	OK      bool   `json:"ok"`
	Trigger string `json:"trigger,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Note    string `json:"note"`
}

// requestTool is the MCP tool definition for request_copilot_review.
var requestTool = &mcp.Tool{
	Name:        "request_copilot_review",
	Description: "Copilot にプルリクエストのレビューをリクエストする。既にレビュー進行中の場合は拒否する（REVIEW_IN_PROGRESS）。",
}

// requestHandler handles a single request_copilot_review call.
func requestHandler(
	clientProvider githubClientProvider,
	db *store.DB,
) func(context.Context, *mcp.CallToolRequest, RequestInput) (*mcp.CallToolResult, RequestOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in RequestInput) (*mcp.CallToolResult, RequestOutput, error) {
		if in.Owner == "" || in.Repo == "" || in.PR <= 0 {
			return nil, RequestOutput{}, fmt.Errorf("owner, repo, and pr are required")
		}
		ghClient, err := clientProvider(ctx, req)
		if err != nil {
			return nil, RequestOutput{}, err
		}

		// Guard: check for an in-progress trigger_log entry.
		pending, err := db.HasPending(in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, RequestOutput{}, err
		}
		if pending {
			return nil, RequestOutput{
				OK:     false,
				Reason: "REVIEW_IN_PROGRESS",
				Note:   "この PR の Copilot レビューは既に保留中または進行中です。",
			}, nil
		}

		// Guard: also check the live GitHub reviewer list.
		data, err := ghClient.GetReviewData(ctx, in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, RequestOutput{}, err
		}

		// If Copilot is already in the requested-reviewers list, reject.
		if data.IsCopilotInReviewers {
			return nil, RequestOutput{
				OK:     false,
				Reason: "REVIEW_IN_PROGRESS",
				Note:   "Copilot は既にこの PR のレビュアーとして登録されています。",
			}, nil
		}

		// Request the review via GitHub API.
		if err := ghClient.RequestCopilotReview(ctx, in.Owner, in.Repo, in.PR); err != nil {
			return nil, RequestOutput{}, err
		}

		// Record the MANUAL trigger. This must succeed so future HasPending
		// checks can prevent duplicate review requests.
		//
		// Bug B fix: If a Copilot review already exists, set requested_at to
		// sat+1s (one second after the existing review's SubmittedAt) and record
		// the existing review's ID as prev_review_id so that:
		//   • the existing review (same ID) is NOT immediately relevant
		//     (ID-based check: currentID == prevReviewID → stale)
		//   • any new review Copilot posts (different ID) WILL be relevant
		//     (ID-based check: currentID != prevReviewID → COMPLETED)
		// The +1s offset is kept as a timestamp-based fallback for entries
		// that pre-date this feature (prevReviewID == nil).
		//
		// Guard: only use InsertWithPrevReviewID when the candidate (sat+1s) is
		// newer than every prior trigger_log entry; otherwise fall back to
		// Insert(now()) so that GetLatest() continues to return the most-recent row.
		var insertErr error
		if data.LatestCopilotReview != nil {
			sat := data.LatestCopilotReview.GetSubmittedAt().Time
			if !sat.IsZero() {
				candidate := sat.UTC().Add(time.Second)
				latest, latestErr := db.GetLatest(in.Owner, in.Repo, in.PR)
				if latestErr != nil {
					return nil, RequestOutput{}, fmt.Errorf("failed to read trigger_log: %w", latestErr)
				}
				if latest == nil || candidate.After(latest.RequestedAt) {
					// candidate is the most-recent logical request time: record both
					// sat+1s and the current review ID for ID-based staleness detection.
					prevID := fmt.Sprintf("%d", data.LatestCopilotReview.GetID())
					_, insertErr = db.InsertWithPrevReviewID(in.Owner, in.Repo, in.PR, "MANUAL", candidate, prevID)
				} else {
					_, insertErr = db.Insert(in.Owner, in.Repo, in.PR, "MANUAL")
				}
			} else {
				_, insertErr = db.Insert(in.Owner, in.Repo, in.PR, "MANUAL")
			}
		} else {
			_, insertErr = db.Insert(in.Owner, in.Repo, in.PR, "MANUAL")
		}
		if insertErr != nil {
			return nil, RequestOutput{}, fmt.Errorf("copilot review requested successfully, but failed to record MANUAL trigger: %w", insertErr)
		}

		return nil, RequestOutput{
			OK:      true,
			Trigger: "MANUAL",
			Note:    "Copilot レビューをリクエストしました。",
		}, nil
	}
}

// RegisterRequestTool adds request_copilot_review to the MCP server.
func RegisterRequestTool(server *mcp.Server, clientProvider githubClientProvider, db *store.DB) {
	mcp.AddTool(server, requestTool, requestHandler(clientProvider, db))
}
