# review-raven Watch Tool Flow

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
  The review has reached `COMPLETED` or `BLOCKED`. Proceed to `get_review_threads` or similar.
- `START_NEW_WATCH`
  The current watch will not continue. Re-check with `get_copilot_review_status` if needed, then start a new watch.
  If `RATE_LIMITED`, `next_poll_seconds` indicates when to retry.
- `REAUTH_AND_START_NEW_WATCH`
  Re-acquire the token, then create a new watch.
- `CHECK_FAILURE`
  Inspect `last_error` / `failure_reason`, resolve the cause, then decide the next action.

## Notes

- `resource_uri` is the stable ID of a watch. Read/subscribe is available via the `review-raven://watch/{watch_id}` scheme (`RegisterWatchResources` / `SubscribeHandler` implemented).
- Watch state is persisted in SQLite, but the worker itself is memory-only. Active watches become `STALE` after a process restart.
- List operations return only watches belonging to the same `github_login`.

## Stateless Streamable HTTP (#111)

Since #111 (MCP `2026-07-28` migration, see [thread-owl#165](https://github.com/scottlz0310/thread-owl/issues/165)), the Streamable HTTP transport of `review-raven` is stateless: `StreamableHTTPOptions.Stateless: true`.

- No `Mcp-Session-Id` is minted or read; each request is served by a temporary per-request session (go-sdk requirement for negotiating protocol `2026-07-28` — stateful servers negotiate down to `2025-11-25`).
- The MCP server (`*mcp.Server`) is still a single shared long-lived instance; only the *session* is per-request, not the server or its registered tools/resources.
- GitHub clients continue to be created per tool call from the authenticated request headers, unrelated to session state.
- There is no session-to-login binding: per-request GitHub token authentication (via `middleware`) is the sole authorization boundary. This also removes the session-hijacking attack surface that the old `sessionLogins` map defended against — with no session to hijack, there is nothing to defend.
- `EventStore` / `Last-Event-ID` stream resumption is not used: `2026-07-28` does not support it.
- GET and DELETE requests return `405 Method Not Allowed` (stateless servers do not open a standalone SSE stream or accept session teardown).

Test considerations:

- `subscriptions/listen` for a legacy `copilot-review://watch/...` URI must still be rejected server-side (`SubscribeHandler` returns `ResourceNotFoundError`) — assert on the raw HTTP response, not `mcp.ClientSession.Subscribe()`. As of go-sdk v1.7.0, the client-side `subscriptionsListen()` silently discards the server's JSON-RPC error for this method (confirmed by wire capture: server returns `400` with the correct error, client returns `nil`). This is an upstream go-sdk client bug, not a review-raven authorization gap.
- Handler shutdown must stop the background watch manager.
- `notifications/resources/updated` delivery to `subscriptions/listen` streams and watch-status read fallback for hosts without notification support remain unaffected by the session-model change.
