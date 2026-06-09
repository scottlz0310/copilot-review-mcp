---
name: thread-owl-review-cycle
description: "Reviewed-side cycle for thread-owl reviewers. Reads thread-owl review threads, classifies & fixes, replies, resolves, then posts @thread-owl re-review requested when re-review is needed. Invoke when thread-owl has posted a new review (review threads are present in the PR)."
---

# thread-owl-review-cycle Skill

[日本語](thread-owl-review-cycle.ja.md)

> **Scope: thread-owl reviews only.**
> This skill handles the reviewed-side cycle when the reviewer is **thread-owl**.
> For **Copilot** reviews (async watch polling), use [`pr-review-cycle`](pr-review-cycle.md) instead.

A skill for the reviewed-side cycle when thread-owl is the reviewer. There is no Copilot watch loop. Entry is triggered when thread-owl posts a new review (unresolved thread-owl review threads are present in the PR). Invoke this skill after receiving a thread-owl review notification.

Re-review requests are posted as `@thread-owl re-review requested` PR comments — the reviewed-side cycle ends there. The next reviewer-side cycle is triggered by thread-owl detecting the comment via its `issue_comment.created` webhook, which enqueues a `re-review-requested` candidate and notifies `queue://review/re-review-requests` subscribers (reviewer-side).

> **About this file**
> `docs/skills/thread-owl-review-cycle.md` is a shared template for this repository.
> Copy it to your personal AI agent configuration (e.g. `~/.claude/skills/`) before use.
> Adapt MCP server keys to match your environment.

---

## Setup

### Required MCP Servers

| Server | Role | Reference |
|--------|------|-----------|
| `github` | Post PR comments, create issues | [README.md](../../README.md) |

> `review-raven` MCP tools are NOT used in this skill. Thread reading is done via `gh` CLI (GraphQL).

### Placeholder Substitution

| Placeholder | Role | Example |
|-------------|------|---------|
| `{GH}` | `github` server tools | `mcp__github__*` |

---

## Overall Flow

```
Phase 0 (entry + cycles_done recovery)
  |
  v
Phase U2: Collect threads → Phase 3: Classify → Phase 4: Fix → Phase U5: Reply/Resolve
                                                                          |
                                                              Phase U6: Cycle evaluation
                                                                          |
                                        ┌─────────────────────────────────┘
                                        ↓ READY_TO_MERGE (need_re_review=no)
                              Phase 6.5 → Phase 6.6 → Phase 7 → Phase 8
                                        ↓ ESCALATE (max cycles exceeded)
                              Phase 6.5 (report) → Phase 7 → Phase 8
                                        ↓ REQUEST_REREVIEW (cycles_done < max_cycles)
                              Post @thread-owl comment → reviewed-side cycle complete
```

---

## Phase 0: Entry & State Recovery

1. Determine `owner`, `repo`, `pr`.
2. Set `max_cycles = 3` (default; adjust if needed).
3. Recover `cycles_done` from PR comment history:
   - Search PR issue comments for `<!-- review-raven: cycles_done=N -->` (most recent occurrence).
   - If found: `cycles_done = N + 1`.
   - If not found: `cycles_done = 0`.
4. Proceed to Phase U2.

## Phase U2: Collect Review Threads

Retrieve all review threads via paginated GraphQL:

```bash
gh api graphql -f query='
  query($owner: String!, $repo: String!, $pr: Int!, $cursor: String) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $pr) {
        reviewThreads(first: 100, after: $cursor) {
          pageInfo { hasNextPage endCursor }
          nodes {
            id
            isResolved
            comments(first: 10) {
              nodes {
                databaseId
                body
                path
                line
                author { login }
                createdAt
              }
            }
          }
        }
      }
    }
  }
' -f owner=<owner> -f repo=<repo> -F pr=<pr>
```

- If `pageInfo.hasNextPage` is `true`, repeat with `-f cursor=<endCursor>` until exhausted.
- Collect threads where `isResolved = false` **and** the root comment author is `thread-owl`. Skip threads posted by other reviewers (Copilot, human reviewers, etc.) — they are out of scope for this skill.
- Record each thread's `id` (PRRT node ID — for resolve mutation) and root comment `databaseId` (for replies).
- If 0 unresolved thread-owl threads: proceed to **Phase 6.5** with `termination_status = READY_TO_MERGE`.
- Otherwise: proceed to **Phase 3**.

