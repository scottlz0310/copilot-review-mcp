# Changelog

[日本語](CHANGELOG.ja.md)

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
