package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v72/github"

	ghclient "github.com/scottlz0310/copilot-review-mcp/internal/github"
	"github.com/scottlz0310/copilot-review-mcp/internal/store"
)

const defaultPollInterval = 90 * time.Second
const defaultPollTimeout = 30 * time.Second
const defaultMaxWatchDuration = 2 * time.Hour

// Status is the lifecycle state of a background review watch.
type Status string

const (
	StatusWatching    Status = "WATCHING"
	StatusCompleted   Status = "COMPLETED"
	StatusBlocked     Status = "BLOCKED"
	StatusTimeout     Status = "TIMEOUT"
	StatusRateLimited Status = "RATE_LIMITED"
	StatusFailed      Status = "FAILED"
	StatusStale       Status = "STALE"
	StatusCancelled   Status = "CANCELLED"
)

// FailureReason describes why a watch entered FAILED.
type FailureReason string

const (
	FailureReasonAuthExpired FailureReason = "AUTH_EXPIRED"
	FailureReasonGitHubError FailureReason = "GITHUB_ERROR"
	FailureReasonInternal    FailureReason = "INTERNAL_ERROR"
)

// Snapshot is the externally visible state of a watch at a point in time.
type Snapshot struct {
	WatchID          string
	Login            string
	Owner            string
	Repo             string
	PR               int
	ResourceURI      *string
	WatchStatus      Status
	ReviewStatus     *ghclient.ReviewStatus
	FailureReason    *FailureReason
	Terminal         bool
	WorkerRunning    bool
	PollsDone        int
	StartedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
	StaleAt          *time.Time
	LastPolledAt     *time.Time
	RateLimitResetAt *time.Time
	LastError        *string
}

// StartInput identifies a PR watch owned by one authenticated GitHub user.
type StartInput struct {
	Login string
	Token string
	Owner string
	Repo  string
	PR    int
}

// ReviewDataFetcher fetches the GitHub snapshot needed to update watch state.
type ReviewDataFetcher interface {
	GetReviewData(ctx context.Context, owner, repo string, prNumber int) (*ghclient.ReviewData, error)
}

// Options configures the watch manager.
type Options struct {
	PollInterval     time.Duration
	PollTimeout      time.Duration
	MaxWatchDuration time.Duration
	Threshold        time.Duration
	InvalidateToken  func(string)
	ClientFactory    func(ctx context.Context, token string) ReviewDataFetcher
	Now              func() time.Time
	// NotifyResourceUpdated is called asynchronously whenever a watch transitions
	// state. The uri argument is the resource URI of the changed watch
	// (e.g. "copilot-review://watch/{id}"). It is safe to call srv.ResourceUpdated
	// from this callback.
	NotifyResourceUpdated func(uri string)
}

// CancelResult reports the outcome of a manual watch cancellation request.
type CancelResult struct {
	Snapshot  Snapshot
	Found     bool
	Cancelled bool
}

// ListOptions scopes a watch listing query.
type ListOptions struct {
	Owner      string
	Repo       string
	PR         int
	ActiveOnly bool
	Limit      int
}

type watchStore interface {
	GetLatest(owner, repo string, pr int) (*store.TriggerEntry, error)
	UpdateCompletedAt(id int64) error
	GetReviewWatchByID(id string) (*store.ReviewWatchEntry, error)
	GetLatestReviewWatch(login, owner, repo string, pr int) (*store.ReviewWatchEntry, error)
	ListReviewWatches(filter store.ReviewWatchFilter) ([]store.ReviewWatchEntry, error)
	UpsertReviewWatch(entry store.ReviewWatchEntry) error
}

// Manager owns background review-watch workers for the current server process.
type Manager struct {
	db                    watchStore
	threshold             time.Duration
	pollInterval          time.Duration
	pollTimeout           time.Duration
	maxWatchDuration      time.Duration
	notifyResourceUpdated func(uri string)
	clientFactory         func(ctx context.Context, token string) ReviewDataFetcher
	now                   func() time.Time
	ctx                   context.Context
	cancel                context.CancelFunc
	idSeq                 atomic.Uint64
	mu                    sync.RWMutex
	watchesByID           map[string]*watchState
	activeByKey           map[watchKey]string
	latestByKey           map[watchKey]string
	closed                bool
}

type watchKey struct {
	login string
	owner string
	repo  string
	pr    int
}

