package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scottlz0310/copilot-review-mcp/internal/middleware"
	"github.com/scottlz0310/copilot-review-mcp/internal/watch"
)

const (
	defaultWatchListLimit = 20
	maxWatchListLimit     = 100

	nextActionPollAfter              = "POLL_AFTER"
	nextActionReadReviewThreads      = "READ_REVIEW_THREADS"
	nextActionStartNewWatch          = "START_NEW_WATCH"
	nextActionReauthAndStartNewWatch = "REAUTH_AND_START_NEW_WATCH"
	nextActionCheckFailure           = "CHECK_FAILURE"
)

// ReviewWatchView is the LLM-facing view of one watch snapshot.
type ReviewWatchView struct {
	WatchID               string  `json:"watch_id"`
	Owner                 string  `json:"owner"`
	Repo                  string  `json:"repo"`
	PR                    int     `json:"pr"`
	ResourceURI           *string `json:"resource_uri,omitempty"`
	WatchStatus           string  `json:"watch_status"`
	ReviewStatus          *string `json:"review_status,omitempty"`
	FailureReason         *string `json:"failure_reason,omitempty"`
	RecommendedNextAction string  `json:"recommended_next_action"`
	NextPollSeconds       *int    `json:"next_poll_seconds,omitempty"`
	Terminal              bool    `json:"terminal"`
	WorkerRunning         bool    `json:"worker_running"`
	PollsDone             int     `json:"polls_done"`
	StartedAt             string  `json:"started_at"`
	UpdatedAt             string  `json:"updated_at"`
	LastPolledAt          *string `json:"last_polled_at,omitempty"`
	CompletedAt           *string `json:"completed_at,omitempty"`
	StaleAt               *string `json:"stale_at,omitempty"`
	RateLimitResetAt      *string `json:"rate_limit_reset_at,omitempty"`
	LastError             *string `json:"last_error,omitempty"`
}

// StartReviewWatchInput is the input schema for start_copilot_review_watch.
type StartReviewWatchInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
}

// StartReviewWatchOutput is the output schema for start_copilot_review_watch.
type StartReviewWatchOutput struct {
	ReviewWatchView
	Reused bool   `json:"reused"`
	Note   string `json:"note"`
}

