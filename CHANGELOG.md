# Changelog

[日本語](CHANGELOG.ja.md)

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
2. Remove `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `AUTH_MODE` from your environment.
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
