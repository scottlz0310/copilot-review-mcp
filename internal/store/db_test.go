package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewWatchActiveUniqueConstraint(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-unique.db"))

	startedAt := time.Now().UTC().Truncate(time.Second)
	first := ReviewWatchEntry{
		ID:          "cw_first",
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		PR:          42,
		WatchStatus: "WATCHING",
		IsActive:    true,
		StartedAt:   startedAt,
		UpdatedAt:   startedAt,
	}
	if err := db.UpsertReviewWatch(first); err != nil {
		t.Fatalf("UpsertReviewWatch(first) error = %v", err)
	}

	second := first
	second.ID = "cw_second"
	second.UpdatedAt = startedAt.Add(time.Minute)
	if err := db.UpsertReviewWatch(second); err == nil {
		t.Fatal("UpsertReviewWatch(second) = nil error, want unique constraint failure")
	}

	first.IsActive = false
	first.WatchStatus = "COMPLETED"
	completedAt := startedAt.Add(2 * time.Minute)
	first.CompletedAt = &completedAt
	first.UpdatedAt = completedAt
	if err := db.UpsertReviewWatch(first); err != nil {
		t.Fatalf("UpsertReviewWatch(first inactive) error = %v", err)
	}
	if err := db.UpsertReviewWatch(second); err != nil {
		t.Fatalf("UpsertReviewWatch(second after deactivation) error = %v", err)
	}
}

func TestUpsertReviewWatchKeepsImmutableIdentityFields(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-upsert-identity.db"))

	startedAt := time.Now().UTC().Truncate(time.Second)
	updatedAt := startedAt.Add(time.Minute)
	first := ReviewWatchEntry{
		ID:          "cw_identity",
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		PR:          42,
		WatchStatus: "WATCHING",
		IsActive:    true,
		StartedAt:   startedAt,
		UpdatedAt:   startedAt,
	}
	if err := db.UpsertReviewWatch(first); err != nil {
		t.Fatalf("UpsertReviewWatch(first) error = %v", err)
	}

	lastError := "persisted update"
	reviewStatus := "COMPLETED"
	updated := ReviewWatchEntry{
		ID:           first.ID,
		GitHubLogin:  "bob",
		Owner:        "other-owner",
		Repo:         "other-repo",
		PR:           99,
		WatchStatus:  "COMPLETED",
		ReviewStatus: &reviewStatus,
		IsActive:     false,
		StartedAt:    startedAt.Add(24 * time.Hour),
		UpdatedAt:    updatedAt,
		LastError:    &lastError,
	}
	if err := db.UpsertReviewWatch(updated); err != nil {
		t.Fatalf("UpsertReviewWatch(updated) error = %v", err)
	}

	got, err := db.GetReviewWatchByID(first.ID)
	if err != nil {
		t.Fatalf("GetReviewWatchByID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetReviewWatchByID() = nil, want row")
	}
	if got.GitHubLogin != first.GitHubLogin {
		t.Fatalf("GitHubLogin = %q, want %q", got.GitHubLogin, first.GitHubLogin)
	}
	if got.Owner != first.Owner {
		t.Fatalf("Owner = %q, want %q", got.Owner, first.Owner)
	}
	if got.Repo != first.Repo {
		t.Fatalf("Repo = %q, want %q", got.Repo, first.Repo)
	}
	if got.PR != first.PR {
		t.Fatalf("PR = %d, want %d", got.PR, first.PR)
	}
	if !got.StartedAt.Equal(first.StartedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, first.StartedAt)
	}
	if got.WatchStatus != updated.WatchStatus {
		t.Fatalf("WatchStatus = %q, want %q", got.WatchStatus, updated.WatchStatus)
	}
	if got.UpdatedAt != updatedAt {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, updatedAt)
	}
	if got.LastError == nil || *got.LastError != lastError {
		t.Fatalf("LastError = %v, want %q", got.LastError, lastError)
	}
	if got.IsActive {
		t.Fatal("IsActive = true, want false")
	}
}

