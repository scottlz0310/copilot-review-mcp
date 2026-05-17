---
name: pr-review-cycle
description: Waits for Copilot review completion via async watch polling (no mcp-resource-subscriber CLI required), then autonomously runs the PR review response cycle (fetch threads → classify & accept/reject → fix → reply → evaluate cycle → post summary). Invoke immediately after creating a PR or requesting a Copilot review. Does not merge autonomously.
---

# pr-review-cycle Skill

[日本語](pr-review-cycle.ja.md)

A skill that uses the `copilot-review` MCP server's watch tools to wait for Copilot review completion via **async watch polling**, then autonomously runs the PR review response cycle.

This skill relies only on MCP tools — no `mcp-resource-subscriber` or other subscription-based CLI is required. It is the polling-based alternative to `pr-review-subscribe` for environments where `mcp-resource-subscriber` is unavailable.

> **About this file**
> `docs/skills/pr-review-cycle.md` is a shared template for this repository.
> Copy it to your personal AI agent configuration (e.g. `~/.claude/skills/`) before use.
> Adapt MCP server keys to match your environment.

---

## Setup

### Required MCP Servers

| Server | Role | Reference |
|---------|------|-----------|
| `copilot-review` | Copilot review watch & thread operations | [README.md](../../README.md) |
| `github` | Post Issue/PR comments | [README.md](../../README.md) |

### Placeholder Substitution

| Placeholder | Role | Example |
|-------------|------|---------|
| `{CRM}` | `copilot-review` server tools | `mcp__copilot-review__*` |
| `{GH}` | `github` server tools | `mcp__github__*` |

> Tool name prefixes depend on your MCP client configuration. Check your IDE's MCP settings to confirm the exact prefix.

---

## Overall Flow

```
Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 → Phase 6
                ↑                                              ↓ WAIT / REQUEST_REREVIEW
                └──────────────────────────────────────────────┘
                                        ↓ READY_TO_MERGE
                              Phase 6.5 → Phase 6.6 → Phase 7 → Phase 8
                                        ↓ ESCALATE
                              End (report to user)
```

---

## Phase 0: Snapshot Check

1. Determine `owner`, `repo`, and `pr` (PR number).
2. Call `{CRM}:get_copilot_review_status` to immediately check the current state on GitHub.
3. `status = COMPLETED` or `BLOCKED`: → Go to Phase 2 (no watch needed).
4. `status = NOT_REQUESTED`: Request a review with `{CRM}:request_copilot_review` → Go to Phase 1.
5. `status = PENDING` / `IN_PROGRESS`: → Go to Phase 1.

## Phase 1: Start Async Watch + Wait for Completion

**Record the wait start time.** When looping back from Phase 6, record a new start time (the 15-minute timeout resets on each entry to Phase 1).

### 1-A: Start Watch

Call `{CRM}:start_copilot_review_watch` (returns immediately).

Record:
- `watch_id`
- `resource_uri` (`copilot-review://watch/{watch_id}`)
- `next_poll_seconds` (only present when `recommended_next_action=POLL_AFTER`)

If an active watch for the same PR already exists, it is reused.

### 1-B: Poll for Completion

Check `recommended_next_action` from the `start_copilot_review_watch` response first. If it is already a terminal action (e.g., `READ_REVIEW_THREADS`), proceed immediately without polling.

Otherwise, wait `next_poll_seconds` (minimum 1 second; server default is 90 seconds), then call `{CRM}:get_copilot_review_watch_status`. Repeat until a non-`POLL_AFTER` action is received.

Follow `recommended_next_action`:

| recommended_next_action | Action |
|------------------------|--------|
| `READ_REVIEW_THREADS` | → Phase 2 |
| `POLL_AFTER` | Re-poll after the specified number of seconds |
| `CHECK_FAILURE` | Report the error to the user and abort the cycle |
| `REAUTH_AND_START_NEW_WATCH` | Prompt user to re-authenticate and exit |
| `START_NEW_WATCH` | Return to Phase 1-A |

### 1-C: Timeout Handling (elapsed ≥ 15 minutes)

1. Cancel the watch with `{CRM}:cancel_copilot_review_watch`.
2. Post via `{GH}:add_issue_comment`:
   `Copilot review completion wait timed out after 15 minutes. Please resume manually.`
3. Guide the user on how to resume manually and exit.

## Phase 2: Fetch Threads

Call `{CRM}:get_review_threads`.

**Routing on 0 unresolved threads** (both cases → Phase 6.5 with the defaults below):
- `cycles_done = 0` and unresolved = 0: Copilot found no issues on first review.
- `cycles_done ≥ 1` and unresolved = 0: Re-review completed with no new issues; all previous fixes were approved.

When skipping Phase 3–6 due to 0 threads, use these defaults for Phase 7/8:
- `termination_status = READY_TO_MERGE`
- `override_applied = no`
- `final_cycle_fix_types`: blocking × 0, non-blocking × 0, suggestion × 0, trivial × 0

Otherwise (unresolved > 0), proceed to Phase 3.

