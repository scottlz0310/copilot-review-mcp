// Package tools registers the Copilot-review MCP tools (status, request, wait, and thread operations) and builds the MCP server.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

// GetStatusInput is the input schema for get_copilot_review_status.
type GetStatusInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
}

// GetStatusOutput is the output schema for get_copilot_review_status.
type GetStatusOutput struct {
	Requested           bool    `json:"requested"`
	Status              string  `json:"status"`
	Trigger             *string `json:"trigger"`
	IsBlocking          bool    `json:"isBlocking"`
	LastReviewAt        *string `json:"lastReviewAt"`
	ElapsedSinceRequest *string `json:"elapsedSinceRequest"`
}

// statusTool is the MCP tool definition for get_copilot_review_status.
var statusTool = &mcp.Tool{
	Name: "get_copilot_review_status",
	Description: "GitHub 上の Copilot review 状態を即時 snapshot として返す。" +
		"推奨経路では、まずこの tool で現状確認し、未完了なら start_copilot_review_watch を開始する。" +
		"ステータスは NOT_REQUESTED / PENDING / IN_PROGRESS / COMPLETED / BLOCKED のいずれか。",
}

// statusHandler handles a single get_copilot_review_status call.
func statusHandler(
	clientProvider githubClientProvider,
	db *store.DB,
) func(context.Context, *mcp.CallToolRequest, GetStatusInput) (*mcp.CallToolResult, GetStatusOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in GetStatusInput) (*mcp.CallToolResult, GetStatusOutput, error) {
		if in.Owner == "" || in.Repo == "" || in.PR <= 0 {
			return nil, GetStatusOutput{}, fmt.Errorf("owner, repo, and pr are required")
		}

		ghClient, err := clientProvider(ctx, req)
		if err != nil {
			return nil, GetStatusOutput{}, err
		}

		data, err := ghClient.GetReviewData(ctx, in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, GetStatusOutput{}, err
		}

		// GetReviewData short-circuits ListReviews when rate limit is low.
		// In that case LatestCopilotReview is nil regardless of actual state,
		// so DeriveStatus would produce a wrong result. Return an error instead.
		if data.RateLimitRemaining < 10 {
			return nil, GetStatusOutput{}, fmt.Errorf(
				"GitHub API rate limit too low (%d remaining); retry after %s",
				data.RateLimitRemaining, data.RateLimitReset.UTC().Format(time.RFC3339),
			)
		}

		entry, err := db.GetLatest(in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, GetStatusOutput{}, err
		}

		var requestedAt *time.Time
		var prevReviewID *string
		if entry != nil {
			requestedAt = &entry.RequestedAt
			prevReviewID = entry.PrevReviewID
		}

		status := ghClient.DeriveStatus(data, requestedAt, prevReviewID)

		// Auto-update completed_at when the review is done.
		if (status == ghclient.StatusCompleted || status == ghclient.StatusBlocked) &&
			entry != nil && entry.CompletedAt == nil {
			if err := db.UpdateCompletedAt(entry.ID); err != nil {
				return nil, GetStatusOutput{}, fmt.Errorf("failed to update completed_at: %w", err)
			}
		}

		out := GetStatusOutput{
			Requested:  data.IsCopilotInReviewers || data.LatestCopilotReview != nil,
			Status:     string(status),
			IsBlocking: status == ghclient.StatusBlocked,
		}

		if data.LatestCopilotReview != nil {
			s := data.LatestCopilotReview.GetSubmittedAt().UTC().Format(time.RFC3339)
			out.LastReviewAt = &s
		}

		if entry != nil {
			elapsed := time.Since(entry.RequestedAt)
			s := fmtDuration(elapsed)
			out.ElapsedSinceRequest = &s
			t := entry.Trigger
			out.Trigger = &t
		} else if data.IsCopilotInReviewers {
			// No trigger_log entry but Copilot is in reviewers → AUTO trigger inferred.
			auto := "AUTO"
			out.Trigger = &auto
		}

		return nil, out, nil
	}
}

// RegisterStatusTool adds get_copilot_review_status to the MCP server.
func RegisterStatusTool(server *mcp.Server, clientProvider githubClientProvider, db *store.DB) {
	mcp.AddTool(server, statusTool, statusHandler(clientProvider, db))
}

// fmtDuration formats a duration as a human-readable string.
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