func TestGetLatestReviewWatchReturnsNewestSnapshot(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-latest.db"))

	base := time.Now().UTC().Truncate(time.Second)
	first := ReviewWatchEntry{
		ID:          "cw_old",
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		PR:          7,
		WatchStatus: "COMPLETED",
		IsActive:    false,
		StartedAt:   base,
		UpdatedAt:   base,
	}
	if err := db.UpsertReviewWatch(first); err != nil {
		t.Fatalf("UpsertReviewWatch(first) error = %v", err)
	}

	lastError := "auth expired"
	reviewStatus := "PENDING"
	failureReason := "AUTH_EXPIRED"
	second := ReviewWatchEntry{
		ID:            "cw_new",
		GitHubLogin:   "alice",
		Owner:         "octo",
		Repo:          "demo",
		PR:            7,
		WatchStatus:   "FAILED",
		ReviewStatus:  &reviewStatus,
		FailureReason: &failureReason,
		IsActive:      false,
		StartedAt:     base.Add(time.Minute),
		UpdatedAt:     base.Add(2 * time.Minute),
		LastError:     &lastError,
	}
	if err := db.UpsertReviewWatch(second); err != nil {
		t.Fatalf("UpsertReviewWatch(second) error = %v", err)
	}

	got, err := db.GetLatestReviewWatch("alice", "octo", "demo", 7)
	if err != nil {
		t.Fatalf("GetLatestReviewWatch() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetLatestReviewWatch() = nil, want entry")
	}
	if got.ID != second.ID {
		t.Fatalf("GetLatestReviewWatch().ID = %q, want %q", got.ID, second.ID)
	}
	if got.LastError == nil || *got.LastError != lastError {
		t.Fatalf("GetLatestReviewWatch().LastError = %v, want %q", got.LastError, lastError)
	}
}

func TestGetLatestReviewWatchIgnoresLexicographicIDTies(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-latest-tie.db"))

	base := time.Now().UTC().Truncate(time.Second)
	first := ReviewWatchEntry{
		ID:          "cw_100_2",
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		PR:          8,
		WatchStatus: "COMPLETED",
		IsActive:    false,
		StartedAt:   base,
		UpdatedAt:   base,
	}
	if err := db.UpsertReviewWatch(first); err != nil {
		t.Fatalf("UpsertReviewWatch(first) error = %v", err)
	}

	second := first
	second.ID = "cw_100_10"
	second.LastError = strPtr("newer insert")
	if err := db.UpsertReviewWatch(second); err != nil {
		t.Fatalf("UpsertReviewWatch(second) error = %v", err)
	}

	got, err := db.GetLatestReviewWatch("alice", "octo", "demo", 8)
	if err != nil {
		t.Fatalf("GetLatestReviewWatch() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetLatestReviewWatch() = nil, want entry")
	}
	if got.ID != second.ID {
		t.Fatalf("GetLatestReviewWatch().ID = %q, want %q", got.ID, second.ID)
	}
}

func TestListReviewWatchesOrdersActiveThenRecentAndFilters(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-list.db"))

	base := time.Now().UTC().Truncate(time.Second)
	entries := []ReviewWatchEntry{
		{
			ID:          "cw_terminal_recent",
			GitHubLogin: "alice",
			Owner:       "octo",
			Repo:        "demo",
			PR:          11,
			WatchStatus: "COMPLETED",
			IsActive:    false,
			StartedAt:   base,
			UpdatedAt:   base.Add(4 * time.Minute),
		},
		{
			ID:          "cw_active",
			GitHubLogin: "alice",
			Owner:       "octo",
			Repo:        "demo",
			PR:          10,
			WatchStatus: "WATCHING",
			IsActive:    true,
			StartedAt:   base.Add(time.Minute),
			UpdatedAt:   base.Add(3 * time.Minute),
		},
		{
			ID:          "cw_terminal_old",
			GitHubLogin: "alice",
			Owner:       "octo",
			Repo:        "demo",
			PR:          12,
			WatchStatus: "FAILED",
			IsActive:    false,
			StartedAt:   base.Add(2 * time.Minute),
			UpdatedAt:   base.Add(2 * time.Minute),
		},
		{
			ID:          "cw_other_user",
			GitHubLogin: "bob",
			Owner:       "octo",
			Repo:        "demo",
			PR:          13,
			WatchStatus: "WATCHING",
			IsActive:    true,
			StartedAt:   base,
			UpdatedAt:   base.Add(5 * time.Minute),
		},
	}
	for _, entry := range entries {
		if err := db.UpsertReviewWatch(entry); err != nil {
			t.Fatalf("UpsertReviewWatch(%q) error = %v", entry.ID, err)
		}
	}

	got, err := db.ListReviewWatches(ReviewWatchFilter{
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListReviewWatches() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(ListReviewWatches()) = %d, want 3", len(got))
	}
	if got[0].ID != "cw_active" {
		t.Fatalf("got[0].ID = %q, want active watch first", got[0].ID)
	}
	if got[1].ID != "cw_terminal_recent" {
		t.Fatalf("got[1].ID = %q, want newest terminal watch second", got[1].ID)
	}
	if got[2].ID != "cw_terminal_old" {
		t.Fatalf("got[2].ID = %q, want oldest terminal watch last", got[2].ID)
	}

	activeOnly, err := db.ListReviewWatches(ReviewWatchFilter{
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		ActiveOnly:  true,
	})
	if err != nil {
		t.Fatalf("ListReviewWatches(active_only) error = %v", err)
	}
	if len(activeOnly) != 1 || activeOnly[0].ID != "cw_active" {
		t.Fatalf("active_only result = %+v, want only cw_active", activeOnly)
	}
}

func TestOpenMarksActiveReviewWatchesStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review-watch-open.db")
	db := openTestDB(t, path)

	startedAt := time.Now().UTC().Truncate(time.Second)
	entry := ReviewWatchEntry{
		ID:          "cw_active",
		GitHubLogin: "alice",
		Owner:       "octo",
		Repo:        "demo",
		PR:          99,
		WatchStatus: "WATCHING",
		IsActive:    true,
		StartedAt:   startedAt,
		UpdatedAt:   startedAt,
	}
	if err := db.UpsertReviewWatch(entry); err != nil {
		t.Fatalf("UpsertReviewWatch() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("store.Open(reopen) error = %v", err)
	}
	defer reopened.Close()

	got, err := reopened.GetReviewWatchByID(entry.ID)
	if err != nil {
		t.Fatalf("GetReviewWatchByID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetReviewWatchByID() = nil, want entry")
	}
	if got.WatchStatus != "STALE" {
		t.Fatalf("WatchStatus = %q, want %q", got.WatchStatus, "STALE")
	}
	if got.IsActive {
		t.Fatal("IsActive = true, want false")
	}
	if got.StaleAt == nil {
		t.Fatal("StaleAt = nil, want timestamp")
	}
	if got.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want timestamp")
	}
	if got.LastError == nil || !strings.Contains(*got.LastError, staleOnOpenMessage) {
		t.Fatalf("LastError = %v, want message containing %q", got.LastError, staleOnOpenMessage)
	}
}

