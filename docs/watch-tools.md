# copilot-review-mcp Watch Tool Flow

[日本語](watch-tools.ja.md)

The primary path in this repository is **async watch**, not blocking wait.
This document describes the recommended flow and the role of each tool as of issue #67.

## Recommended Flow

1. `get_copilot_review_status(owner, repo, pr)`
2. If status is not `COMPLETED` / `BLOCKED`, call `start_copilot_review_watch(owner, repo, pr)`
3. Continue other work
4. At the next decision point, call `get_copilot_review_watch_status(watch_id)`
5. If you lose track of `watch_id`, recover it with `list_copilot_review_watches(...)`
6. When the watch is no longer needed, call `cancel_copilot_review_watch(...)`

## Tool Roles

- `get_copilot_review_status`
  Fetches an instant snapshot from the GitHub API. Use before starting a watch, or to re-check after a watch reaches `STALE` / `TIMEOUT` / `CANCELLED`.
- `start_copilot_review_watch`
  Starts a background watch. If an active watch for the same PR already exists, it is reused idempotently.
- `get_copilot_review_watch_status`
  A cheap read returning local state. Prefers `watch_id`; falls back to `(owner, repo, pr)` lookup.
- `list_copilot_review_watches`
  Lists active/recent watches. Used for human debugging and watch recovery.
- `cancel_copilot_review_watch`
  Stops an unnecessary active watch.
- `wait_for_copilot_review`
  Legacy fallback. Use only when the host requires a blocking wait.

## Hints for LLM Agents

Watch tools return `recommended_next_action` and, when relevant, `next_poll_seconds`.

- `POLL_AFTER`
  The watch is still in progress. Re-check the same watch after `next_poll_seconds` seconds.
- `READ_REVIEW_THREADS`
  The Copilot review has reached `COMPLETED` or `BLOCKED`. Proceed to `get_review_threads` or similar.
- `START_NEW_WATCH`
  The current watch will not continue. Re-check with `get_copilot_review_status` if needed, then start a new watch.
  If `RATE_LIMITED`, `next_poll_seconds` indicates when to retry.
- `REAUTH_AND_START_NEW_WATCH`
  Re-acquire the token, then create a new watch.
- `CHECK_FAILURE`
  Inspect `last_error` / `failure_reason`, resolve the cause, then decide the next action.

## Notes

- `resource_uri` is the stable ID of a watch. Read/subscribe is available via the `copilot-review://watch/{watch_id}` scheme (`RegisterWatchResources` / `SubscribeHandler` implemented).
- Watch state is persisted in SQLite, but the worker itself is memory-only. Active watches become `STALE` after a process restart.
- List operations return only watches belonging to the same `github_login`.

## Stateful Session Foundation (#64)

Since #64, the Streamable HTTP transport of `copilot-review-mcp` is treated as stateful, not stateless.

- The `Mcp-Session-Id` issued on the first `initialize` is reused by subsequent requests.
- The MCP server is not recreated per request; a long-lived in-process server holds multiple stateful sessions.
- GitHub clients are not tied to the long-lived server; they are created per tool request from the authenticated request headers.
- `Mcp-Session-Id` is bound to a GitHub login; requests from a different login using the same session ID are rejected.
- Idle sessions are closed by the server-side timeout.
- `EventStore` uses a memory store, providing a foundation for future resource notifications and SSE replay.

Test considerations:

- Multiple requests after `initialize` must reuse the same stateful session and long-lived server.
- A different GitHub login using an existing `Mcp-Session-Id` must receive a 403 with a JSON error body.
- Login bindings for sessions that have disappeared from the server must be removed by periodic pruning.
- Handler shutdown must stop active sessions and the background watch manager.
- When resource notifications are added, `notifications/resources/updated` must be delivered to sessions with an active `resources/subscribe`, and watch-status read fallback must be maintained for hosts that do not support notifications.