## Phase 3: Classify & Accept/Reject (Autonomous)

Classify each unresolved comment:

| Category | Criteria |
|----------|----------|
| `blocking` | Runtime errors, data integrity violations, security risks, breaking changes, inconsistent published records |
| `non-blocking` | Recommended but not required: tests, logs, privacy, consistency improvements |
| `suggestion` | Design, naming, structure, or maintainability suggestions |

Decide `accept` or `reject` autonomously. Reject only with a concrete reason: out of scope, already handled, invalid premise, or intentionally deferred.

**Reject constraint — scope-out / deferred requires a tracking issue.**
A reject with reason `out-of-scope`, `deferred`, or `follow-up` is NOT complete until traceable to a follow-up issue. The `Follow-up issue` column MUST be filled.

Reject reasons that do NOT require a follow-up issue:
- `already-handled` — cite the commit / PR / issue.
- `invalid-premise` — explain the misunderstanding.
- `wont-fix` — explicit decision; must NOT say "will handle later".

Present this table before editing:

```
| # | Thread ID | Class | Decision | Summary | Reject reason | Follow-up issue |
|---|-----------|-------|----------|---------|---------------|-----------------|
```

Determine `fix_type` for Phase 6:

| fix_type | Use for |
|----------|---------|
| `logic` | Code behavior or test-only changes |
| `spec_change` | Public docs, API, workflow, or compatibility record semantics |
| `trivial` | Typo, formatting, or wording-only fix |
| `none` | No accepted changes (all rejected) |

## Phase 4: Fix + Commit

1. Run `git status --short --branch`.
2. Fix only `accept`-ed items.
3. Keep changes atomic by review thread unless a shared edit is clearly cleaner.
4. Re-run build and tests after all fixes. Retry on failure. If unresolvable, abort the cycle and report to the user.
5. After Phase 4 completes, make **one commit** covering all fixes (Conventional Commits format).
6. Push without force unless the user explicitly asks otherwise.

Do not revert unrelated user changes.

## Phase 5: Reply + Resolve Threads

For every reviewed thread, call `{CRM}:reply_and_resolve_review_thread`.

- **Fixed**: mention the commit and concrete fix.
- **Rejected**: state the reason clearly. Always set `resolve=true` unless a tracking issue cannot be created (see step 4 below).

### Reject reply rules

A scope-out reject is not complete until it is traceable to a follow-up issue.

#### 1. Linking an existing issue

Include `Tracked by #xxx` or `Follow-up: #xxx` in the reply. Confirm the linked issue actually covers the rejected item — do NOT reuse an unrelated issue.

#### 2. Creating a new follow-up issue

When no existing issue covers the item:
1. Call `{GH}:create_issue` with a descriptive title/body referencing the PR and thread.
2. Capture the new issue number.
3. Include `Follow-up: #<number>` in the reply.
4. Record the number in the Phase 3 decision table and Phase 7 summary.

#### 3. Explicit `Won't fix`

Reply with `Won't fix` and a concrete reason. Do NOT write "will handle later" or "deferred" — those require step 1 or 2.

#### 4. When issue creation or linking is not possible

- Do NOT resolve the thread (`resolve=false`).
- Record it in Phase 7 as `untracked — needs follow-up issue`.

## Phase 6: Cycle Evaluation

Call `{CRM}:get_pr_review_cycle_status`:

```json
{
  "owner": "<owner>",
  "repo": "<repo>",
  "pr": 42,
  "cycles_done": 0,
  "max_cycles": 0,
  "fix_type": "<fix_type from Phase 3>"
}
```

> `max_cycles: 0` uses the server-side default (`MAX_REVIEW_CYCLES`, defaults to 3 if unset).
> `cycles_done` is 0-based. Increment on each entry to Phase 1 (first call: `0`, second cycle: `1`, …).

Follow `recommended_action`:

| recommended_action | Next Action |
|-------------------|-------------|
| `WAIT` | Increment `cycles_done`, return to Phase 1 |
| `REPLY_RESOLVE` | Return to Phase 2 (unresolved threads remain) |
| `REQUEST_REREVIEW` | See override rule; otherwise call `{CRM}:request_copilot_review` → increment `cycles_done` → Phase 1 |
| `READY_TO_MERGE` | → Phase 6.5 |
| `ESCALATE` | Classify and report (see below), then stop |

**`REQUEST_REREVIEW` override**: If `recommended_action = REQUEST_REREVIEW` AND `cycles_done ≥ 1` AND unresolved thread count from Phase 2 of this cycle = 0, do **not** request another review. Treat as `READY_TO_MERGE` and proceed to Phase 6.5.

**Termination classification**:

| Classification | Condition | Implication |
|---------------|-----------|-------------|
| ✅ `READY_TO_MERGE` | `recommended_action = READY_TO_MERGE`, or override applied with `unresolved = 0` | Safe — normal merge gate |
| 🟡 `ESCALATE — Clean` | `ESCALATE` AND final cycle's accepted fixes contain **no** `blocking` items | Likely safe — note unverified status |
| 🔴 `ESCALATE — Unverified Fix` | `ESCALATE` AND final cycle accepted **at least one `blocking` fix** not re-reviewed by Copilot | Risky — recommend human review before merge |