type watchState struct {
	id               string
	key              watchKey
	triggerLogID     *int64
	resourceURI      *string
	token            string
	ctx              context.Context
	cancel           context.CancelFunc
	clientMu         sync.RWMutex
	client           ReviewDataFetcher
	status           Status
	reviewStatus     *ghclient.ReviewStatus
	failureReason    *FailureReason
	terminal         bool
	workerRunning    bool
	pollsDone        int
	startedAt        time.Time
	updatedAt        time.Time
	completedAt      *time.Time
	staleAt          *time.Time
	lastPolledAt     *time.Time
	lastError        *string
	rateLimitResetAt *time.Time
}

// NewManager creates a process-local, memory-only watch manager.
func NewManager(db watchStore, opts Options) *Manager {
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	pollTimeout := opts.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	maxWatchDuration := opts.MaxWatchDuration
	if maxWatchDuration <= 0 {
		maxWatchDuration = defaultMaxWatchDuration
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	clientFactory := opts.ClientFactory
	if clientFactory == nil {
		clientFactory = func(ctx context.Context, token string) ReviewDataFetcher {
			return ghclient.NewClient(ctx, token, opts.Threshold, opts.InvalidateToken)
		}
	}
	return &Manager{
		db:                    db,
		threshold:             opts.Threshold,
		pollInterval:          pollInterval,
		pollTimeout:           pollTimeout,
		maxWatchDuration:      maxWatchDuration,
		notifyResourceUpdated: opts.NotifyResourceUpdated,
		clientFactory:         clientFactory,
		now:                   now,
		ctx:                   ctx,
		cancel:                cancel,
		watchesByID:           make(map[string]*watchState),
		activeByKey:           make(map[watchKey]string),
		latestByKey:           make(map[watchKey]string),
	}
}

// Close cancels all active workers and marks them as STALE.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	now := m.now().UTC()
	watches := make([]*watchState, 0, len(m.activeByKey))
	for key, id := range m.activeByKey {
		w := m.watchesByID[id]
		if w == nil || w.terminal {
			delete(m.activeByKey, key)
			continue
		}
		w.status = StatusStale
		w.terminal = true
		w.workerRunning = false
		w.updatedAt = now
		w.completedAt = timePtr(now)
		w.staleAt = timePtr(now)
		w.token = ""
		w.clientMu.Lock()
		w.client = nil
		w.clientMu.Unlock()
		errText := "watch manager closed before the watch could finish"
		w.lastError = &errText
		_ = m.persistOrDegradeLocked(w, StatusStale, now)
		watches = append(watches, w)
		delete(m.activeByKey, key)
	}
	m.mu.Unlock()

	for _, w := range watches {
		w.cancel()
	}
	m.cancel()
}

// Start begins a new background watch or reuses the current active watch for the same user/PR.
func (m *Manager) Start(in StartInput) (Snapshot, bool, error) {
	if in.Login == "" || in.Token == "" || in.Owner == "" || in.Repo == "" || in.PR <= 0 {
		return Snapshot{}, false, fmt.Errorf("login, token, owner, repo, and pr are required")
	}

	var triggerLogID *int64
	if m.db != nil {
		entry, err := m.db.GetLatest(in.Owner, in.Repo, in.PR)
		if err != nil {
			return Snapshot{}, false, fmt.Errorf("failed to read trigger_log: %w", err)
		}
		if entry != nil {
			id := entry.ID
			triggerLogID = &id
		}
	}

	key := watchKey{
		login: in.Login,
		owner: in.Owner,
		repo:  in.Repo,
		pr:    in.PR,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return Snapshot{}, false, fmt.Errorf("watch manager is closed")
	}

	if id, ok := m.activeByKey[key]; ok {
		if existing := m.watchesByID[id]; existing != nil && !existing.terminal {
			tokenChanged := existing.token != in.Token
			triggerLinked := existing.triggerLogID == nil && triggerLogID != nil
			if tokenChanged || triggerLinked {
				existing.updatedAt = m.now().UTC()
			}
			if tokenChanged {
				existing.token = in.Token
				existing.clientMu.Lock()
				existing.client = m.clientFactory(existing.ctx, in.Token)
				existing.clientMu.Unlock()
			}
			if triggerLinked {
				existing.triggerLogID = cloneInt64Ptr(triggerLogID)
			}
			if tokenChanged || triggerLinked {
				if err := m.persistLocked(existing); err != nil {
					return Snapshot{}, false, fmt.Errorf("failed to persist review_watch: %w", err)
				}
			}
			return snapshotFromState(existing), true, nil
		}
		delete(m.activeByKey, key)
	}

	now := m.now().UTC()
	watchCtx, cancel := context.WithCancel(m.ctx)
	id := m.nextID()
	resourceURI := resourceURIForWatch(id)
	state := &watchState{
		id:            id,
		key:           key,
		triggerLogID:  cloneInt64Ptr(triggerLogID),
		resourceURI:   stringPtr(resourceURI),
		token:         in.Token,
		ctx:           watchCtx,
		cancel:        cancel,
		client:        m.clientFactory(watchCtx, in.Token),
		status:        StatusWatching,
		workerRunning: true,
		startedAt:     now,
		updatedAt:     now,
	}
	if err := m.persistLocked(state); err != nil {
		cancel()
		return Snapshot{}, false, fmt.Errorf("failed to persist review_watch: %w", err)
	}
	m.watchesByID[id] = state
	m.activeByKey[key] = id
	m.latestByKey[key] = id

	go m.run(state)

	return snapshotFromState(state), false, nil
}

