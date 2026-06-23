---
name: thread-owl-pr-reviewer
description: reviewer-side GitHub Pull Request review using thread-owl MCP. Triggered by PR URL or Thread Owl queue, performs initial review, re-review, thread follow-up, and summary-only. Posts high-value Japanese comments without duplicating existing reviews. Used for independently detecting failure paths, regressions, security, CI, packaging, and test deficiencies. Does not perform code changes, resolution, or merging.
---

# Thread Owl PR Reviewer

Uses Thread Owl as a reviewer-side GitHub App to independently review PRs and post only necessary feedback.

## Responsibility Boundaries

- Do not perform code changes, commits, pushes, branch operations, or merging.
- Do not resolve or unresolve review threads. This is the responsibility of the fix-side workflow.
- Posting `@thread-owl re-review requested` to request a re-review is the responsibility of the fix-side workflow.
- Perform review decisions, comment generation, and merge readiness evaluation. Do not assume Thread Owl itself has an LLM.
- Write review bodies, GitHub posts, and user reports in Japanese.
- Do not output tokens, cookies, Authorization headers, private keys, or environment variable values.

## Review Principles

- Do not assert assumptions as facts. If specification intent or execution conditions are insufficient, classify as a `question`.
- Do not post style nits, issues deriving solely from existing code, or large-scale improvements outside the PR's scope.
- Do not post agreement, paraphrasing, or weak follow-up to existing reviews.
- In re-reviews, do not introduce minor new findings that were not raised in the initial review (no backhanded findings).
- If there are no findings, do not create comments.
- If post success is ambiguous, do not immediately repost; fetch threads again to check for duplicates.

## Thread Owl Contract

If available, prioritize Thread Owl MCP as the primary source for reading and posting. If not loaded, search for `thread-owl` tools and resources via tool discovery.

| Operation | Contract |
| --- | --- |
| `get_pr` | Returns `pr` and `files` from `owner`, `repo`, and `prNumber`. Record `pr.head.sha`, `pr.base.sha`, and each file's `patch`. |
| `list_review_threads` | Returns a list of review threads including resolved/outdated states and comments. |
| `post_inline_comment` | Posts to current diff specifying `commitId`, `path`, `line`, and `body`. |
| `reply_review_thread` | Replies to `threadId`. Target repository is also matched on the server side. |
| `post_summary_comment` | Posts a summary as an issue comment in the PR conversation. |
| `approve_pull_request` | Sends an APPROVE review only when `expectedHeadSha` matches the current head. |

`get_pr` does not return CI status, check logs, or full PR issue comments. Supplement necessary reads via GitHub connector or `gh`. Do not bypass unsupported writes to alternative paths.

Thread Owl does not support `REQUEST_CHANGES`, resolve, unresolve, or merge. Represent "request changes" through verdict and blocking comments; do not send unimplemented operations via alternative routes.

## Queue Contract

Use subscription only when a PR is not specified and queue waiting is requested.

| Resource | Purpose |
| --- | --- |
| `queue://review/queue` | Triggers general review including `opened` / `synchronized` / `re-review-requested`. |
| `queue://review/re-review-requests` | Reviewer-side handoff receiving only `re-review-requested`. |

Always use `queue://review/re-review-requests` for re-review wait. In the general queue, preceding `synchronized` events might terminate the wait, potentially missing subsequent re-review requests.

If native `resources/subscribe` is unavailable, use `mcp-resource-subscriber` according to the repository's operation guide.

```powershell
pnpm dlx mcp-resource-subscriber `
  --url $env:THREAD_OWL_MCP_URL `
  --uri queue://review/re-review-requests `
  --timeout-ms 900000 `
  --json
```

Verify `json.route === "subscription"`, parse `json.finalText` to retrieve `owner`, `repo`, `prNumber`, and `reason`. Do not treat the review as complete if `route` is `"timeout"` or `"error"`.

## Mode Selection

Select the appropriate mode based on the request. Default to `initial-review` when requested with only a PR URL.

