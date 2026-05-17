package tools

import (
	"testing"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
)

func TestComputeAllReplied(t *testing.T) {
	comment := func(author string) ghclient.ThreadComment {
		return ghclient.ThreadComment{Author: author, Body: "body"}
	}

	tests := []struct {
		name             string
		threads          []ghclient.ReviewThread
		wantAllReplied   bool
		wantUnreplied    []string
	}{
		{
			name:           "no threads → vacuously true",
			threads:        nil,
			wantAllReplied: true,
			wantUnreplied:  nil,
		},
		{
			name: "only resolved threads → true (resolved threads skipped)",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: true, Comments: []ghclient.ThreadComment{comment("copilot[bot]")}},
				{ID: "T2", IsResolved: true, Comments: []ghclient.ThreadComment{comment("copilot[bot]")}},
			},
			wantAllReplied: true,
			wantUnreplied:  nil,
		},
		{
			name: "unresolved thread with 2 authors → true",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: false, Comments: []ghclient.ThreadComment{
					comment("copilot[bot]"),
					comment("alice"),
				}},
			},
			wantAllReplied: true,
			wantUnreplied:  nil,
		},
		{
			name: "unresolved thread with 1 author → false",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: false, Comments: []ghclient.ThreadComment{
					comment("copilot[bot]"),
				}},
			},
			wantAllReplied: false,
			wantUnreplied:  []string{"T1"},
		},
		{
			name: "resolved thread (1 author) does not block all_replied",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: true, Comments: []ghclient.ThreadComment{comment("copilot[bot]")}},
				{ID: "T2", IsResolved: false, Comments: []ghclient.ThreadComment{
					comment("copilot[bot]"),
					comment("alice"),
				}},
			},
			wantAllReplied: true,
			wantUnreplied:  nil,
		},
		{
			name: "mixed: resolved (1 author) + unresolved (1 author) → false, only unresolved in unreplied",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: true, Comments: []ghclient.ThreadComment{comment("copilot[bot]")}},
				{ID: "T2", IsResolved: false, Comments: []ghclient.ThreadComment{comment("copilot[bot]")}},
			},
			wantAllReplied: false,
			wantUnreplied:  []string{"T2"},
		},
		{
			name: "multiple unresolved unreplied threads → all listed",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: false, Comments: []ghclient.ThreadComment{comment("bot")}},
				{ID: "T2", IsResolved: false, Comments: []ghclient.ThreadComment{comment("bot"), comment("user")}},
				{ID: "T3", IsResolved: false, Comments: []ghclient.ThreadComment{comment("bot")}},
			},
			wantAllReplied: false,
			wantUnreplied:  []string{"T1", "T3"},
		},
		{
			name: "empty author trimmed and not counted",
			threads: []ghclient.ReviewThread{
				{ID: "T1", IsResolved: false, Comments: []ghclient.ThreadComment{
					{Author: "   ", Body: "noise"},
					{Author: "bot", Body: "comment"},
				}},
			},
			wantAllReplied: false,
			wantUnreplied:  []string{"T1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAllReplied, gotUnreplied := computeAllReplied(tt.threads)
			if gotAllReplied != tt.wantAllReplied {
				t.Errorf("allReplied = %v, want %v", gotAllReplied, tt.wantAllReplied)
			}
			if len(gotUnreplied) != len(tt.wantUnreplied) {
				t.Errorf("unreplied = %v, want %v", gotUnreplied, tt.wantUnreplied)
				return
			}
			for i, id := range tt.wantUnreplied {
				if gotUnreplied[i] != id {
					t.Errorf("unreplied[%d] = %q, want %q", i, gotUnreplied[i], id)
				}
			}
		})
	}
}