// GetByID returns the latest snapshot for a watch ID.
func (m *Manager) GetByID(watchID string) (Snapshot, bool) {
	m.mu.RLock()
	w := m.watchesByID[watchID]
	if w != nil {
		snapshot := snapshotFromState(w)
		m.mu.RUnlock()
		return snapshot, true
	}
	m.mu.RUnlock()

	if m.db == nil {
		return Snapshot{}, false
	}
	entry, err := m.db.GetReviewWatchByID(watchID)
	if err != nil {
		slog.Warn("failed to load persisted review_watch by id", "watch_id", watchID, "err", err)
		return Snapshot{}, false
	}
	if entry == nil {
		return Snapshot{}, false
	}
	return snapshotFromReviewWatchEntry(entry), true
}

// GetLatest returns the latest watch snapshot for a given user/PR key.
func (m *Manager) GetLatest(login, owner, repo string, pr int) (Snapshot, bool) {
	key := watchKey{login: login, owner: owner, repo: repo, pr: pr}
	m.mu.RLock()
	id, ok := m.latestByKey[key]
	if ok {
		w := m.watchesByID[id]
		if w != nil {
			snapshot := snapshotFromState(w)
			m.mu.RUnlock()
			return snapshot, true
		}
	}
	m.mu.RUnlock()

	if m.db == nil {
		return Snapshot{}, false
	}
	entry, err := m.db.GetLatestReviewWatch(login, owner, repo, pr)
	if err != nil {
		slog.Warn(
			"failed to load latest persisted review_watch",
			"login", login,
			"owner", owner,
			"repo", repo,
			"pr", pr,
			"err", err,
		)
		return Snapshot{}, false
	}
	if entry == nil {
		return Snapshot{}, false
	}
	return snapshotFromReviewWatchEntry(entry), true
}

