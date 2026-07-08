# Changelog

[日本語](CHANGELOG.ja.md)

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `diagnose_github_token` MCP tool — reports the current request's token login and OAuth scopes (parsed from GitHub's `X-OAuth-Scopes` response header on `GET /user`), without ever returning the raw token. Added to help diagnose cases where read operations succeed but a write operation (e.g. `reply_and_resolve_review_thread`) fails with `PERMISSION_DENIED` due to insufficient token scope. ([Issue #89](https://github.com/scottlz0310/review-raven/issues/89))

- `spike-request-scoped-reauth.md` / `.ja.md` — spike investigation into frequent `REAUTH_REQUIRED` on request-scoped (non-watch) tool calls such as `get_review_threads`. Root cause: mcp-gateway builtin mode discards the GitHub provider access token at token exchange, so `EnsureFreshAccessTokenForSubject` hands the gateway RS256 JWT to review-raven as the Bearer token and GitHub rejects it with 401 on every call. Fix belongs to mcp-gateway (filed as [mcp-gateway#188](https://github.com/scottlz0310/mcp-gateway/issues/188)); no review-raven code change required. ([Issue #87](https://github.com/scottlz0310/review-raven/issues/87))

- Default credentials and dynamic default user resolution for `auth=none` routes: When `X-Authenticated-User` and `Authorization` headers are missing (such as in `auth=none` proxy routes), the server can fall back to default values. The default user is dynamically resolved by calling GitHub's `GET /user` endpoint using the `GITHUB_PERSONAL_ACCESS_TOKEN` environment variable if `REVIEW_RAVEN_DEFAULT_USER` is not explicitly set. ([Issue #80](https://github.com/scottlz0310/review-raven/issues/80))

- `docs/skills/thread-owl-review-cycle.md` / `.ja.md` — new skill for the thread-owl reviewed-side cycle. Reads review threads via paginated GraphQL, classifies and fixes, replies and resolves, then posts `@thread-owl re-review requested` as the re-review path. The reviewed-side cycle ends there; the next reviewer-side cycle is triggered by thread-owl's webhook. ([Issue #71](https://github.com/scottlz0310/review-raven/issues/71))

### Fixed

- `ClassifyGitHubError` now recognizes GitHub GraphQL's `"Resource not accessible by integration"` message (returned with HTTP 200 when a GitHub App installation token lacks the required repository permission) and classifies it as `PERMISSION_DENIED`, instead of leaking the raw Go error string to MCP tool callers. Found while investigating [Issue #89](https://github.com/scottlz0310/review-raven/issues/89). ([Issue #92](https://github.com/scottlz0310/review-raven/issues/92))

### Changed

- `pr-review-cycle` and `review-raven-thread-owl-cycle` skill templates now fail closed before reading PR comment bodies. Only comments from `scottlz0310-user`, `copilot-pull-request-reviewer[bot]`, and `thread-owl[bot]` are trusted; any other or unverifiable author stops automation for human escalation without quoting or acting on the comment body.
- **Renamed `docs/skills/thread-owl-review-cycle.md` / `.ja.md` to `docs/skills/review-raven-thread-owl-cycle.md` / `.ja.md`**, and updated the `name` frontmatter and cross-reference links in `pr-review-cycle.md` / `.ja.md` accordingly. The old name (`thread-owl-*`) was easily confused with thread-owl's own reviewer-side skill (`thread-owl-pr-reviewer`); prefixing with `review-raven-` makes the owning repository and reviewed-side role unambiguous. Installed personal skill copies (`~/.claude/skills/`, `~/.gemini/antigravity-cli/skills/`) need to be renamed manually to pick up this change. ([Issue #96](https://github.com/scottlz0310/review-raven/issues/96))
- `docs/skills/thread-owl-review-cycle.md` / `.ja.md` — Added verification of thread-owl's Verdict comment (`## @thread-owl Review Verdict: APPROVED` with `Status: READY_TO_MERGE`) to Phase 7, requiring its `Reviewed HEAD SHA` to match the current PR HEAD SHA before proceeding to the Phase 8 merge gate (replaces the previous ambiguous "SHA approved in the latest review" condition; blocks with `AWAITING_THREAD_OWL_VERDICT` when the comment is missing or mismatched). Also fixed a deadlock in Phase U6: `need_re_review` now triggers a re-review request for any fix commit, including `trivial`-only fixes, since even a trivial fix updates the PR HEAD and would otherwise leave thread-owl's existing Verdict comment permanently stale. ([Issue #94](https://github.com/scottlz0310/review-raven/issues/94))
- `docs/skills/thread-owl-review-cycle.ja.md` / `.md` — Added a PR HEAD sync gate to prevent proceeding with reply, resolve, and re-review request before local fixes are pushed. Added `expected_head` tracking to re-review request comment annotations and summary comments. Added PR HEAD SHA verification gate to Phase 8 merge conditions (blocks merge with `APPROVED_HEAD_MISMATCH` on mismatch). ([Issue #84](https://github.com/scottlz0310/review-raven/issues/84))
- `docs/skills/pr-review-cycle.ja.md` / `.md` — Added the same PR HEAD sync gate and Phase 8 merge gate SHA verification to the Copilot review cycle skill. ([Issue #84](https://github.com/scottlz0310/review-raven/issues/84))
- `docs/skills/thread-owl-review-cycle.md` / `.ja.md` — updated to address all unresolved review comments regardless of the author. Updated to use `review-raven` MCP tools (`get_review_threads`, `reply_and_resolve_review_thread`, etc.) as the primary method, with the `gh` CLI (GraphQL/REST API) as a fallback when MCP tools are unavailable. Documented detailed procedures for fetching, replying to, tracking progress, and re-verifying non-thread comments like review bodies and PR comments (issue comments). Added mechanism to persist and recover processed non-thread comment IDs in annotations (`handled_comments`) to avoid duplicate processing. Added update instructions for installed skill templates. ([Issue #76](https://github.com/scottlz0310/review-raven/issues/76))
- `docs/skills/pr-review-cycle.md` / `.ja.md` — added **Phase 1S** (subscription-based wait using `mcp-resource-subscriber --json`) as an alternative to the MCP polling Phase 1. Updated Overall Flow diagram to reflect the two-path entry. ([Issue #74](https://github.com/scottlz0310/review-raven/issues/74))
- `docs/skills/pr-review-cycle.md` / `.ja.md` — explicitly scoped to **Copilot review only**. Phase 6 `REQUEST_REREVIEW` reverted to `request_copilot_review` + Copilot watch loop (as originally designed). Added scope callout and `## See Also` link to `thread-owl-review-cycle`. ([Issue #71](https://github.com/scottlz0310/review-raven/issues/71))
- `docs/architecture.md` / `.ja.md` — added Re-review request flow section documenting the responsibility boundary between review-raven (posts comment), thread-owl (webhook → queue), and mcp-resource-subscriber (subscription bridge). ([Issue #69](https://github.com/scottlz0310/review-raven/issues/69))

### Removed

- **Legacy `copilot-review://` URI scheme and `COPILOT_REVIEW_*` env vars removed** ([Issue #66](https://github.com/scottlz0310/review-raven/issues/66)):
  - `SubscribeHandler` now returns `ResourceNotFoundError` for `copilot-review://watch/...` URIs instead of silently succeeding (ghost subscriptions). Re-request any active watches.
  - `parseWatchIDFromURI()` no longer accepts `copilot-review://watch/{id}`; only `review-raven://watch/{id}` is valid.
  - `COPILOT_REVIEW_GATEWAY_INTERNAL_URL` / `COPILOT_REVIEW_GATEWAY_INTERNAL_SECRET` fallback removed from `loadConfig()`. Set `REVIEW_RAVEN_GATEWAY_INTERNAL_URL` / `REVIEW_RAVEN_GATEWAY_INTERNAL_SECRET` directly.

## [0.1.0] - 2026-06-08

First release as **review-raven** — product identity rename and architecture docs.
This version restarts the version sequence from `0.1.0`. The pre-rename lineage (`copilot-review-mcp` v2.5.0 – v3.2.0) is preserved below as legacy history.

### Changed

- **Repository renamed from `copilot-review-mcp` to `review-raven`** ([Issue #63](https://github.com/scottlz0310/review-raven/issues/63)):
  - Go module path updated: `github.com/scottlz0310/copilot-review-mcp` → `github.com/scottlz0310/review-raven`
  - MCP server implementation name updated: `"copilot-review-mcp"` → `"review-raven"`; version restarted at `0.1.0`
  - Resource URI scheme updated: `copilot-review://watch/{id}` → `review-raven://watch/{id}`
  - MCP client configuration key updated: `"copilot-review"` → `"review-raven"` (tool prefix: `mcp__review-raven__*`)
  - Environment variables renamed: `COPILOT_REVIEW_GATEWAY_INTERNAL_URL/SECRET` → `REVIEW_RAVEN_GATEWAY_INTERNAL_URL/SECRET` (old names remain as fallback for backward compatibility)
  - Default SQLite path updated: `/data/copilot-review.db` → `/data/review-raven.db` (overridable via `SQLITE_PATH`)
  - Docker image/container/volume names updated: `copilot-review-mcp` → `review-raven`, `copilot-review-data` → `review-raven-data`
  - `.env.template` updated with canonical `REVIEW_RAVEN_*` variable names and a migration/compatibility section for legacy `COPILOT_REVIEW_*` names
  - Legacy URI scheme `copilot-review://watch/{id}` accepted as a deprecated read/subscribe alias; new URIs use `review-raven://watch/{id}` only

### Added

- `docs/architecture.md` / `docs/architecture.ja.md` — new document defining the reviewed-side MCP server role and responsibility boundaries with Thread Owl, mcp-resource-subscriber, and github-mcp-server. Includes Migration / Compatibility section covering URI scheme, environment variable, and tool name compatibility.

---

## Pre-rename history (copilot-review-mcp)

The entries below document the `copilot-review-mcp` era (git tags `v2.5.0` – `v3.2.0`).
Version numbers in this section refer to that lineage and are unrelated to the `review-raven` versioning above.

### [3.2.0] - 2026-05-18

#### Added

- **Phase B delegated background access — gateway integration tests (PR-C)** for [Issue #40](https://github.com/scottlz0310/review-raven/issues/40) (part of [Issue #29](https://github.com/scottlz0310/review-raven/issues/29)):
  - `internal/watch/gateway_integration_test.go` exercises the full chain `gatewayTokenSource → oauth2.ReuseTokenSource → oauth2.Transport → *ghclient.Client → watch.Manager.pollOnce` end-to-end using a fake `POST /internal/v1/whoami` server and a minimal fake GitHub REST surface, mirroring the production wiring in `cmd/server/main.go`'s `buildGatewayClientFactory`.
  - Covers six scenarios: happy path (200 → `COMPLETED`), subject gone (404 → `FAILED`/`AUTH_EXPIRED` with re-seed hint), rotation failed (502/rotation_failed → `FAILED`/`AUTH_EXPIRED` with refresh-rejected hint), single upstream failure stays `WATCHING`, consecutive upstream failures escalate to `FAILED`/`AUTH_EXPIRED` with the consecutive-polls hint, and token rotation visible to GitHub (`oauth2.ReuseTokenSource` re-fetches a rotated token across polls).
  - Distinct from existing `manager_test.go` cases that feed sentinel errors directly through `fakeFetcher`; these tests fail closed if the real gateway-backed wiring regresses (e.g., factory refactor breaks the chain).
- **Phase B delegated background access — client core (PR-A)** for [Issue #29](https://github.com/scottlz0310/review-raven/issues/29):
  - `internal/github/gateway_token_source.go` — `gatewayTokenSource` implements `oauth2.TokenSource` against the gateway's `POST /internal/v1/whoami` endpoint. Validates loopback host (`127.0.0.1` / `::1` / `localhost`) at construction; parses `expires_at` into `oauth2.Token.Expiry` so `oauth2.ReuseTokenSource` only re-resolves near expiry.
  - Sentinel errors `ErrGatewaySubjectGone` (404), `ErrGatewayUnauthorized` (401), `ErrGatewayLoopbackRequired` (403), `ErrGatewayUpstreamFailure` (502), `ErrGatewayBadRequest` (other 4xx), `ErrGatewayNonLoopback`. Mapping to `FailureReasonAuthExpired` / recovery hints is deferred to PR-B.
  - `internal/github/client.go` — new `NewClientWithTokenSource(ctx, ts, threshold)` for dynamic-token clients (no `invalidatingTransport`; activation deferred to PR-B).
  - `internal/tools/server.go` — new `BuilderOptions{GatewayClientFactory}` and `BuildStreamableHandlerWithOptions`. Existing `BuildStreamableHandler(db, threshold)` is unchanged.
  - `cmd/server/main.go` — opt-in via `REVIEW_RAVEN_GATEWAY_INTERNAL_URL` and `REVIEW_RAVEN_GATEWAY_INTERNAL_SECRET`. When unset, watch goroutines keep using `oauth2.StaticTokenSource` (no behavior change). Fail-closed: setting only one of the two env vars exits at startup.
  - **Subject** sent to the gateway is the authenticated GitHub login (per gateway docs).
  - **Limitation**: PoC requires client and gateway on the same host (loopback). Cross-container Docker Compose deployments are not supported in PR-A.

#### Changed

- `watch.Options.ClientFactory` signature extended from `func(ctx, token string) ReviewDataFetcher` to `func(ctx, token, login string) ReviewDataFetcher`. Internal-only callers updated.
- **Phase B PR-A review feedback (PR #30 Copilot review)**:
  - `gatewayTokenSource.Token()` now derives its request context from a configurable parent (`GatewayTokenSourceConfig.Context`) and a single `defaultGatewayTimeout` constant (10s). Cancelling the watch / shutting down the server now propagates into in-flight whoami calls.
  - Non-200 responses now drain a bounded portion of the body before returning the mapped sentinel error, letting `net/http` reuse the underlying keep-alive connection.
  - New `ghclient.ValidateGatewayEndpoint(url, secret)` and `loadConfig` startup check: malformed URL, non-http(s) scheme, non-loopback host, or empty secret now fail-fast at startup instead of silently degrading every watch to static tokens.
  - `buildGatewayClientFactory` now constructs a single shared `*http.Client` once and passes it via `GatewayTokenSourceConfig.HTTPClient` to every per-watch token source (transport / idle-connection pool reuse). Runtime fallback to static tokens (only reachable on empty GitHub login) is now logged at `slog.Error`.
  - `GatewayTokenSourceConfig.HTTPClient` documentation clarified: the token source itself must be per-subject, but the underlying `*http.Client` / `http.Transport` is designed for concurrent reuse and should be shared across watches.

### [3.1.0] - 2026-05-09

#### Added

- **Five new structured error types** in `internal/autherr` completing [Issue #21](https://github.com/scottlz0310/review-raven/issues/21):
  - `PERMISSION_DENIED` — HTTP 403 responses (non-rate-limit)
  - `RATE_LIMITED` — primary (`*github.RateLimitError`) and secondary/abuse (`*github.AbuseRateLimitError`) rate limits; `retryable` and `safe_to_continue` are situation-dependent
  - `NOT_FOUND` — HTTP 404 responses
  - `VALIDATION_ERROR` — HTTP 400 / 422 responses
  - `TRANSIENT_UPSTREAM_ERROR` — HTTP 5xx responses (retryable)
- **`ClassifyGitHubError(err error) *autherr.AuthError`** in `internal/github/client.go` — a single entry point that classifies any GitHub API error (REST `*github.ErrorResponse`, `*github.RateLimitError`, `*github.AbuseRateLimitError`, shurcooL/githubv4 string-matched errors, and already-classified `*autherr.AuthError`) into the appropriate structured error type.
- `tryAuthResult` and `authErrString` in `internal/tools/auth_result.go` now call `ClassifyGitHubError` instead of `IsAuthError`, so all tool handlers automatically return structured errors for any of the 8 error types without additional per-handler changes.

#### Changed

- Skill templates (`docs/skills/`) updated to use MCP server key `copilot-review` (was `review-raven`) and `github` (was `github-mcp-server-docker`), matching the defaults used in mcp-docker / mcp-gateway setups (#23). Usage docs (`docs/usage.md`, `docs/usage.ja.md`) aligned to the same convention.

### [3.0.0] - 2026-05-06

#### Removed

- **Standalone GitHub OAuth App flow removed entirely.** `internal/auth` package (handler, session, token cache) deleted.
- `AuthModeStandalone`, `AuthModeGateway` constants and `AuthMode` type removed from `internal/middleware`.
- `TokenInvalidator` interface and the `inv TokenInvalidator` parameter removed from `BuildStreamableHandler`.
- Environment variables removed: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `GITHUB_OAUTH_SCOPES`, `SESSION_TTL_MIN`, `TOKEN_CACHE_TTL_MIN`, `TOKEN_EXPIRES_IN_SEC`, `AUTH_MODE`.
- OAuth endpoints (`/.well-known/oauth-authorization-server`, `/authorize`, `/callback`, `/token`, `/register`) now return **410 Gone** with a migration message.

#### Changed

- **mcp-gateway is now required** for authentication. The server trusts the `X-Authenticated-User` header and `Authorization: Bearer` token injected by the gateway.
- `BuildStreamableHandler(db, threshold)` — third argument removed.
- `middleware.Auth()` — no longer accepts a `TokenValidator` or `AuthMode`; gateway-only.
- Version bumped to `3.0.0` in the MCP server implementation metadata.

#### Added

- `BIND_ADDR` environment variable (default `127.0.0.1`). Set to `0.0.0.0` in Docker so the container is reachable from mcp-gateway on the same network.

#### Migration

If you were running with `AUTH_MODE=standalone` or `AUTH_MODE=gateway`:

1. Deploy [mcp-gateway](https://github.com/mcp-b/mcp-gateway) in front of this server.
2. Remove the following environment variables: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `AUTH_MODE`, `GITHUB_OAUTH_SCOPES`, `SESSION_TTL_MIN`, `TOKEN_CACHE_TTL_MIN`, `TOKEN_EXPIRES_IN_SEC` (see "Breaking Changes" above for the full list of removed variables).
3. Point your MCP client at the mcp-gateway URL. For stdio clients use [mcp-remote](https://github.com/geelen/mcp-remote).

### [2.5.0] - 2026-04-26

#### Added

- Split `services/review-raven/` from [scottlz0310/Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker) into a standalone repository
- Added the OAuth-enabled Streamable HTTP MCP server for Copilot review workflows
- Added async watch tools, review-thread reply/resolve tools, and the `pr-review-cycle` skill template
- Added SQLite-persisted watch state with stale-watch detection after process restart
- Added bilingual English/Japanese README, changelog, watch-tool docs, skill docs, and usage docs
- Added CI to test, scan, build, and publish Docker images to ghcr.io

#### Notes

- This standalone repository preserves release continuity from the original `review-raven` service work in Mcp-Docker; git history was not migrated.
- See `docs/` for related design context and migration history.

[Unreleased]: https://github.com/scottlz0310/review-raven/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/scottlz0310/review-raven/releases/tag/v0.1.0