## Phase 3: Classify & Accept/Reject (Autonomous)

Classify each unresolved comment:

| Class | Criteria |
|-------|----------|
| `blocking` | Runtime errors, data integrity violations, security risks, breaking changes, inconsistent published records |
| `non-blocking` | Recommended but not required: tests, logs, privacy, consistency improvements |
| `suggestion` | Design, naming, structure, or maintainability suggestions |

Decide `accept` or `reject` autonomously. Reject only with a concrete reason.

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

Determine `fix_type`:

| fix_type | Use for |
|----------|---------|
| `logic` | Code behavior or test-only changes |
| `spec_change` | Public docs, API, workflow, or compatibility record semantics |
| `trivial` | Typo, formatting, or wording-only fix |
| `none` | No accepted changes (all rejected) |

## Phase 4: Fix & Commit

1. Run `git status --short --branch`.
2. Fix only `accept`-ed items.
3. Keep changes atomic by review thread unless a shared edit is clearly cleaner.
4. Re-run build and tests after all fixes.
5. Make **one commit** covering all fixes (Conventional Commits format).
6. Push without force unless the user explicitly asks otherwise.

## Phase U5: Reply & Resolve

**Reply** using `{GH}:add_reply_to_pull_request_comment`:
- `owner`, `repo`, `pull_number`: from Phase 0
- `comment_id`: root comment's `databaseId` from Phase U2
- Fixed: mention the commit and concrete fix.
- Rejected: state the reason clearly (see reject sub-rules below).

**Resolve** using GraphQL mutation:

```bash
gh api graphql -f query='
  mutation($threadId: ID!) {
    resolveReviewThread(input: {threadId: $threadId}) {
      thread { id isResolved }
    }
  }
' -f threadId=<PRRT_node_id>
```

Always resolve unless a tracking issue cannot be created (see step 4 below).

### Reject reply rules

#### 1. Linking an existing issue
Include `Tracked by #xxx` or `Follow-up: #xxx`. Confirm the issue actually covers the rejected item.

#### 2. Creating a new follow-up issue
Call `{GH}:create_issue`. Include `Follow-up: #<number>` in the reply. Record the number in Phase 3 table and Phase 7 summary.

#### 3. Explicit `Won't fix`
Reply with `Won't fix` and a concrete reason. Do NOT write "will handle later".

#### 4. When issue creation is not possible
Do NOT resolve the thread. Record as `untracked — needs follow-up issue` in Phase 7.

## Phase U6: Cycle Evaluation & Re-review Decision

**Step 1**: Re-fetch unresolved threads (re-run Phase U2 query).
- If unresolved > 0: unexpected. Report and stop with `needs user decision`.

**Step 2**: Determine `need_re_review` (only when unresolved = 0):

| fix_type | Accepted `blocking`? | need_re_review |
|----------|----------------------|----------------|
| `none` | — | **no** |
| `trivial` | — | **no** |
| `logic` or `spec_change` | any | **yes** |
| any | at least 1 `blocking` | **yes** |

**Step 3**: Route

- `need_re_review = no` → **Phase 6.5** (`termination_status = READY_TO_MERGE`)
- `need_re_review = yes` AND `cycles_done ≥ max_cycles` → classify termination, proceed to **Phase 6.5**
- `need_re_review = yes` AND `cycles_done < max_cycles` → post `@thread-owl` comment (see format below), **reviewed-side cycle complete**

### Termination classification

| Classification | Condition | Merge implication |
|----------------|-----------|-------------------|
| ✅ `READY_TO_MERGE` | unresolved = 0, need_re_review = no | Safe — normal merge gate |
| 🟡 `ESCALATE — Clean` | max cycles AND final cycle has **no** `blocking` accepts | Likely safe — note unverified status |
| 🔴 `ESCALATE — Unverified Fix` | max cycles AND final cycle accepted **≥ 1 `blocking` fix** not re-reviewed | Risky — recommend human review |

Record for Phase 7: `termination_status`, `final_cycle_fix_types`, `unverified_blocking_commits`.