// List returns active and/or recent watch snapshots for one GitHub login.
func (m *Manager) List(login string, opts ListOptions) ([]Snapshot, error) {
	if login == "" {
		return nil, fmt.Errorf("login is required")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	byID := make(map[string]Snapshot)

	m.mu.RLock()
	for _, state := range m.watchesByID {
		if state == nil || state.key.login != login {
			continue
		}
		snapshot := snapshotFromState(state)
		if !matchesListOptions(snapshot, opts) {
			continue
		}
		byID[snapshot.WatchID] = snapshot
	}
	m.mu.RUnlock()

	if m.db != nil {
		entries, err := m.db.ListReviewWatches(store.ReviewWatchFilter{
			GitHubLogin: login,
			Owner:       opts.Owner,
			Repo:        opts.Repo,
			PR:          opts.PR,
			ActiveOnly:  opts.ActiveOnly,
			Limit:       limit,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list review_watch: %w", err)
		}
		for i := range entries {
			if _, exists := byID[entries[i].ID]; exists {
				continue
			}
			snapshot := snapshotFromReviewWatchEntry(&entries[i])
			if !matchesListOptions(snapshot, opts) {
				continue
			}
			byID[snapshot.WatchID] = snapshot
		}
	}

	snapshots := make([]Snapshot, 0, len(byID))
	for _, snapshot := range byID {
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		activeI := !snapshots[i].Terminal
		activeJ := !snapshots[j].Terminal
		if activeI != activeJ {
			return activeI
		}
		if !snapshots[i].UpdatedAt.Equal(snapshots[j].UpdatedAt) {
			return snapshots[i].UpdatedAt.After(snapshots[j].UpdatedAt)
		}
		if !snapshots[i].StartedAt.Equal(snapshots[j].StartedAt) {
			return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
		}
		return snapshots[i].WatchID > snapshots[j].WatchID
	})
	if len(snapshots) > limit {
		snapshots = snapshots[:limit]
	}
	return snapshots, nil
}

// CancelByID stops a running watch by watch ID when it belongs to the caller.
func (m *Manager) CancelByID(login, watchID string) (CancelResult, error) {
	if login == "" || watchID == "" {
		return CancelResult{}, fmt.Errorf("login and watch_id are required")
	}

	m.mu.Lock()
	if state := m.watchesByID[watchID]; state != nil {
		if state.key.login != login {
			m.mu.Unlock()
			return CancelResult{}, nil
		}
		if state.terminal {
			snapshot := snapshotFromState(state)
			m.mu.Unlock()
			return CancelResult{Snapshot: snapshot, Found: true}, nil
		}
		now := m.now().UTC()
		// Extract triggerLogID before releasing the lock; DB update happens
		// outside the lock to avoid blocking the manager mutex during SQLite
		// write operations.
		var cancelByIDTriggerLogID int64
		hasCancelByIDTriggerLogID := m.db != nil && state.triggerLogID != nil
		if hasCancelByIDTriggerLogID {
			cancelByIDTriggerLogID = *state.triggerLogID
		}
		m.finishLocked(state, StatusCancelled, nil, now, "watch was cancelled manually")
		snapshot := snapshotFromState(state)
		m.mu.Unlock()
		if hasCancelByIDTriggerLogID {
			if err := m.db.UpdateCompletedAt(cancelByIDTriggerLogID); err != nil {
				slog.Warn("CancelByID: failed to update trigger_log completed_at",
					"watch_id", watchID,
					"trigger_log_id", cancelByIDTriggerLogID,
					"error", err,
				)
			}
		}
		return CancelResult{Snapshot: snapshot, Found: true, Cancelled: true}, nil
	}
	m.mu.Unlock()

	if m.db == nil {
		return CancelResult{}, nil
	}
	entry, err := m.db.GetReviewWatchByID(watchID)
	if err != nil {
		return CancelResult{}, fmt.Errorf("failed to load review_watch: %w", err)
	}
	if entry == nil || entry.GitHubLogin != login {
		return CancelResult{}, nil
	}
	return CancelResult{
		Snapshot: snapshotFromReviewWatchEntry(entry),
		Found:    true,
	}, nil
}

// CancelLatest stops the active watch for one user/PR key when present.
func (m *Manager) CancelLatest(login, owner, repo string, pr int) (CancelResult, error) {
	if login == "" || owner == "" || repo == "" || pr <= 0 {
		return CancelResult{}, fmt.Errorf("login, owner, repo, and pr are required")
	}

	key := watchKey{login: login, owner: owner, repo: repo, pr: pr}

	m.mu.Lock()
	if id, ok := m.activeByKey[key]; ok {
		if state := m.watchesByID[id]; state != nil {
			if state.terminal {
				snapshot := snapshotFromState(state)
				m.mu.Unlock()
				return CancelResult{Snapshot: snapshot, Found: true}, nil
			}
			now := m.now().UTC()
			// Extract triggerLogID before releasing the lock; DB update happens
			// outside the lock to avoid blocking the manager mutex during SQLite
			// write operations.
			var cancelLatestTriggerLogID int64
			hasCancelLatestTriggerLogID := m.db != nil && state.triggerLogID != nil
			if hasCancelLatestTriggerLogID {
				cancelLatestTriggerLogID = *state.triggerLogID
			}
			m.finishLocked(state, StatusCancelled, nil, now, "watch was cancelled manually")
			snapshot := snapshotFromState(state)
			m.mu.Unlock()
			if hasCancelLatestTriggerLogID {
				if err := m.db.UpdateCompletedAt(cancelLatestTriggerLogID); err != nil {
					slog.Warn("CancelLatest: failed to update trigger_log completed_at",
						"owner", owner, "repo", repo, "pr", pr,
						"trigger_log_id", cancelLatestTriggerLogID,
						"error", err,
					)
				}
			}
			return CancelResult{Snapshot: snapshot, Found: true, Cancelled: true}, nil
		}
		delete(m.activeByKey, key)
	}
	if id, ok := m.latestByKey[key]; ok {
		if state := m.watchesByID[id]; state != nil {
			snapshot := snapshotFromState(state)
			m.mu.Unlock()
			return CancelResult{Snapshot: snapshot, Found: true}, nil
		}
	}
	m.mu.Unlock()

	if m.db == nil {
		return CancelResult{}, nil
	}
	entry, err := m.db.GetLatestReviewWatch(login, owner, repo, pr)
	if err != nil {
		return CancelResult{}, fmt.Errorf("failed to load latest review_watch: %w", err)
	}
	if entry == nil {
		return CancelResult{}, nil
	}
	return CancelResult{
		Snapshot: snapshotFromReviewWatchEntry(entry),
		Found:    true,
	}, nil
}

// PollInterval returns the manager's configured polling cadence.
func (m *Manager) PollInterval() time.Duration {
	return m.pollInterval
}

func (m *Manager) run(w *watchState) {
	defer func() {
		if r := recover(); r != nil {
			m.markStale(w.id, fmt.Sprintf("watch worker panicked: %v", r))
		}
	}()

	if m.pollOnce(w.id) {
		return
	}

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			if m.pollOnce(w.id) {
				return
			}
		}
	}
}

