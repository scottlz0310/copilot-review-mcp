package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottlz0310/copilot-review-mcp/internal/autherr"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

func TestCycleStatusHandlerAuthRequired(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cycle-auth-required.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := cycleStatusHandler(errorProvider(autherr.NewAuthRequired()), db)
	result, _, err := handler(context.Background(), nil, CycleStatusInput{
		Owner:      "o",
		Repo:       "r",
		PR:         1,
		CyclesDone: 0,
		MaxCycles:  3,
		FixType:    "none",
	})
	assertAuthResult(t, result, err, autherr.AUTH_REQUIRED)
}

func TestCycleStatusHandlerReauthRequired(t *testing.T) {
	srv := new401Server()
	t.Cleanup(srv.Close)

	db, err := store.Open(filepath.Join(t.TempDir(), "cycle-reauth-required.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := cycleStatusHandler(staticProvider(make401GitHubClient(srv)), db)
	result, _, err := handler(context.Background(), nil, CycleStatusInput{
		Owner:      "o",
		Repo:       "r",
		PR:         1,
		CyclesDone: 0,
		MaxCycles:  3,
		FixType:    "none",
	})
	assertAuthResult(t, result, err, autherr.REAUTH_REQUIRED)
}