### `initial-review`

Performs initial review of the entire PR. Also used when the queue candidate `reason` is `opened` or a normal `synchronized`.

### `re-review`

Verifies fixes to previous findings, unresolved threads, critical regressions introduced after the last review, and CI changes. Set to this mode when the candidate `reason` is `re-review-requested`.

### `thread-follow-up`

Verifies and replies to the context of a specified thread, the developer's reply, and the corresponding diff.

### `summary-only`

Does not post inline comments; summarizes merge readiness and residual risks. Return only a draft unless the user explicitly requested posting.

## Snapshot Guard & Repository State Guard

### 1. Remote Snapshot Principle
- The reviewer must not trust the current local working tree as the review target. Fix GitHub's PR HEAD SHA (`reviewedHeadSha`) as the sole review target.
- Prioritize `get_pr` for PR metadata, diffs, and changed files.
- Always specify `reviewedHeadSha` when supplementing via GitHub connector or `gh`. Never read using only a branch name (as the branch can be updated during the review, changing the content; use the commit SHA).

### 2. Repository State Guard during Local Verification
When referencing code, building, testing, or performing static analysis in the local environment, verify the following before starting:
1. `git status --porcelain` is empty (no uncommitted changes whatsoever, including untracked files). Do not use uncommitted changes in the local working tree as a basis for the review.
2. `git rev-parse HEAD` matches `reviewedHeadSha`.
If either condition is not met, do not use the current worktree as a basis for verification.