Record for Phase 7:
- `termination_status`
- `final_cycle_fix_types`: counts of `blocking` / `non-blocking` / `suggestion` / `trivial` accepts
- `override_applied`: `yes` or `no`
- `unverified_blocking_commits`: list of commit SHAs when `ESCALATE — Unverified Fix`

On `ESCALATE — Unverified Fix`, still proceed to Phase 6.5 / 6.6 / 7, but Phase 8 must downgrade merge readiness regardless of CI outcome.

## Phase 6.5: CI Check

1. Run `gh pr checks <PR number>`.
2. All jobs SUCCESS → Phase 6.6.
3. Failing jobs: fetch logs with `gh run view <run-id> --log-failed` and analyze the cause.
   - **Fixable** (code issue, clear failure cause): Add to the accept/reject table and return to Phase 4.
   - **Hard to fix** (environment factors, flaky tests, unknown cause): Report to the user and await instructions.

If `gh` is unavailable or cannot access the PR checks, use `{GH}` / GitHub MCP server to inspect check runs or PR status. If neither route can verify CI, report `CI: unknown` and stop before Phase 7.

## Phase 6.6: Coverage Check

Check Codecov or similar coverage PR comments if present.

- If testable coverage gaps are introduced, return to Phase 4 (`fix_type = logic`).
- If no relevant coverage signal exists or there is no issue, continue to Phase 7.

## Phase 7: Post Summary Comment

Post via `{GH}:add_issue_comment`:

```markdown
## Review Cycle Summary

### Changes Made
- (bulleted overview)

### Accept/Reject Decisions
- accept: N items
- reject: M items
  - Thread <threadId> (PRRT_xxx): (reason)

### Deferred / Scope-out Items
- None | <list: Thread <threadId> (PRRT_xxx) — summary>

### Verification
- CI: ...
- Unresolved threads: ...
- Cycle status: <termination_status>
  - On `ESCALATE — Unverified Fix`: reason, unverified commit SHA(s), and "Recommendation: human review before merge"
- Final cycle fix types: blocking × N, non-blocking × N, suggestion × N, trivial × N
- Override applied (0-unresolved re-review): yes | no
```

**`Deferred / Scope-out Items` rules:**

- MUST list every reject with reason `out-of-scope` / `deferred` / `follow-up`, with the follow-up issue number.
- `- None` is only allowed when no reject used those reasons AND no thread was left unresolved per Phase 5 step 4.
- Untracked items: `Thread <threadId> (PRRT_xxx) — untracked — needs follow-up issue (Phase 5 step 4)`.
- `Won't fix` rejects do NOT go in this section.

## Phase 8: Merge Decision

**Do not merge autonomously.** Wait for explicit instructions from the user.

Merge conditions (must be satisfied when the user instructs a merge):
- CI all SUCCESS
- Unresolved review threads = 0
- All threads replied
- No unresolved `blocking` items
- `termination_status` is `READY_TO_MERGE` or `ESCALATE — Clean`

If `termination_status = ESCALATE — Unverified Fix`:
1. Do **not** report the PR as ready to merge, even if CI is green.
2. Surface the warning with unverified commit SHA(s).
3. If the user still requests merge, confirm they have manually reviewed the unverified blocking fix.

If any other condition is not met, report the missing items and await instructions.

---

## Notes

- Polling interval: follow `next_poll_seconds` from the response (minimum 1 second; server default 90 seconds)
- Timeout: resets to 15 minutes on each entry to Phase 1
- Re-review limit: server-side `MAX_REVIEW_CYCLES` (default 3). After `ESCALATE`, defer to human judgment
- Fix granularity: atomic per thread (1 thread = 1 logical change unit)
- Commit strategy: one commit after Phase 4 completes (Conventional Commits format)
- Phase 3 accept/reject decisions are autonomous but the result table must always be presented (for auditability)
- Phase 8 requires explicit user instruction (operational safety)

---

## Tool Reference

| Tool | Purpose |
|------|---------|
| `{CRM}:get_copilot_review_status` | Instant check of Copilot review state on GitHub |
| `{CRM}:request_copilot_review` | Request a Copilot review |
| `{CRM}:start_copilot_review_watch` | Start async watch (returns immediately) |
| `{CRM}:get_copilot_review_watch_status` | Poll current watch state |
| `{CRM}:cancel_copilot_review_watch` | Cancel a watch |
| `{CRM}:get_review_threads` | List review threads |
| `{CRM}:reply_and_resolve_review_thread` | Reply to and resolve a thread |
| `{CRM}:get_pr_review_cycle_status` | Evaluate cycle and determine next action |
| `{GH}:add_issue_comment` | Post a comment to the PR |
| `{GH}:create_issue` | Create a follow-up tracking issue |
