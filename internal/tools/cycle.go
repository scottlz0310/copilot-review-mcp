package tools

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

// ─── Environment helpers ──────────────────────────────────────────────────────

const (
	defaultMaxCycles             = 3
	defaultNoCommentThresholdMin = 6
)

func envMaxCycles() int {
	if s := os.Getenv("MAX_REVIEW_CYCLES"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return defaultMaxCycles
}

func envNoCommentThreshold() int {
	if s := os.Getenv("NO_COMMENT_THRESHOLD_MIN"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return defaultNoCommentThresholdMin
}

// ─── Tool 8 Types ─────────────────────────────────────────────────────────────

// CycleStatusInput is the input schema for get_pr_review_cycle_status.
type CycleStatusInput struct {
	Owner         string  `json:"owner"`
	Repo          string  `json:"repo"`
	PR            int     `json:"pr"`
	LastCommentAt *string `json:"last_comment_at,omitempty"` // RFC3339 timestamp (ISO8601 subset) | null | omitted; auto-computed from threads when absent
	CyclesDone    int     `json:"cycles_done"`
	MaxCycles     int     `json:"max_cycles"` // 0 → use env/default
	FixType       string  `json:"fix_type"`   // logic | spec_change | trivial | none
}

// MergeConditions holds the per-condition merge readiness check.
type MergeConditions struct {
	CIOK            bool `json:"ci_ok"`
	UnresolvedCount int  `json:"unresolved_count"`
	AllReplied      bool `json:"all_replied"`
}

// CycleStatusOutput is the output schema for get_pr_review_cycle_status.
type CycleStatusOutput struct {
	CycleStatus       string          `json:"cycle_status"`       // CONTINUE | TERMINATE
	RecommendedAction string          `json:"recommended_action"` // WAIT | REPLY_RESOLVE | REQUEST_REREVIEW | READY_TO_MERGE | ESCALATE
	RereviewRequired  bool            `json:"rereview_required"`
	RereviewReason    *string         `json:"rereview_reason"`
	CyclesDone        int             `json:"cycles_done"`
	MaxCycles         int             `json:"max_cycles"`
	MergeConditions   MergeConditions `json:"merge_conditions"`
	Notes             []string        `json:"notes"`
}

// ─── Tool 8: get_pr_review_cycle_status ──────────────────────────────────────

var cycleTool = &mcp.Tool{
	Name: "get_pr_review_cycle_status",
	Description: "PR レビューサイクルの現在状態を評価し、次の推奨アクション " +
		"(WAIT / REPLY_RESOLVE / REQUEST_REREVIEW / READY_TO_MERGE / ESCALATE) を返す。" +
		"スレッドの blocking/non-blocking/suggestion 分類は呼び出し元 LLM がルールファイルに基づいて判断する。\n\n" +
		"【cycle_status の定義】\n" +
		"  TERMINATE = recommended_action が READY_TO_MERGE または ESCALATE の場合のみ。\n" +
		"  これらは \"次のサイクル不要\" を意味する終端アクション。\n" +
		"  終了条件 (terminateCond) が満たされても rereview_required=true 等の理由で\n" +
		"  recommended_action が READY_TO_MERGE でない場合は CONTINUE になることがある。",
}

func cycleStatusHandler(
	clientProvider githubClientProvider,
	db *store.DB,
) func(context.Context, *mcp.CallToolRequest, CycleStatusInput) (*mcp.CallToolResult, CycleStatusOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in CycleStatusInput) (*mcp.CallToolResult, CycleStatusOutput, error) {
		// Input validation
		if in.Owner == "" || in.Repo == "" || in.PR <= 0 {
			return nil, CycleStatusOutput{}, fmt.Errorf("owner, repo, and pr are required")
		}
		if in.CyclesDone < 0 {
			return nil, CycleStatusOutput{}, fmt.Errorf("cycles_done must be >= 0 (got %d)", in.CyclesDone)
		}
		if in.MaxCycles < 0 {
			return nil, CycleStatusOutput{}, fmt.Errorf("max_cycles must be >= 0 when specified (got %d)", in.MaxCycles)
		}
		validFixTypes := map[string]bool{
			"logic": true, "spec_change": true, "trivial": true, "none": true,
		}
		if in.FixType == "" {
			return nil, CycleStatusOutput{}, fmt.Errorf(
				`fix_type is required and must be one of: "logic", "spec_change", "trivial", "none"`,
			)
		}
		if !validFixTypes[in.FixType] {
			return nil, CycleStatusOutput{}, fmt.Errorf(
				"fix_type must be one of: logic, spec_change, trivial, none (got %q)", in.FixType,
			)
		}

		gh, err := clientProvider(ctx, req)
		if err != nil {
			return nil, CycleStatusOutput{}, err
		}

		// Resolve max_cycles (input → env → default)
		maxCycles := in.MaxCycles
		if maxCycles <= 0 {
			maxCycles = envMaxCycles()
		}
		noCommentThreshold := envNoCommentThreshold()

		// Determine rereview_required from fix_type
		rereviewRequired := in.FixType == "logic" || in.FixType == "spec_change"
		var rereviewReason *string
		if rereviewRequired {
			reason := fmt.Sprintf("fix_type=%q は再レビューが必要です", in.FixType)
			rereviewReason = &reason
		}

		// Auto-detect CI status early so all exit paths return accurate ci_ok.
		ciAllSuccess, err := gh.GetCIStatus(ctx, in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, CycleStatusOutput{}, fmt.Errorf("failed to get CI status: %w", err)
		}

		// ── Early exit: max cycles exceeded ────────────────────────────────
		if in.CyclesDone >= maxCycles {
			notes := []string{
				fmt.Sprintf("■ 最大サイクル数 %d 回に達しました。", maxCycles),
				fmt.Sprintf("■ PR: https://github.com/%s/%s/pull/%d", in.Owner, in.Repo, in.PR),
				"■ 人間によるレビューと判断が必要です。",
			}
			return nil, CycleStatusOutput{
				CycleStatus:       "TERMINATE",
				RecommendedAction: "ESCALATE",
				RereviewRequired:  rereviewRequired,
				RereviewReason:    rereviewReason,
				CyclesDone:        in.CyclesDone,
				MaxCycles:         maxCycles,
				MergeConditions:   MergeConditions{CIOK: ciAllSuccess},
				Notes:             notes,
			}, nil
		}

		// ── Fetch review threads ─────────────────────────────────────────────
		rawThreads, err := gh.GetReviewThreads(ctx, in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, CycleStatusOutput{}, fmt.Errorf("failed to fetch review threads: %w", err)
		}

		unresolvedCount := 0
		allReplied := true // vacuously true when there are no threads

		for _, t := range rawThreads {
			if !t.IsResolved {
				unresolvedCount++
			}
			// A thread is "replied" only when there are comments from
			// at least two distinct authors (for example, Copilot and a user).
			uniqueAuthors := make(map[string]struct{})
			for _, c := range t.Comments {
				authorKey := strings.TrimSpace(c.Author)
				if authorKey == "" {
					continue
				}
				uniqueAuthors[authorKey] = struct{}{}
			}
			if len(uniqueAuthors) < 2 {
				allReplied = false
			}
		}

		mergeConditions := MergeConditions{
			CIOK:            ciAllSuccess,
			UnresolvedCount: unresolvedCount,
			AllReplied:      allReplied,
		}

		// ── Fetch current review status ──────────────────────────────────────
		reviewData, err := gh.GetReviewData(ctx, in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, CycleStatusOutput{}, fmt.Errorf("failed to fetch review data: %w", err)
		}
		if reviewData.RateLimitRemaining < 10 {
			return nil, CycleStatusOutput{}, fmt.Errorf(
				"insufficient GitHub API rate limit remaining to safely derive review status: remaining=%d, retry-after=%s",
				reviewData.RateLimitRemaining,
				reviewData.RateLimitReset.UTC().Format(time.RFC3339),
			)
		}

		entry, err := db.GetLatest(in.Owner, in.Repo, in.PR)
		if err != nil {
			return nil, CycleStatusOutput{}, fmt.Errorf("failed to fetch trigger log: %w", err)
		}

		var requestedAt *time.Time
		var prevReviewID *string
		if entry != nil {
			requestedAt = &entry.RequestedAt
			prevReviewID = entry.PrevReviewID
		}
		reviewStatus := gh.DeriveStatus(reviewData, requestedAt, prevReviewID)

		// ── Elapsed time since last Copilot comment ───────────────────────────
		elapsedMinutes := 0
		if in.LastCommentAt != nil && *in.LastCommentAt != "" {
			lastAt, parseErr := time.Parse(time.RFC3339, *in.LastCommentAt)
			if parseErr != nil {
				return nil, CycleStatusOutput{}, fmt.Errorf("invalid last_comment_at: must be RFC3339: %w", parseErr)
			}
			elapsedMinutes = int(time.Since(lastAt).Minutes())
		} else if latest := findLatestCommentAt(rawThreads); latest != nil {
			// Auto-compute from thread comments when last_comment_at is not provided.
			elapsedMinutes = int(time.Since(*latest).Minutes())
		}

		// ── Termination condition checks (used for action and notes) ─────────
		// Condition 1: unresolved=0 and CI=OK.
		terminateCond1 := unresolvedCount == 0 && ciAllSuccess
		// Condition 2: no new Copilot comment for ≥ threshold minutes and CI=OK.
		terminateCond2 := elapsedMinutes >= noCommentThreshold && ciAllSuccess

		// ── Determine recommended_action ──────────────────────────────────────
		var recommendedAction string

		switch {
		case reviewStatus == ghclient.StatusNotRequested:
			// Guard: never mark an unreviewed PR as ready to merge.
			recommendedAction = "WAIT"

		case reviewStatus == ghclient.StatusPending || reviewStatus == ghclient.StatusInProgress:
			recommendedAction = "WAIT"

		case unresolvedCount > 0:
			// LLM determines blocking vs non-blocking from raw thread content per SKILL.md.
			recommendedAction = "REPLY_RESOLVE"

		case rereviewRequired:
			// Guard: previous review must be COMPLETED or BLOCKED, and all threads replied.
			reviewDone := reviewStatus == ghclient.StatusCompleted || reviewStatus == ghclient.StatusBlocked
			if reviewDone && allReplied {
				recommendedAction = "REQUEST_REREVIEW"
			} else {
				// Guards not met; reply/resolve remaining threads first.
				recommendedAction = "REPLY_RESOLVE"
			}

		case terminateCond1 || terminateCond2:
			recommendedAction = "READY_TO_MERGE"

		default:
			// Remaining case: blocking==0, unresolved==0, rereview not required,
			// but termination conditions not met (e.g. CI not yet green).
			// WAIT is the most conservative choice until the next cycle.
			recommendedAction = "WAIT"
		}

		// ── Derive cycle_status from recommended_action ───────────────────────
		// cycle_status=TERMINATE は「終端アクション」の場合のみ。
		// 終端アクション = READY_TO_MERGE または ESCALATE（次のサイクル不要）。
		// terminateCond1/2 は READY_TO_MERGE 到達のための入力条件であり、
		// 直接 cycle_status を制御しない。これにより cycle_status と
		// recommended_action が常に整合する（e.g. rereview 必要な場合は
		// terminateCond が満たされても CONTINUE のまま）。
		cycleStatus := "CONTINUE"
		if recommendedAction == "READY_TO_MERGE" || recommendedAction == "ESCALATE" {
			cycleStatus = "TERMINATE"
		}

		// ── Build notes ───────────────────────────────────────────────────────
		notes := []string{
			fmt.Sprintf("■ サイクル: %d/%d回目", in.CyclesDone+1, maxCycles),
			fmt.Sprintf("■ 未解決スレッド: %d件", unresolvedCount),
		}
		if in.FixType != "" {
			fixNote := fmt.Sprintf("■ 修正種別: %s", in.FixType)
			if rereviewRequired {
				fixNote += " → 再レビュー必須"
			} else {
				fixNote += " → 再レビュー不要"
			}
			notes = append(notes, fixNote)
		}

		if cycleStatus == "TERMINATE" {
			switch {
			case terminateCond1:
				notes = append(notes, "■ サイクル終了条件: 達成（unresolved=0, CI=OK）")
			default:
				notes = append(notes, fmt.Sprintf(
					"■ サイクル終了条件: 達成（コメントなし %d分経過 ≥ %d分閾値 & CI=OK）",
					elapsedMinutes, noCommentThreshold,
				))
			}
		} else {
			var reasons []string
			if terminateCond1 || terminateCond2 {
				// Termination conditions are already met, but the cycle continues
				// due to a higher-priority action (e.g. rereview required, PENDING status).
				reasons = append(reasons, "終了条件は達成済み")
				switch {
				case rereviewRequired && rereviewReason != nil:
					reasons = append(reasons, fmt.Sprintf("継続理由: 再レビュー必須（%s）", *rereviewReason))
				case rereviewRequired:
					reasons = append(reasons, "継続理由: 再レビュー必須")
				default:
					reasons = append(reasons, fmt.Sprintf("継続理由: 推奨アクション=%s", recommendedAction))
				}
			} else {
				if unresolvedCount > 0 {
					reasons = append(reasons, fmt.Sprintf("unresolved=%d残存", unresolvedCount))
				}
				if !ciAllSuccess {
					reasons = append(reasons, "CI未達成")
				}
				if len(reasons) == 0 {
					reasons = append(reasons, "条件評価中")
				}
			}
			notes = append(notes, fmt.Sprintf("■ サイクル終了条件: 未達成（%s）", strings.Join(reasons, ", ")))
		}
		notes = append(notes, fmt.Sprintf("■ 推奨アクション: %s", recommendedAction))

		return nil, CycleStatusOutput{
			CycleStatus:       cycleStatus,
			RecommendedAction: recommendedAction,
			RereviewRequired:  rereviewRequired,
			RereviewReason:    rereviewReason,
			CyclesDone:        in.CyclesDone,
			MaxCycles:         maxCycles,
			MergeConditions:   mergeConditions,
			Notes:             notes,
		}, nil
	}
}

// RegisterCycleTool adds get_pr_review_cycle_status to the MCP server.
func RegisterCycleTool(server *mcp.Server, clientProvider githubClientProvider, db *store.DB) {
	mcp.AddTool(server, cycleTool, cycleStatusHandler(clientProvider, db))
}

// findLatestCommentAt returns the most recent CreatedAt across Copilot-authored
// thread comments, or nil when no such comments exist.
func findLatestCommentAt(threads []ghclient.ReviewThread) *time.Time {
	var latest time.Time
	for _, t := range threads {
		for _, c := range t.Comments {
			if !ghclient.IsCopilotLogin(c.Author) {
				continue
			}
			ts, err := time.Parse(time.RFC3339, c.CreatedAt)
			if err == nil && ts.After(latest) {
				latest = ts
			}
		}
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}
