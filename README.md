# copilot-review-mcp

[日本語](README.ja.md)

An MCP (Model Context Protocol) server that manages GitHub Copilot PR review cycles. Provides review request, completion detection, staleness detection, and thread reply/resolve through an **async watch + notification** model designed for LLM agents.

Built-in OAuth facade enables direct connections from MCP clients such as `claude.ai`, Claude Code, and VS Code via Streamable HTTP transport.

## Features

- **Async watch + notification** based. Start a background watch with `start_copilot_review_watch`, then track progress via the cheap `get_copilot_review_watch_status` read and `notifications/resources/updated` events.
- **GraphQL-based Copilot review request**. Avoids the issue where REST `requested_reviewers` silently ignores bot actors.
- **Per-thread review operations**. Reply, resolve, or reply+resolve individual threads using `PRRT_xxx` node IDs.
- **OAuth Authorization Code flow** for MCP clients, backed by a GitHub OAuth App.
- **Stateful sessions**. `Mcp-Session-Id` is bound to a GitHub login; sessions are automatically pruned on idle timeout.
- **SQLite-persisted watch state**. Active watches that survive a process restart are observable as `STALE`.

## Tools

| Tool | Description |
|---|---|
| `request_copilot_review` | Request a Copilot review on a PR |
| `get_copilot_review_status` | Fetch an instant snapshot from GitHub |
| `start_copilot_review_watch` | Start a background watch (recommended entry point) |
| `get_copilot_review_watch_status` | Cheap read of the current watch state |
| `list_copilot_review_watches` | List your active/recent watches |
| `cancel_copilot_review_watch` | Stop a watch |
| `get_pr_review_cycle_status` | Overall review cycle status and next-action recommendation |
| `get_review_threads` | List review threads (raw data; classification is left to the calling LLM) |
| `reply_to_review_thread` | Post a reply to a thread |
| `resolve_review_thread` | Mark a thread as resolved |
| `reply_and_resolve_review_thread` | Reply then resolve in sequence |
| `wait_for_copilot_review` | Legacy blocking wait (fallback) |

See [docs/usage.md](docs/usage.md) for setup and operation. Tool-level details are in [docs/watch-tools.md](docs/watch-tools.md) and [docs/skills/pr-review-cycle.md](docs/skills/pr-review-cycle.md).

## Quick Start (Docker)

Create a GitHub OAuth App and obtain its Client ID and Client Secret (callback URL: `http://localhost:8083/callback`).

```bash
docker run --rm -p 127.0.0.1:8083:8083 \
  -e GITHUB_CLIENT_ID=... \
  -e GITHUB_CLIENT_SECRET=... \
  -e BASE_URL=http://localhost:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

Register `http://localhost:8083/mcp` as an OAuth-enabled MCP server in your MCP client (e.g. Claude Code).

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_CLIENT_ID` | ✓ | — | GitHub OAuth App Client ID |
| `GITHUB_CLIENT_SECRET` | ✓ | — | GitHub OAuth App Client Secret |
| `BASE_URL` | | `http://localhost:8083` | Public URL of the MCP server |
| `GITHUB_OAUTH_SCOPES` | | `repo,user` | OAuth scopes |
| `MCP_PORT` | | `8083` | Listen port |
| `LOG_LEVEL` | | `info` | `debug` / `info` / `warn` / `error` |
| `SESSION_TTL_MIN` | | `10` | OAuth session TTL (minutes) |
| `TOKEN_CACHE_TTL_MIN` | | `30` | Token validation cache TTL (minutes) |
| `TOKEN_EXPIRES_IN_SEC` | | `7776000` | Token expiry advertised to clients (seconds) |
| `SQLITE_PATH` | | `/data/copilot-review.db` | Path to the watch-state database |
| `IN_PROGRESS_THRESHOLD_SEC` | | `30` | Grace period after a review request before treating the review as in-progress (seconds) |
| `MCP_SESSION_TIMEOUT_MIN` | | `30` | Idle timeout for Streamable HTTP sessions (minutes). After this period without any HTTP request from a client, the session is closed and subsequent requests with the stale `Mcp-Session-Id` get `404 session not found`. Set to `0` to never expire idle sessions — note that orphaned sessions (clients that disappear without sending `DELETE`) then remain in memory until process shutdown. |

## Local Development

Requires Go 1.26+.

```bash
# Run tests
go test ./...

# Build
go build -o bin/copilot-review-mcp ./cmd/server

# Build Docker image
docker build -t copilot-review-mcp:dev .
```

## History

This repository is a split-out of `services/copilot-review-mcp/` from [scottlz0310/Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker). Git history was not migrated. Related PRs and Issues from Mcp-Docker (`#47`, `#52`, `#53`, `#55`–`#58`, `#62`, `#63`–`#68`, `#74`–`#77`, `#92`, etc.) are referenced in the documents under `docs/`.

## License

MIT License — see [LICENSE](LICENSE).
