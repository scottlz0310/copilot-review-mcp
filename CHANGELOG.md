# Changelog

[日本語](CHANGELOG.ja.md)

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `AUTH_MODE=gateway` support: when set to `gateway`, the auth middleware trusts the `X-Authenticated-User` header injected by an upstream proxy (e.g. mcp-gateway) and skips GitHub API token validation, eliminating double-validation overhead
- `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` are no longer required when `AUTH_MODE=gateway`
- `MCP_SESSION_TIMEOUT_MIN` environment variable to configure the Streamable HTTP session idle timeout. **Default changed to `0` (idle sessions are never closed)** to fix the `session not found` failure mode observed after ~30 minutes of idle time behind `mcp-gateway` (#14). Operators who prefer eviction can set a positive value (e.g. `1440` for 24h); see README for the memory-growth trade-off when clients disappear without sending `DELETE`.

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