func TestOpenRebuildsReviewWatchLookupIndex(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "review-watch-index.db"))

	var sqlText string
	err := db.db.QueryRow(
		`SELECT sql
		   FROM sqlite_master
		  WHERE type = 'index' AND name = 'idx_review_watch_lookup'`,
	).Scan(&sqlText)
	if err != nil {
		t.Fatalf("index lookup error = %v", err)
	}
	if !strings.Contains(sqlText, "updated_at DESC, started_at DESC") {
		t.Fatalf("index SQL = %q, want updated_at/started_at ordering", sqlText)
	}
	if strings.Contains(sqlText, "id DESC") {
		t.Fatalf("index SQL = %q, want id ordering removed", sqlText)
	}
}

func TestInsertWithTimePersistsAtEpochSecondPrecision(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "trigger-insert-with-time.db"))

	// Insert with sub-second precision; stored epoch-second should truncate downward.
	base := time.Now().UTC().Truncate(time.Second)
	if _, err := db.InsertWithTime("octo", "demo", 1, "MANUAL", base.Add(500*time.Millisecond)); err != nil {
		t.Fatalf("InsertWithTime() error = %v", err)
	}

	entry, err := db.GetLatest("octo", "demo", 1)
	if err != nil {
		t.Fatalf("GetLatest() error = %v", err)
	}
	if entry == nil {
		t.Fatal("GetLatest() = nil, want entry")
	}
	if !entry.RequestedAt.Equal(base) {
		t.Fatalf("RequestedAt = %v, want %v (epoch-second truncation)", entry.RequestedAt, base)
	}
}

func TestInsertWithTimeNewerRowWinsGetLatest(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "trigger-insert-ordering.db"))

	base := time.Now().UTC().Truncate(time.Second)
	older := base.Add(-time.Minute)
	newer := base

	// Insert the older entry first.
	if _, err := db.InsertWithTime("octo", "demo", 2, "MANUAL", older); err != nil {
		t.Fatalf("InsertWithTime(older) error = %v", err)
	}
	// Mark it completed so HasPending = false.
	olderEntry, err := db.GetLatest("octo", "demo", 2)
	if err != nil || olderEntry == nil {
		t.Fatalf("GetLatest(older) err = %v, entry = %v", err, olderEntry)
	}
	if err := db.UpdateCompletedAt(olderEntry.ID); err != nil {
		t.Fatalf("UpdateCompletedAt() error = %v", err)
	}

	// Insert a newer pending entry.
	if _, err := db.InsertWithTime("octo", "demo", 2, "MANUAL", newer); err != nil {
		t.Fatalf("InsertWithTime(newer) error = %v", err)
	}

	// GetLatest must return the newer (pending) row.
	entry, err := db.GetLatest("octo", "demo", 2)
	if err != nil {
		t.Fatalf("GetLatest() error = %v", err)
	}
	if entry == nil {
		t.Fatal("GetLatest() = nil, want newer entry")
	}
	if !entry.RequestedAt.Equal(newer) {
		t.Fatalf("RequestedAt = %v, want %v (newer entry)", entry.RequestedAt, newer)
	}
	if entry.CompletedAt != nil {
		t.Fatal("CompletedAt = non-nil, want nil (newer entry is pending)")
	}
}

func openTestDB(t *testing.T, path string) *DB {
	t.Helper()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func strPtr(s string) *string { return &s }