func (m *Manager) pollOnce(watchID string) bool {
	m.mu.RLock()
	w := m.watchesByID[watchID]
	m.mu.RUnlock()
	if w == nil {
		return true
	}
	now := m.now().UTC()
	if now.Sub(w.startedAt) >= m.maxWatchDuration {
		m.finishStatusWithoutPoll(w.id, now, StatusTimeout, nil, fmt.Sprintf("watch exceeded max duration of %s", m.maxWatchDuration))
		return true
	}

	w.clientMu.RLock()
	client := w.client
	w.clientMu.RUnlock()
	if client == nil {
		m.finishFailureWithoutPoll(w.id, now, FailureReasonInternal, "watch client is unavailable")
		return true
	}

	callCtx, cancel := context.WithTimeout(w.ctx, m.pollTimeout)
	defer cancel()

	data, err := client.GetReviewData(callCtx, w.key.owner, w.key.repo, w.key.pr)
	now = m.now().UTC()

	if err != nil {
		if errors.Is(err, context.Canceled) && w.ctx.Err() != nil {
			return true
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if w.ctx.Err() != nil {
				return true
			}
			m.finishFailureWithPoll(w.id, now, FailureReasonGitHubError, fmt.Sprintf("github poll timed out after %s", m.pollTimeout))
			return true
		}
		if IsRateLimitHTTPError(err) {
			m.finishStatusWithPoll(w.id, now, StatusRateLimited, nil, err.Error())
			return true
		}
		reason := FailureReasonGitHubError
		if ghclient.IsAuthError(err) {
			reason = FailureReasonAuthExpired
		}
		m.finishFailureWithPoll(w.id, now, reason, err.Error())
		return true
	}

	var entry *store.TriggerEntry
	if m.db != nil {
		var err error
		entry, err = m.db.GetLatest(w.key.owner, w.key.repo, w.key.pr)
		if err != nil {
			m.finishFailureWithPoll(w.id, now, FailureReasonInternal, fmt.Sprintf("failed to read trigger_log: %v", err))
			return true
		}
	}

	var requestedAt *time.Time
	var prevReviewID *string
	if entry != nil {
		requestedAt = &entry.RequestedAt
		prevReviewID = entry.PrevReviewID
	}
	reviewStatus := ghclient.DeriveStatus(data, requestedAt, prevReviewID)

	if data.RateLimitRemaining < 10 {
		m.mu.Lock()
		current := m.watchesByID[watchID]
		if current == nil || current.terminal {
			m.mu.Unlock()
			return true
		}
		m.markPollLocked(current, now)
		current.reviewStatus = reviewStatusPtr(reviewStatus)
		current.lastError = nil
		if data.RateLimitReset.IsZero() {
			current.rateLimitResetAt = nil
		} else {
			current.rateLimitResetAt = cloneTimePtr(&data.RateLimitReset)
		}
		m.finishLocked(current, StatusRateLimited, nil, now, formatRateLimitMessage(data.RateLimitRemaining, data.RateLimitReset))
		m.mu.Unlock()
		return true
	}

	terminalStatus, terminal := watchStatusForReview(reviewStatus)
	if terminal {
		if m.db != nil && entry != nil && entry.CompletedAt == nil {
			if err := m.db.UpdateCompletedAt(entry.ID); err != nil {
				m.finishFailureWithPoll(w.id, now, FailureReasonInternal, fmt.Sprintf("failed to update trigger_log completed_at: %v", err))
				return true
			}
		}
		m.mu.Lock()
		current := m.watchesByID[watchID]
		if current == nil || current.terminal {
			m.mu.Unlock()
			return true
		}
		m.markPollLocked(current, now)
		current.reviewStatus = reviewStatusPtr(reviewStatus)
		current.lastError = nil
		current.rateLimitResetAt = nil
		if current.triggerLogID == nil && entry != nil {
			id := entry.ID
			current.triggerLogID = &id
		}
		m.finishLocked(current, terminalStatus, nil, now, "")
		m.mu.Unlock()
		return true
	}

	m.mu.Lock()
	current := m.watchesByID[watchID]
	if current == nil || current.terminal {
		m.mu.Unlock()
		return true
	}
	prevReviewStatus := current.reviewStatus
	m.markPollLocked(current, now)
	current.reviewStatus = reviewStatusPtr(reviewStatus)
	current.lastError = nil
	current.rateLimitResetAt = nil
	if current.triggerLogID == nil && entry != nil {
		id := entry.ID
		current.triggerLogID = &id
	}
	current.status = StatusWatching
	current.workerRunning = true
	if err := m.persistOrDegradeLocked(current, StatusWatching, now); err != nil {
		// persistOrDegradeLocked transitions the watch to a terminal state on error.
		// Notify subscribers so they don't miss the terminal transition.
		var notifyURIOnError string
		if m.notifyResourceUpdated != nil && current.resourceURI != nil {
			notifyURIOnError = *current.resourceURI
		}
		m.mu.Unlock()
		if notifyURIOnError != "" {
			go m.notifyResourceUpdated(notifyURIOnError)
		}
		return true
	}
	var notifyURI string
	if m.notifyResourceUpdated != nil && current.resourceURI != nil && reviewStatusChanged(prevReviewStatus, &reviewStatus) {
		notifyURI = *current.resourceURI
	}
	m.mu.Unlock()
	if notifyURI != "" {
		go m.notifyResourceUpdated(notifyURI)
	}
	return false
}