### Re-review request comment format

Post via `{GH}:add_issue_comment`:

```markdown
@thread-owl re-review requested

The requested changes have been addressed. Please review again.

<!-- review-raven: cycles_done=N -->
```

Replace `N` with the current value of `cycles_done`. This annotation is used to recover `cycles_done` when the skill re-enters at Phase 0 after the next thread-owl queue notification.

**The reviewed-side cycle ends here. Do NOT loop back to Phase U2.**
The next reviewer-side cycle is triggered by thread-owl's `issue_comment.created` webhook → queue → mcp-resource-subscriber notification.

---

## Phase 6.5: CI Check

1. Run `gh pr checks <pr>`.
2. All jobs SUCCESS → Phase 6.6.
3. Failing jobs: fetch logs with `gh run view <run-id> --log-failed`.
   - Fixable → add to accept work, return to Phase 4.
   - Not fixable → report and stop.

If `gh` is unavailable, use `{GH}` / GitHub MCP server to inspect check runs. If neither route works, report `CI: unknown` and stop.

## Phase 6.6: Coverage Check

Check Codecov or similar PR comments if present.
- If testable coverage gaps exist, return to Phase 4 (`fix_type = logic`).
- Otherwise continue to Phase 7.

## Phase 7: Summary Comment

Post via `{GH}:add_issue_comment`:

```markdown
## Review Cycle Summary (thread-owl)

### Changes Made
- (bulleted overview)

### Accept/Reject Decisions
- accept: N items
- reject: M items
  - Thread <threadId> (PRRT_xxx): (reason)

### Deferred / Scope-out Items
- None | <list: Thread <threadId> — Follow-up: #N>

### Verification
- CI: ...
- Unresolved threads: 0
- Cycle status: <termination_status>
  - On `ESCALATE — Unverified Fix`: reason, unverified commit SHA(s), "Recommendation: human review before merge"
- Final cycle fix types: blocking × N, non-blocking × N, suggestion × N, trivial × N
- cycles_done: N
- Re-review: requested via @thread-owl comment | not needed | ESCALATE (max cycles)
```

**`Deferred / Scope-out Items` rules:** MUST list every `out-of-scope` / `deferred` / `follow-up` reject with follow-up issue number. `- None` only when zero such rejects exist AND no thread was left unresolved.

## Phase 8: Merge Gate

**Never merge autonomously.** Wait for explicit user instruction.

Merge conditions:
- CI all SUCCESS
- Unresolved review threads = 0
- All threads replied
- No unresolved `blocking` items
- `termination_status` is `READY_TO_MERGE` or `ESCALATE — Clean`

If `termination_status = ESCALATE — Unverified Fix`:
1. Do NOT report as ready to merge.
2. Surface warning with unverified commit SHA(s).
3. If user still requests merge, confirm they have manually reviewed the unverified blocking fix.

If `termination_status = WAITING_FOR_REVIEW(thread-owl)` (re-review comment posted):
1. Do NOT report as ready to merge.
2. Report: "Re-review requested from thread-owl. Waiting for next review cycle."

---

## Notes

- `max_cycles` default is 3. Adjust at Phase 0 if needed.
- `cycles_done` is recovered from `<!-- review-raven: cycles_done=N -->` PR comment annotation, not from server state.
- Re-review requests use `@thread-owl` PR comments — NOT `request_copilot_review`.
- This skill does NOT start Copilot watches or call `get_pr_review_cycle_status`.
- Fix granularity: atomic per thread (1 thread = 1 logical change unit).
- Commit strategy: one commit after Phase 4 (Conventional Commits format).
- Phase 8 requires explicit user instruction.

---

## Tool Reference

| Tool | Purpose |
|------|---------|
| `gh` CLI | GraphQL thread collection, resolve mutation, CI checks |
| `{GH}:add_reply_to_pull_request_comment` | Reply to review thread |
| `{GH}:add_issue_comment` | Post PR summary / re-review request comment |
| `{GH}:create_issue` | Create follow-up tracking issue |

---

## See Also

- [`pr-review-cycle`](pr-review-cycle.md) — For Copilot reviews. Uses `request_copilot_review` + async watch loop instead of `@thread-owl` comment.
