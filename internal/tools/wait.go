package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

// WaitInput is the input schema for wait_for_copilot_review.
type WaitInput struct {
	Owner               string `json:"owner"`
	Repo                string `json:"repo"`
	PR                  int    `json:"pr"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	MaxPolls            int    `json:"max_polls"`
}

// WaitOutput is the output schema for wait_for_copilot_review.
type WaitOutput struct {
	Status        string           `json:"status"`
	ReviewStatus  *GetStatusOutput `json:"review_status"`
	PollsDone     int              `json:"polls_done"`
	WaitedSeconds int              `json:"waited_seconds"`
}

// waitTool is the MCP tool definition for wait_for_copilot_review.
var waitTool = &mcp.Tool{
	Name: "wait_for_copilot_review",
	Description: "Legacy fallback。" +
		"Copilot のレビューが COMPLETED または BLOCKED になるまで、この tool call 自体を block しながら定期ポーリングする。" +
		"通常は get_copilot_review_status と watch 系ツールを優先し、この tool は host が通知や cheap status read を扱いにくい場合だけ使う。" +
		"タイムアウト時は TIMEOUT、レート制限時は RATE_LIMITED、コンテキストキャンセル時は CANCELLED を返す。",
}

// waitHandler handles a single wait_for_copilot_review call.
func waitHandler(
	clientProvider githubClientProvider,
	db *store.DB,
) func(context.Context, *mcp.CallToolRequest, WaitInput) (*mcp.CallToolResult, WaitOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in WaitInput) (*mcp.CallToolResult, WaitOutput, error) {
		if in.Owner == "" || in.Repo == "" || in.PR <= 0 {
			return nil, WaitOutput{}, fmt.Errorf("owner, repo, and pr are required")
		}
		// Apply defaults.
		if in.PollIntervalSeconds <= 0 {
			in.PollIntervalSeconds = 120
		}
		if in.MaxPolls <= 0 {
			in.MaxPolls = 5
		}
		// Enforce upper bounds to prevent goroutine pinning / DoS.
		const maxPollIntervalSeconds = 3600
		const maxMaxPolls = 100
		if in.PollIntervalSeconds > maxPollIntervalSeconds {
			return nil, WaitOutput{}, fmt.Errorf("poll_interval_seconds must not exceed %d", maxPollIntervalSeconds)
		}
		if in.MaxPolls > maxMaxPolls {
			return nil, WaitOutput{}, fmt.Errorf("max_polls must not exceed %d", maxMaxPolls)
		}
		// Enforce a total-wait ceiling to cap goroutine occupancy.
		const maxTotalWait = 30 * time.Minute
		totalWait := time.Duration(in.PollIntervalSeconds) * time.Duration(in.MaxPolls-1) * time.Second
		if totalWait > maxTotalWait {
			return nil, WaitOutput{}, fmt.Errorf(
				"total wait time must not exceed %d seconds (poll_interval_seconds\u00d7(max_polls−1) = %d)",
				int(maxTotalWait.Seconds()), int(totalWait.Seconds()),
			)
		}

		pollInterval := time.Duration(in.PollIntervalSeconds) * time.Second
		start := time.Now()
		ghClient, err := clientProvider(ctx, req)
		if err != nil {
			return nil, WaitOutput{}, err
		}

		// lastData/lastEntry/lastStatus hold the most recent successful poll result.
		// Reused by TIMEOUT and CANCELLED paths to avoid an extra API call.
		var lastData *ghclient.ReviewData
		var lastEntry *store.TriggerEntry
		var lastStatus ghclient.ReviewStatus

		for poll := 0; poll < in.MaxPolls; poll++ {
			// Wait between polls (skip on first iteration).
			if poll > 0 {
				timer := time.NewTimer(pollInterval)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return nil, buildCancelledOutput(lastData, lastEntry, lastStatus, poll, time.Since(start)), ctx.Err()
				case <-timer.C:
				}
			}

			data, err := ghClient.GetReviewData(ctx, in.Owner, in.Repo, in.PR)
			if err != nil {
				if isCancellation(err) {
					return nil, buildCancelledOutput(lastData, lastEntry, lastStatus, poll, time.Since(start)), err
				}
				return nil, WaitOutput{}, err
			}

			// Check rate limit before proceeding.
			if data.RateLimitRemaining < 10 {
				entry, err := db.GetLatest(in.Owner, in.Repo, in.PR)
				if err != nil {
					if isCancellation(err) {
						return nil, buildCancelledOutput(lastData, lastEntry, lastStatus, poll, time.Since(start)), err
					}
					return nil, WaitOutput{}, fmt.Errorf("failed to get latest entry (RATE_LIMITED): %w", err)
				}
				var reqAt *time.Time
				var prevReviewID *string
				if entry != nil {
					reqAt = &entry.RequestedAt
					prevReviewID = entry.PrevReviewID
				}
				partialStatus := ghClient.DeriveStatus(data, reqAt, prevReviewID)
				rs := buildStatusOutput(data, entry, partialStatus)
				return nil, WaitOutput{
					Status:        "RATE_LIMITED",
					ReviewStatus:  &rs,
					PollsDone:     poll + 1,
					WaitedSeconds: int(time.Since(start).Seconds()),
				}, nil
			}

			entry, err := db.GetLatest(in.Owner, in.Repo, in.PR)
			if err != nil {
				if isCancellation(err) {
					return nil, buildCancelledOutput(lastData, lastEntry, lastStatus, poll, time.Since(start)), err
				}
				return nil, WaitOutput{}, err
			}

			var requestedAt *time.Time
			var prevReviewID *string
			if entry != nil {
				requestedAt = &entry.RequestedAt
				prevReviewID = entry.PrevReviewID
			}

			status := ghClient.DeriveStatus(data, requestedAt, prevReviewID)

			// Auto-update completed_at.
			if (status == ghclient.StatusCompleted || status == ghclient.StatusBlocked) &&
				entry != nil && entry.CompletedAt == nil {
				if err := db.UpdateCompletedAt(entry.ID); err != nil {
					return nil, WaitOutput{}, fmt.Errorf("failed to update completed_at: %w", err)
				}
			}

			// Cache for TIMEOUT/CANCELLED paths.
			lastData = data
			lastEntry = entry
			lastStatus = status

			if status == ghclient.StatusCompleted || status == ghclient.StatusBlocked {
				rs := buildStatusOutput(data, entry, status)
				return nil, WaitOutput{
					Status:        string(status),
					ReviewStatus:  &rs,
					PollsDone:     poll + 1,
					WaitedSeconds: int(time.Since(start).Seconds()),
				}, nil
			}
		}

		// All polls exhausted — check context before reporting TIMEOUT.
		if err := ctx.Err(); err != nil {
			return nil, buildCancelledOutput(lastData, lastEntry, lastStatus, in.MaxPolls, time.Since(start)), err
		}
		rs := buildStatusOutput(lastData, lastEntry, lastStatus)
		return nil, WaitOutput{
			Status:        "TIMEOUT",
			ReviewStatus:  &rs,
			PollsDone:     in.MaxPolls,
			WaitedSeconds: int(time.Since(start).Seconds()),
		}, nil
	}
}

// isCancellation reports whether err represents a context cancellation or deadline.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// buildCancelledOutput assembles a CANCELLED WaitOutput.
// data may be nil when cancellation occurs before the first successful poll;
// in that case ReviewStatus is omitted but Status/PollsDone/WaitedSeconds are still set.
func buildCancelledOutput(data *ghclient.ReviewData, entry *store.TriggerEntry, status ghclient.ReviewStatus, pollsDone int, waited time.Duration) WaitOutput {
	out := WaitOutput{
		Status:        "CANCELLED",
		PollsDone:     pollsDone,
		WaitedSeconds: int(waited.Seconds()),
	}
	if data != nil {
		rs := buildStatusOutput(data, entry, status)
		out.ReviewStatus = &rs
	}
	return out
}

// buildStatusOutput assembles a GetStatusOutput from already-fetched data.
func buildStatusOutput(data *ghclient.ReviewData, entry *store.TriggerEntry, status ghclient.ReviewStatus) GetStatusOutput {
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
	return out
}

// RegisterWaitTool adds wait_for_copilot_review to the MCP server.
func RegisterWaitTool(server *mcp.Server, clientProvider githubClientProvider, db *store.DB) {
	mcp.AddTool(server, waitTool, waitHandler(clientProvider, db))
}