// GetReviewWatchStatusInput is the input schema for get_copilot_review_watch_status.
type GetReviewWatchStatusInput struct {
	WatchID string `json:"watch_id,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Repo    string `json:"repo,omitempty"`
	PR      int    `json:"pr,omitempty"`
}

// GetReviewWatchStatusOutput is the output schema for get_copilot_review_watch_status.
type GetReviewWatchStatusOutput struct {
	Found bool             `json:"found"`
	Watch *ReviewWatchView `json:"watch,omitempty"`
	Note  string           `json:"note"`
}

// ListReviewWatchesInput is the input schema for list_copilot_review_watches.
type ListReviewWatchesInput struct {
	Owner      string `json:"owner,omitempty"`
	Repo       string `json:"repo,omitempty"`
	PR         int    `json:"pr,omitempty"`
	ActiveOnly bool   `json:"active_only,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// ListReviewWatchesOutput is the output schema for list_copilot_review_watches.
type ListReviewWatchesOutput struct {
	Watches []ReviewWatchView `json:"watches"`
	Count   int               `json:"count"`
	Note    string            `json:"note"`
}

// CancelReviewWatchInput is the input schema for cancel_copilot_review_watch.
type CancelReviewWatchInput struct {
	WatchID string `json:"watch_id,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Repo    string `json:"repo,omitempty"`
	PR      int    `json:"pr,omitempty"`
}

// CancelReviewWatchOutput is the output schema for cancel_copilot_review_watch.
type CancelReviewWatchOutput struct {
	Found     bool             `json:"found"`
	Watch     *ReviewWatchView `json:"watch,omitempty"`
	Cancelled *bool            `json:"cancelled,omitempty"`
	Note      string           `json:"note"`
}

var startWatchTool = &mcp.Tool{
	Name: "start_copilot_review_watch",
	Description: "推奨経路の開始点。Copilot review の background watch を開始し、即時 return する。" +
		"同一ユーザー・同一 PR の active watch があればそれを再利用する。" +
		"まず get_copilot_review_status で GitHub 上の即時 snapshot を確認し、未完了ならこの tool を使う。",
}

var getWatchStatusTool = &mcp.Tool{
	Name: "get_copilot_review_watch_status",
	Description: "background watch の現在状態をローカル state から返す cheap read。" +
		"watch_id を優先し、watch_id が無い場合は owner/repo/pr から同一ユーザーの最新 watch を引く。" +
		"recommended_next_action と next_poll_seconds を返すため、通知が弱い host の主経路として使える。",
}

var listWatchTool = &mcp.Tool{
	Name: "list_copilot_review_watches",
	Description: "同一ユーザーの active / recent watch 一覧を返す。" +
		"watch_id を見失った場合の回復や、人手デバッグ時の状況確認に使う。",
}

var cancelWatchTool = &mcp.Tool{
	Name: "cancel_copilot_review_watch",
	Description: "不要になった background watch を停止する。" +
		"watch_id を優先し、watch_id が無い場合は owner/repo/pr の active watch を対象にする。",
}

func startWatchHandler(
	manager *watch.Manager,
) func(context.Context, *mcp.CallToolRequest, StartReviewWatchInput) (*mcp.CallToolResult, StartReviewWatchOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in StartReviewWatchInput) (*mcp.CallToolResult, StartReviewWatchOutput, error) {
		if in.Owner == "" || in.Repo == "" || in.PR <= 0 {
			return nil, StartReviewWatchOutput{}, fmt.Errorf("owner, repo, and pr are required")
		}

		login := loginFromToolRequest(ctx, req)
		token := tokenFromToolRequest(ctx, req)
		if login == "" || token == "" {
			return nil, StartReviewWatchOutput{}, fmt.Errorf("authenticated GitHub login and token are required")
		}

		snapshot, reused, err := manager.Start(watch.StartInput{
			Login: login,
			Token: token,
			Owner: in.Owner,
			Repo:  in.Repo,
			PR:    in.PR,
		})
		if err != nil {
			return nil, StartReviewWatchOutput{}, err
		}

		out := buildStartWatchOutput(snapshot, reused, manager.PollInterval())
		if reused {
			out.Note = "既存の active watch を再利用しました。"
		} else {
			out.Note = "background watch を開始しました。"
		}
		return nil, out, nil
	}
}

func getWatchStatusHandler(
	manager *watch.Manager,
) func(context.Context, *mcp.CallToolRequest, GetReviewWatchStatusInput) (*mcp.CallToolResult, GetReviewWatchStatusOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in GetReviewWatchStatusInput) (*mcp.CallToolResult, GetReviewWatchStatusOutput, error) {
		login := loginFromToolRequest(ctx, req)
		if login == "" {
			return nil, GetReviewWatchStatusOutput{}, fmt.Errorf("authenticated GitHub login is required")
		}

		var (
			snapshot watch.Snapshot
			ok       bool
		)

		switch {
		case in.WatchID != "":
			snapshot, ok = manager.GetByID(in.WatchID)
			if ok && snapshot.Login != login {
				ok = false
			}
		case in.Owner != "" && in.Repo != "" && in.PR > 0:
			snapshot, ok = manager.GetLatest(login, in.Owner, in.Repo, in.PR)
		default:
			return nil, GetReviewWatchStatusOutput{}, fmt.Errorf("watch_id or owner, repo, and pr are required")
		}

		if !ok {
			return nil, GetReviewWatchStatusOutput{
				Found: false,
				Note:  "watch が見つかりませんでした。",
			}, nil
		}

		return nil, buildGetWatchStatusOutput(snapshot, manager.PollInterval()), nil
	}
}

func listWatchesHandler(
	manager *watch.Manager,
) func(context.Context, *mcp.CallToolRequest, ListReviewWatchesInput) (*mcp.CallToolResult, ListReviewWatchesOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in ListReviewWatchesInput) (*mcp.CallToolResult, ListReviewWatchesOutput, error) {
		login := loginFromToolRequest(ctx, req)
		if login == "" {
			return nil, ListReviewWatchesOutput{}, fmt.Errorf("authenticated GitHub login is required")
		}
		if in.Repo != "" && in.Owner == "" {
			return nil, ListReviewWatchesOutput{}, fmt.Errorf("owner is required when repo is specified")
		}
		if in.PR > 0 && (in.Owner == "" || in.Repo == "") {
			return nil, ListReviewWatchesOutput{}, fmt.Errorf("owner and repo are required when pr is specified")
		}

		limit := in.Limit
		if limit <= 0 {
			limit = defaultWatchListLimit
		}
		if limit > maxWatchListLimit {
			return nil, ListReviewWatchesOutput{}, fmt.Errorf("limit must not exceed %d", maxWatchListLimit)
		}

		snapshots, err := manager.List(login, watch.ListOptions{
			Owner:      in.Owner,
			Repo:       in.Repo,
			PR:         in.PR,
			ActiveOnly: in.ActiveOnly,
			Limit:      limit,
		})
		if err != nil {
			return nil, ListReviewWatchesOutput{}, err
		}

		now := time.Now().UTC()
		out := ListReviewWatchesOutput{
			Watches: make([]ReviewWatchView, 0, len(snapshots)),
			Count:   len(snapshots),
			Note:    "active watch を先頭に、updated_at の新しい順で返します。",
		}
		for _, snapshot := range snapshots {
			out.Watches = append(out.Watches, buildReviewWatchView(snapshot, manager.PollInterval(), now))
		}
		if len(out.Watches) == 0 {
			out.Note = "watch は見つかりませんでした。"
		}
		return nil, out, nil
	}
}

func cancelWatchHandler(
	manager *watch.Manager,
) func(context.Context, *mcp.CallToolRequest, CancelReviewWatchInput) (*mcp.CallToolResult, CancelReviewWatchOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in CancelReviewWatchInput) (*mcp.CallToolResult, CancelReviewWatchOutput, error) {
		login := loginFromToolRequest(ctx, req)
		if login == "" {
			return nil, CancelReviewWatchOutput{}, fmt.Errorf("authenticated GitHub login is required")
		}

		var (
			result watch.CancelResult
			err    error
		)

		switch {
		case in.WatchID != "":
			result, err = manager.CancelByID(login, in.WatchID)
		case in.Owner != "" && in.Repo != "" && in.PR > 0:
			result, err = manager.CancelLatest(login, in.Owner, in.Repo, in.PR)
		default:
			return nil, CancelReviewWatchOutput{}, fmt.Errorf("watch_id or owner, repo, and pr are required")
		}
		if err != nil {
			return nil, CancelReviewWatchOutput{}, err
		}
		if !result.Found {
			return nil, CancelReviewWatchOutput{
				Found: false,
				Note:  "watch が見つかりませんでした。",
			}, nil
		}
		return nil, buildCancelWatchOutput(result, manager.PollInterval()), nil
	}
}

// RegisterWatchTools adds the async review watch tools to the MCP server.
func RegisterWatchTools(server *mcp.Server, manager *watch.Manager) {
	mcp.AddTool(server, startWatchTool, startWatchHandler(manager))
	mcp.AddTool(server, getWatchStatusTool, getWatchStatusHandler(manager))
	mcp.AddTool(server, listWatchTool, listWatchesHandler(manager))
	mcp.AddTool(server, cancelWatchTool, cancelWatchHandler(manager))
}

func buildStartWatchOutput(snapshot watch.Snapshot, reused bool, pollInterval time.Duration) StartReviewWatchOutput {
	return StartReviewWatchOutput{
		ReviewWatchView: buildReviewWatchView(snapshot, pollInterval, time.Now().UTC()),
		Reused:          reused,
	}
}

func buildGetWatchStatusOutput(snapshot watch.Snapshot, pollInterval time.Duration) GetReviewWatchStatusOutput {
	view := buildReviewWatchView(snapshot, pollInterval, time.Now().UTC())
	return GetReviewWatchStatusOutput{
		Found: true,
		Watch: &view,
		Note:  "watch の現在状態です。",
	}
}

func buildCancelWatchOutput(result watch.CancelResult, pollInterval time.Duration) CancelReviewWatchOutput {
	view := buildReviewWatchView(result.Snapshot, pollInterval, time.Now().UTC())
	out := CancelReviewWatchOutput{
		Found: result.Found,
		Watch: &view,
	}
	out.Cancelled = boolPtr(result.Cancelled)
	switch {
	case result.Cancelled:
		out.Note = "watch を停止しました。"
	case result.Snapshot.Terminal:
		out.Note = "対象 watch は既に停止しています。"
	default:
		out.Note = "watch は見つかりましたが、現在の worker は停止しています。"
	}
	return out
}

func buildReviewWatchView(snapshot watch.Snapshot, pollInterval time.Duration, now time.Time) ReviewWatchView {
	recommendedAction, nextPollSeconds := deriveRecommendedNextAction(snapshot, pollInterval, now)

	out := ReviewWatchView{
		WatchID:               snapshot.WatchID,
		Owner:                 snapshot.Owner,
		Repo:                  snapshot.Repo,
		PR:                    snapshot.PR,
		ResourceURI:           snapshot.ResourceURI,
		WatchStatus:           string(snapshot.WatchStatus),
		RecommendedNextAction: recommendedAction,
		NextPollSeconds:       nextPollSeconds,
		Terminal:              snapshot.Terminal,
		WorkerRunning:         snapshot.WorkerRunning,
		PollsDone:             snapshot.PollsDone,
		StartedAt:             snapshot.StartedAt.UTC().Format(time.RFC3339),
		UpdatedAt:             snapshot.UpdatedAt.UTC().Format(time.RFC3339),
		LastError:             snapshot.LastError,
	}
	if snapshot.ReviewStatus != nil {
		status := string(*snapshot.ReviewStatus)
		out.ReviewStatus = &status
	}
	if snapshot.FailureReason != nil {
		reason := string(*snapshot.FailureReason)
		out.FailureReason = &reason
	}
	if snapshot.LastPolledAt != nil {
		ts := snapshot.LastPolledAt.UTC().Format(time.RFC3339)
		out.LastPolledAt = &ts
	}
	if snapshot.CompletedAt != nil {
		ts := snapshot.CompletedAt.UTC().Format(time.RFC3339)
		out.CompletedAt = &ts
	}
	if snapshot.StaleAt != nil {
		ts := snapshot.StaleAt.UTC().Format(time.RFC3339)
		out.StaleAt = &ts
	}
	if snapshot.RateLimitResetAt != nil {
		ts := snapshot.RateLimitResetAt.UTC().Format(time.RFC3339)
		out.RateLimitResetAt = &ts
	}
	return out
}

func deriveRecommendedNextAction(snapshot watch.Snapshot, pollInterval time.Duration, now time.Time) (string, *int) {
	switch snapshot.WatchStatus {
	case watch.StatusWatching:
		seconds := secondsUntilNextPoll(snapshot.LastPolledAt, pollInterval, now)
		return nextActionPollAfter, &seconds
	case watch.StatusCompleted, watch.StatusBlocked:
		return nextActionReadReviewThreads, nil
	case watch.StatusRateLimited:
		seconds := secondsUntilRateLimitReset(snapshot.RateLimitResetAt, pollInterval, now)
		return nextActionStartNewWatch, &seconds
	case watch.StatusCancelled, watch.StatusStale, watch.StatusTimeout:
		return nextActionStartNewWatch, nil
	case watch.StatusFailed:
		if snapshot.FailureReason != nil && *snapshot.FailureReason == watch.FailureReasonAuthExpired {
			return nextActionReauthAndStartNewWatch, nil
		}
		return nextActionCheckFailure, nil
	default:
		if snapshot.Terminal {
			return nextActionCheckFailure, nil
		}
		seconds := secondsUntilNextPoll(snapshot.LastPolledAt, pollInterval, now)
		return nextActionPollAfter, &seconds
	}
}

func secondsUntilNextPoll(lastPolledAt *time.Time, pollInterval time.Duration, now time.Time) int {
	if pollInterval <= 0 {
		pollInterval = 90 * time.Second
	}
	if lastPolledAt == nil {
		return 1
	}
	remaining := lastPolledAt.UTC().Add(pollInterval).Sub(now.UTC())
	if remaining <= 0 {
		return 1
	}
	return durationSecondsCeil(remaining)
}

func secondsUntilRateLimitReset(resetAt *time.Time, fallback time.Duration, now time.Time) int {
	if resetAt == nil {
		return durationSecondsCeil(fallback)
	}
	remaining := resetAt.UTC().Sub(now.UTC())
	if remaining <= 0 {
		return 1
	}
	return durationSecondsCeil(remaining)
}

func durationSecondsCeil(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func boolPtr(v bool) *bool {
	return &v
}

// RegisterWatchResources registers the watch resource template on the MCP server.
// Resources are accessible at copilot-review://watch/{watch_id} and return the
// full ReviewWatchView JSON of the specified watch. Clients may subscribe to
// receive resources/updated notifications whenever the watch state changes.
func RegisterWatchResources(server *mcp.Server, manager *watch.Manager) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "copilot-review://watch/{watch_id}",
		Name:        "Copilot Review Watch",
		Description: "Copilot レビュー watch の現在状態を JSON で返す MCP リソース。" +
			"watch_id を URI に埋め込んでアクセスする。状態変化時に resources/updated 通知が届く。",
		MIMEType: "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if req == nil || req.Params == nil {
			return nil, fmt.Errorf("missing read resource request params")
		}
		uri := req.Params.URI
		if uri == "" {
			return nil, fmt.Errorf("missing resource URI")
		}
		watchID, err := parseWatchIDFromURI(uri)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		login := middleware.LoginFromContext(ctx)
		if login == "" {
			return nil, fmt.Errorf("authenticated GitHub login is required")
		}
		snapshot, ok := manager.GetByID(watchID)
		if !ok || snapshot.Login != login {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		view := buildReviewWatchView(snapshot, manager.PollInterval(), time.Now().UTC())
		data, err := json.Marshal(view)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal watch view: %w", err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{URI: uri, Text: string(data), MIMEType: "application/json"},
			},
		}, nil
	})
}

// parseWatchIDFromURI extracts the watch ID from a copilot-review://watch/{id} URI.
func parseWatchIDFromURI(uri string) (string, error) {
	const prefix = "copilot-review://watch/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("invalid watch URI: %q", uri)
	}
	id := strings.TrimPrefix(uri, prefix)
	if id == "" || strings.ContainsAny(id, "/?#") {
		return "", fmt.Errorf("invalid watch ID in URI: %q", uri)
	}
	return id, nil
}