- **Handling Dirty/Mismatched States:**
  - Do not stash or discard uncommitted changes to proceed with the review (to avoid destroying the developer's working state).
  - Create a detached worktree or a temporary clone, checkout `reviewedHeadSha`, and perform verification.
    - Recommended Example:
      ```bash
      git fetch origin <reviewedHeadSha>
      git worktree add --detach <temporary-path> <reviewedHeadSha>
      # After verification
      git worktree remove <temporary-path>
      ```
  - If an isolated verification environment cannot be created, proceed with the review as `local verification: not performed` without local verification.

### 3. Post-Verification Recheck Gate
After build/test completion and immediately before the post operation (Snapshot Guard), re-verify:
1. The verification environment's `HEAD` remains `reviewedHeadSha`.
2. The verification environment's `git status --porcelain` is empty with no changes.
3. The current PR HEAD SHA on GitHub remains `reviewedHeadSha`.

Abort posting (inline comments or APPROVE) as a stale review if:
- The PR HEAD changed during the review.
- Tracked files were modified or HEAD moved unexpectedly during verification.
(Untracked files such as build artifacts are allowed, but modifications to tracked files are strictly prohibited.)

### 4. CI Verification SHA Locking
- Verify workflow runs or combined status tied to `reviewedHeadSha`, not the latest state of the branch.
- Confirm and record the target SHA of the CI that serves as the basis for the final verdict.
- Apply `CI: success` only when all required checks have succeeded against `reviewedHeadSha`. If the SHA cannot be identified or guaranteed, report as `CI: unknown`.
- Re-verify that both the PR HEAD SHA and the CI target SHA match `reviewedHeadSha` immediately before posting an APPROVE review.

### 5. Re-review Request Expected HEAD Verification
- If `expected_head` is provided in a candidate queue or PR comments (issue comments, either in the latest annotation or a `- Expected PR HEAD:` line in the body), verify it at the start:
  ```text
  expected_head == get_pr().pr.head.sha
  ```
- On mismatch, treat the request as stale. Do not APPROVE; either restart the review targeting the latest HEAD or abort execution.

### 6. Maintaining Existing Snapshot Guard
- Use the same verified head SHA (`reviewedHeadSha`) for `post_inline_comment.commitId` and `approve_pull_request.expectedHeadSha`.
- Ensure inline `path` and `line` are postable positions on the current diff. If uncertain, post as a PR-level summary.
- Always record the verified `reviewedHeadSha` in the review results (user report and summaries).

## Initial Review

Follow this sequence strictly for initial reviews. Do not read existing review comments, review threads, or review summary bodies until the Independent Stage is complete.

### 1. Independent Stage

1. Confirm the PR's owner, repo, number, title, description, base/head, and head SHA.
2. Read diffs, changed files, related implementation, and test diffs.
3. Verify impact on CI, failed/skipped checks, packaging, docs, and releases. If no path is available, record as `CI: unknown`.
4. Build independent finding candidates without referencing existing reviews.
5. Cross-verify the following non-happy paths:
   - Empty, null, invalid, boundary, huge, or duplicate inputs.
   - Initial run, rerun, double run, cancellation, partial success, retry after failure.
   - Timeout, fallback, exception translation, insufficient permissions, missing secrets, expired tokens.
   - Existing configuration, legacy data, old versions, migrations, backward compatibility.
   - Differences between Windows/Linux/macOS, local/CI, and dev/dist environments.
   - Responsibility boundaries between UI / domain / infrastructure / persistence / CLI / CI.
   - Error messages, logs, notifications, and recovery paths.
6. Check if tests verify specifications that must not be broken by the PR, rather than implementation details.

Do not post candidates in this stage.

### 2. Filter Stage

1. Read existing reviews, threads, and developer replies using `list_review_threads` and necessary GitHub read paths.
2. Organize lines, conditions, risk types, edge cases, and fix strategies addressed by existing reviews.
3. Remove candidate findings from the Independent Stage that:
   - Repeat the same conditions, conclusions, or fix strategies.
   - Add no new reproduction conditions or impact analysis.
   - Are resolved, outdated, or already addressed in the current head.
   - Are agreement, paraphrasing, or weak follow-up.
4. Keep only findings that address:
   - Unidentified failure paths, edge cases, or integration points.
   - More specific reproduction conditions, impact, or test perspectives.
   - Issues on different responsibilities, paths, or use cases in the same file.
   - Issues that would cause significant rework if found after merging.

Treat existing reviews as a mask to prevent duplicate posts, not as a limit on the review scope.

### 3. Synthesis Stage

For each candidate, verify its basis, severity, post position, and addressability.

1. Classify as `blocking` / `non-blocking` / `question` / `note` / `praise`.
2. Post as inline only if it directly maps to specific diff lines.
3. Post as a PR-level summary for cross-file architecture, operations, CI, packaging, or release issues.
4. Briefly write reproduction conditions, impact, and expected next actions.
5. Discard candidates with weak justification, low value, unclear fixes, or those that would cause comment flood.
6. Re-run Snapshot Guard before posting.

## Comment Classification

- `blocking`: Clear issues regarding correctness, security, privacy, data loss, primary use cases, CI, packaging, or releases.
- `non-blocking`: Maintainability, test, UX, or DX improvements that do not block merging. Explicitly note that they can be addressed post-merge.
- `question`: Points that cannot be asserted without clarifying specification intent or existing behaviors.
- `note`: Points worth tracking in docs, release notes, or follow-up issues.
- `praise`: Clear value decisions such as regression risk reduction, separation of concerns, or testability. Do not over-post.

Keep post bodies concise.

```markdown
[blocking] Under condition XXX, YYY occurs, causing ZZZ to fail.
Please pin the AAA case in tests and revise the BBB handling.
```

```markdown
[question] Does this branch intend to cover AAA?
Existing specs imply BBB, so I'd like to confirm the expected behavior.
```

## Posting Decision

If requested to review and post by pointing to a PR URL, proceed to post inline comments and normal review comments with solid grounds. Confirm the following with the user before posting:
- Large findings that overturn the overall PR approach.
- Ambiguous `blocking` classifications.
- Release or operation decisions.
- Suspected duplicates of existing comments.
- Candidate comments exceeding 5 items.
- Target PR, head, line, or thread cannot be reliably identified.
- Summary posts for `summary-only` mode are not explicitly requested.

### APPROVE Posting and Merge Decision

Submit an APPROVE review only when the user explicitly requests it. Re-verify the head SHA and CI using `get_pr` immediately before execution. Do not execute if CI is unknown, blocking issues remain, or the head has changed.

For safety (preventing accidental merges or auto-deployments), never submit `APPROVE` autonomously without explicit chat instructions.

To ensure clear communication of merge decisions, apply these rules:
- **State review results clearly:** Explicitly report in Japanese whether the PR is technically and qualitatively ready to merge (recommended for merge) in summaries or comments.
- **Execute upon user command:** Once the user explicitly permits/instructs "please post APPROVE" in the chat, the agent executes the `approve_pull_request` call (merging itself is the responsibility of a separate workflow).

## Re-review

1. If queue-triggered, verify candidate `reason = re-review-requested` and the target PR.
2. Read previous threads, developer replies, the current head, and diffs since the last review.
3. Verify the `isResolved` / `isOutdated` states of each thread and their status in the current head.
4. Verify only unresolved threads and critical regressions introduced by changes.
5. Verify changes in CI.
6. Process each thread according to the following mapping:

| Previous Thread State | Current Head State | Action |
| --- | --- | --- |
| unresolved | resolved in code | Briefly reply to the previous thread. Do not resolve the thread. |
| unresolved | partially resolved / not resolved / needs clarification | Reply with specific remaining reproduction conditions. |
| resolved / outdated | resolved in code | Do not post new comments. If necessary, report resolution in the PR summary only. |
| resolved / outdated | partially resolved / not resolved / needs clarification | Create a new unresolved thread via `post_inline_comment` on relevant lines in the current diff. |

New inline comments must state they are continuations of previous findings and describe specific remaining reproduction conditions in the current head. Do not duplicate replies to the original thread.

If there are no postable lines in the current diff, do not force stale comments; specify blocking and remaining conditions in the PR-level summary.

Re-run Snapshot Guard to verify the current head SHA and postable lines before posting new inline comments.

7. Do not introduce minor new findings that were not raised in the initial review.
8. Consider new inline comments only when a new blocking issue exists.

## Thread Follow-up

1. Identify the specified thread and current head.
2. Verify the `isResolved` / `isOutdated` states of the thread.
3. Read only the root comment of the thread, all replies, and corresponding diffs.
4. Determine if it is `resolved in code` / `partially resolved` / `not resolved` / `needs clarification`.
5. Do not mix new independent findings into the same thread.
6. Apply the same rules as the Re-review mapping:
   - If unresolved, reply via `reply_review_thread`. Do not resolve.
   - If resolved/outdated but issues remain, create a new thread via `post_inline_comment` on the current diff. Do not reply to the original thread.
   - If no postable lines exist on the current diff, address in the PR-level summary.

## Verdict

- `approve`: No blocking issues remain, main risks are covered by tests or explanation, and CI succeeded. Represents "technically and qualitatively ready to merge (recommended for merge)" and must be stated in the user report. Never post `APPROVE` without explicit user instruction.
- `request changes`: Blocking issues remain. Since Thread Owl has no REQUEST_CHANGES tool, report blocking comments and the verdict only.
- `comment only`: Insufficient information, mostly questions.
- `needs follow-up`: Mergeable, but has points to track in separate issues or subsequent PRs.

## User Report

```markdown
## Review result

- PR: ...
- mode: initial-review | re-review | thread-follow-up | summary-only
- reviewed head: <SHA>
- source of truth: remote PR snapshot
- local verification: isolated worktree | clean matching worktree | not performed
- local verification head: <SHA | n/a>
- CI head: <SHA | unknown>
- verdict: approve | request changes | comment only | needs follow-up
- CI: success | failure | unknown
- posted: new inline N, thread replies N, summary N, approve N
- blocking: N (new inline N / thread replies N)
- residual risk: ...

## Independent review delta

- Scope addressed by existing reviews: ...
- Dead angles checked this time: ...
- Duplicate candidates omitted: ...
```

If the queue was used, report resource URI, candidate reason, and subscription route. If there are no findings, report the reviewed scope and residual risks only.