func (m *Manager) finishFailureWithPoll(watchID string, now time.Time, reason FailureReason, errText string) {
	reasonCopy := reason
	m.finishState(watchID, now, StatusFailed, &reasonCopy, errText, true)
}

func (m *Manager) finishFailureWithoutPoll(watchID string, now time.Time, reason FailureReason, errText string) {
	reasonCopy := reason
	m.finishState(watchID, now, StatusFailed, &reasonCopy, errText, false)
}

func (m *Manager) finishStatusWithPoll(watchID string, now time.Time, status Status, reason *FailureReason, errText string) {
	m.finishState(watchID, now, status, reason, errText, true)
}

func (m *Manager) finishStatusWithoutPoll(watchID string, now time.Time, status Status, reason *FailureReason, errText string) {
	m.finishState(watchID, now, status, reason, errText, false)
}

func (m *Manager) finishState(watchID string, now time.Time, status Status, reason *FailureReason, errText string, countedPoll bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w := m.watchesByID[watchID]
	if w == nil || w.terminal {
		return
	}

	if countedPoll {
		m.markPollLocked(w, now)
	} else {
		w.updatedAt = now
	}
	m.finishLocked(w, status, reason, now, errText)
}

func (m *Manager) markStale(watchID, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w := m.watchesByID[watchID]
	if w == nil || w.terminal {
		return
	}

	now := m.now().UTC()
	m.finishLocked(w, StatusStale, nil, now, errText)
}

