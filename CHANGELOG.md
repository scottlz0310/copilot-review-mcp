# Changelog

[日本語](CHANGELOG.ja.md)

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Phase B delegated background access — client core (PR-A)** for [Issue #29](https://github.com/scottlz0310/copilot-review-mcp/issues/29):
  - `internal/github/gateway_token_source.go` — `gatewayTokenSource` implements `oauth2.TokenSource` against the gateway's `POST /internal/v1/whoami` endpoint. Validates loopback host (`127.0.0.1` / `::1` / `localhost`) at construction; parses `expires_at` into `oauth2.Token.Expiry` so `oauth2.ReuseTokenSource` only re-resolves near expiry.
  - Sentinel errors `ErrGatewaySubjectGone` (404), `ErrGatewayUnauthorized` (401), `ErrGatewayLoopbackRequired` (403), `ErrGatewayUpstreamFailure` (502), `ErrGatewayBadRequest` (other 4xx), `ErrGatewayNonLoopback`. Mapping to `FailureReasonAuthExpired` / recovery hints is deferred to PR-B.
  - `internal/github/client.go` — new `NewClientWithTokenSource(ctx, ts, threshold)` for dynamic-token clients (no `invalidatingTransport`; activation deferred to PR-B).
  - `internal/tools/server.go` — new `BuilderOptions{GatewayClientFactory}` and `BuildStreamableHandlerWithOptions`. Existing `BuildStreamableHandler(db, threshold)` is unchanged.
  - `cmd/server/main.go` — opt-in via `COPILOT_REVIEW_GATEWAY_INTERNAL_URL` and `COPILOT_REVIEW_GATEWAY_INTERNAL_SECRET`. When unset, watch goroutines keep using `oauth2.StaticTokenSource` (no behavior change). Fail-closed: setting only one of the two env vars exits at startup.
  - **Subject** sent to the gateway is the authenticated GitHub login (per gateway docs).
  - **Limitation**: PoC requires client and gateway on the same host (loopback). Cross-container Docker Compose deployments are not supported in PR-A.

### Changed

- `watch.Options.ClientFactory` signature extended from `func(ctx, token string) ReviewDataFetcher` to `func(ctx, token, login string) ReviewDataFetcher`. Internal-only callers updated.

## [3.1.0] - 2026-05-09

### Added

- **Five new structured error types** in `internal/autherr` completing [Issue #21](https://github.com/scottlz0310/copilot-review-mcp/issues/21):
  - `PERMISSION_DENIED` — HTTP 403 responses (non-rate-limit)
  - `RATE_LIMITED` — primary (`*github.RateLimitError`) and secondary/abuse (`*github.AbuseRateLimitError`) rate limits; `retryable` and `safe_to_continue` are situation-dependent
  - `NOT_FOUND` — HTTP 404 responses
  - `VALIDATION_ERROR` — HTTP 400 / 422 responses
  - `TRANSIENT_UPSTREAM_ERROR` — HTTP 5xx responses (retryable)
- **`ClassifyGitHubError(err error) *autherr.AuthError`** in `internal/github/client.go` — a single entry point that classifies any GitHub API error (REST `*github.ErrorResponse`, `*github.RateLimitError`, `*github.AbuseRateLimitError`, shurcooL/githubv4 string-matched errors, and already-classified `*autherr.AuthError`) into the appropriate structured error type.
- `tryAuthResult` and `authErrString` in `internal/tools/auth_result.go` now call `ClassifyGitHubError` instead of `IsAuthError`, so all tool handlers automatically return structured errors for any of the 8 error types without additional per-handler changes.

### Changed

- Skill templates (`docs/skills/`) updated to use MCP server key `copilot-review` (was `copilot-review-mcp`) and `github` (was `github-mcp-server-docker`), matching the defaults used in mcp-docker / mcp-gateway setups (#23). Usage docs (`docs/usage.md`, `docs/usage.ja.md`) aligned to the same convention.

## [3.0.0] - 2026-05-06

### Removed

- **Standalone GitHub OAuth App flow removed entirely.** `internal/auth` package (handler, session, token cache) deleted.
- `AuthModeStandalone`, `AuthModeGateway` constants and `AuthMode` type removed from `internal/middleware`.
- `TokenInvalidator` interface and the `inv TokenInvalidator` parameter removed from `BuildStreamableHandler`.
- Environment variables removed: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `GITHUB_OAUTH_SCOPES`, `SESSION_TTL_MIN`, `TOKEN_CACHE_TTL_MIN`, `TOKEN_EXPIRES_IN_SEC`, `AUTH_MODE`.
- OAuth endpoints (`/.well-known/oauth-authorization-server`, `/authorize`, `/callback`, `/token`, `/register`) now return **410 Gone** with a migration message.

### Changed

- **mcp-gateway is now required** for authentication. The server trusts the `X-Authenticated-User` header and `Authorization: Bearer` token injected by the gateway.
- `BuildStreamableHandler(db, threshold)` — third argument removed.
- `middleware.Auth()` — no longer accepts a `TokenValidator` or `AuthMode`; gateway-only.
- Version bumped to `3.0.0` in the MCP server implementation metadata.

### Added

- `BIND_ADDR` environment variable (default `127.0.0.1`). Set to `0.0.0.0` in Docker so the container is reachable from mcp-gateway on the same network.

### Migration

If you were running with `AUTH_MODE=standalone` or `AUTH_MODE=gateway`:

1. Deploy [mcp-gateway](https://github.com/mcp-b/mcp-gateway) in front of this server.
2. Remove the following environment variables: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `AUTH_MODE`, `GITHUB_OAUTH_SCOPES`, `SESSION_TTL_MIN`, `TOKEN_CACHE_TTL_MIN`, `TOKEN_EXPIRES_IN_SEC` (see "Breaking Changes" above for the full list of removed variables).
3. Point your MCP client at the mcp-gateway URL. For stdio clients use [mcp-remote](https://github.com/geelen/mcp-remote).

## [2.5.0] - 2026-04-26

### Added

- Split `services/copilot-review-mcp/` from [scottlz0310/Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker) into a standalone repository
- Added the OAuth-enabled Streamable HTTP MCP server for Copilot review workflows
- Added async watch tools, review-thread reply/resolve tools, and the `pr-review-cycle` skill template
- Added SQLite-persisted watch state with stale-watch detection after process restart
- Added bilingual English/Japanese README, changelog, watch-tool docs, skill docs, and usage docs
- Added CI to test, scan, build, and publish Docker images to ghcr.io

### Notes

- This standalone repository preserves release continuity from the original `copilot-review-mcp` service work in Mcp-Docker; git history was not migrated.
- See `docs/` for related design context and migration history.