func (m *Manager) markPollLocked(w *watchState, now time.Time) {
	w.pollsDone++
	w.updatedAt = now
	w.lastPolledAt = timePtr(now)
}

func (m *Manager) finishLocked(w *watchState, status Status, reason *FailureReason, now time.Time, errText string) {
	w.status = status
	w.failureReason = reason
	w.terminal = true
	w.workerRunning = false
	w.updatedAt = now
	w.completedAt = timePtr(now)
	if status == StatusStale {
		w.staleAt = timePtr(now)
	}
	w.token = ""
	w.clientMu.Lock()
	w.client = nil
	w.clientMu.Unlock()
	if errText != "" {
		w.lastError = &errText
	}
	delete(m.activeByKey, w.key)
	_ = m.persistOrDegradeLocked(w, status, now)
	w.cancel()
	if m.notifyResourceUpdated != nil && w.resourceURI != nil {
		uri := *w.resourceURI
		go m.notifyResourceUpdated(uri)
	}
}

func (m *Manager) nextID() string {
	seq := m.idSeq.Add(1)
	return fmt.Sprintf("cw_%d_%d", m.now().UTC().UnixNano(), seq)
}

func snapshotFromState(w *watchState) Snapshot {
	return Snapshot{
		WatchID:          w.id,
		Login:            w.key.login,
		Owner:            w.key.owner,
		Repo:             w.key.repo,
		PR:               w.key.pr,
		ResourceURI:      cloneStringPtr(w.resourceURI),
		WatchStatus:      w.status,
		ReviewStatus:     cloneReviewStatusPtr(w.reviewStatus),
		FailureReason:    cloneFailureReasonPtr(w.failureReason),
		Terminal:         w.terminal,
		WorkerRunning:    w.workerRunning,
		PollsDone:        w.pollsDone,
		StartedAt:        w.startedAt,
		UpdatedAt:        w.updatedAt,
		CompletedAt:      cloneTimePtr(w.completedAt),
		StaleAt:          cloneTimePtr(w.staleAt),
		LastPolledAt:     cloneTimePtr(w.lastPolledAt),
		RateLimitResetAt: cloneTimePtr(w.rateLimitResetAt),
		LastError:        cloneStringPtr(w.lastError),
	}
}

func snapshotFromReviewWatchEntry(entry *store.ReviewWatchEntry) Snapshot {
	var reviewStatus *ghclient.ReviewStatus
	if entry.ReviewStatus != nil {
		status := ghclient.ReviewStatus(*entry.ReviewStatus)
		reviewStatus = &status
	}
	var failureReason *FailureReason
	if entry.FailureReason != nil {
		reason := FailureReason(*entry.FailureReason)
		failureReason = &reason
	}
	return Snapshot{
		WatchID:          entry.ID,
		Login:            entry.GitHubLogin,
		Owner:            entry.Owner,
		Repo:             entry.Repo,
		PR:               entry.PR,
		ResourceURI:      cloneStringPtr(entry.ResourceURI),
		WatchStatus:      Status(entry.WatchStatus),
		ReviewStatus:     reviewStatus,
		FailureReason:    failureReason,
		Terminal:         !entry.IsActive,
		WorkerRunning:    false,
		PollsDone:        0,
		StartedAt:        entry.StartedAt,
		UpdatedAt:        entry.UpdatedAt,
		CompletedAt:      cloneTimePtr(entry.CompletedAt),
		StaleAt:          cloneTimePtr(entry.StaleAt),
		LastPolledAt:     nil,
		RateLimitResetAt: cloneTimePtr(entry.RateLimitResetAt),
		LastError:        cloneStringPtr(entry.LastError),
	}
}

func (m *Manager) persistLocked(w *watchState) error {
	if m.db == nil {
		return nil
	}
	reviewWatch := store.ReviewWatchEntry{
		ID:               w.id,
		GitHubLogin:      w.key.login,
		Owner:            w.key.owner,
		Repo:             w.key.repo,
		PR:               w.key.pr,
		TriggerLogID:     cloneInt64Ptr(w.triggerLogID),
		ResourceURI:      cloneStringPtr(w.resourceURI),
		WatchStatus:      string(w.status),
		ReviewStatus:     reviewStatusStringPtr(w.reviewStatus),
		FailureReason:    failureReasonStringPtr(w.failureReason),
		IsActive:         !w.terminal,
		StartedAt:        w.startedAt,
		UpdatedAt:        w.updatedAt,
		CompletedAt:      cloneTimePtr(w.completedAt),
		StaleAt:          cloneTimePtr(w.staleAt),
		LastError:        cloneStringPtr(w.lastError),
		RateLimitResetAt: cloneTimePtr(w.rateLimitResetAt),
	}
	return m.db.UpsertReviewWatch(reviewWatch)
}

func (m *Manager) persistOrDegradeLocked(w *watchState, intended Status, now time.Time) error {
	err := m.persistLocked(w)
	if err == nil {
		return nil
	}

	msg := fmt.Sprintf("failed to persist review_watch while recording %s: %v", intended, err)
	slog.Error(
		"failed to persist review_watch",
		"watch_id", w.id,
		"login", w.key.login,
		"owner", w.key.owner,
		"repo", w.key.repo,
		"pr", w.key.pr,
		"intended_status", intended,
		"err", err,
	)

	w.updatedAt = now
	if w.completedAt == nil {
		w.completedAt = timePtr(now)
	}
	w.lastError = &msg
	w.terminal = true
	w.workerRunning = false
	w.token = ""
	w.clientMu.Lock()
	w.client = nil
	w.clientMu.Unlock()
	delete(m.activeByKey, w.key)

	if intended == StatusStale {
		w.status = StatusStale
		w.failureReason = nil
		if w.staleAt == nil {
			w.staleAt = timePtr(now)
		}
	} else {
		reason := FailureReasonInternal
		w.status = StatusFailed
		w.failureReason = &reason
	}

	w.cancel()
	return err
}

func reviewStatusPtr(status ghclient.ReviewStatus) *ghclient.ReviewStatus {
	s := status
	return &s
}

func reviewStatusStringPtr(status *ghclient.ReviewStatus) *string {
	if status == nil {
		return nil
	}
	value := string(*status)
	return &value
}

func failureReasonStringPtr(reason *FailureReason) *string {
	if reason == nil {
		return nil
	}
	value := string(*reason)
	return &value
}

func cloneReviewStatusPtr(status *ghclient.ReviewStatus) *ghclient.ReviewStatus {
	if status == nil {
		return nil
	}
	s := *status
	return &s
}

func cloneFailureReasonPtr(reason *FailureReason) *FailureReason {
	if reason == nil {
		return nil
	}
	r := *reason
	return &r
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func cloneStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func stringPtr(v string) *string {
	return &v
}

func formatRateLimitMessage(remaining int, reset time.Time) string {
	resetText := "unknown"
	if !reset.IsZero() {
		resetText = reset.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"GitHub API rate limit is low (remaining=%d, reset=%s); poll again after the reset time",
		remaining,
		resetText,
	)
}

func resourceURIForWatch(watchID string) string {
	return fmt.Sprintf("copilot-review://watch/%s", watchID)
}

// IsRateLimitHTTPError reports whether err is a GitHub rate-limit HTTP failure.
func IsRateLimitHTTPError(err error) bool {
	var rateErr *github.RateLimitError
	if errors.As(err, &rateErr) {
		return true
	}
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		return true
	}
	return false
}

// reviewStatusChanged reports whether the review status has changed between prev and curr.
func reviewStatusChanged(prev *ghclient.ReviewStatus, curr *ghclient.ReviewStatus) bool {
	if prev == nil && curr == nil {
		return false
	}
	if prev == nil || curr == nil {
		return true
	}
	return *prev != *curr
}

func watchStatusForReview(status ghclient.ReviewStatus) (Status, bool) {
	switch status {
	case ghclient.StatusCompleted:
		return StatusCompleted, true
	case ghclient.StatusBlocked:
		return StatusBlocked, true
	default:
		return "", false
	}
}

func matchesListOptions(snapshot Snapshot, opts ListOptions) bool {
	if opts.Owner != "" && snapshot.Owner != opts.Owner {
		return false
	}
	if opts.Repo != "" && snapshot.Repo != opts.Repo {
		return false
	}
	if opts.PR > 0 && snapshot.PR != opts.PR {
		return false
	}
	if opts.ActiveOnly && snapshot.Terminal {
		return false
	}
	return true
}
