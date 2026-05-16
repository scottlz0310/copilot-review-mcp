# copilot-review-mcp

[日本語](README.ja.md)

An MCP (Model Context Protocol) server that manages GitHub Copilot PR review cycles. Provides review request, completion detection, staleness detection, and thread reply/resolve through an **async watch + notification** model designed for LLM agents.

> **v3.0.0 BREAKING CHANGE**: Standalone GitHub OAuth has been removed. This server must be deployed behind **[mcp-gateway](https://github.com/mcp-b/mcp-gateway)**, which handles authentication and injects `X-Authenticated-User` + `Authorization` headers.

## Features

- **Async watch + notification** based. Start a background watch with `start_copilot_review_watch`, then track progress via the cheap `get_copilot_review_watch_status` read and `notifications/resources/updated` events.
- **GraphQL-based Copilot review request**. Avoids the issue where REST `requested_reviewers` silently ignores bot actors.
- **Per-thread review operations**. Reply, resolve, or reply+resolve individual threads using `PRRT_xxx` node IDs.
- **mcp-gateway integration** for authentication. The gateway handles OAuth and injects verified identity headers.
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

## Quick Start (Docker + mcp-gateway)

This server requires [mcp-gateway](https://github.com/mcp-b/mcp-gateway) to handle authentication.

```bash
# Start copilot-review-mcp (internal, not exposed directly)
docker run --rm -p 127.0.0.1:8083:8083 \
  -e BIND_ADDR=0.0.0.0 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

Configure mcp-gateway to proxy the internal address of this server as seen **from the gateway** (e.g., `http://copilot-review-mcp:8083` on a shared Docker network, or `http://host.docker.internal:8083` on Docker Desktop). See [mcp-gateway docs](https://github.com/mcp-b/mcp-gateway).

**For stdio clients** (Claude Desktop, etc.) use [mcp-remote](https://github.com/geelen/mcp-remote):

```json
{
  "mcpServers": {
    "copilot-review": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://your-gateway-url/mcp"]
    }
  }
}
```

See [docs/usage.md](docs/usage.md) for the full setup guide.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `MCP_PORT` | | `8083` | Listen port |
| `BIND_ADDR` | | `127.0.0.1` | Bind address. Use `0.0.0.0` in Docker so the container is reachable from mcp-gateway on the same network |
| `LOG_LEVEL` | | `info` | `debug` / `info` / `warn` / `error` |
| `SQLITE_PATH` | | `/data/copilot-review.db` | Path to the watch-state database |
| `IN_PROGRESS_THRESHOLD_SEC` | | `30` | Grace period after a review request before treating the review as in-progress (seconds) |
| `MCP_SESSION_TIMEOUT_MIN` | | `0` | Idle timeout for Streamable HTTP sessions (minutes). After this period without any HTTP request from a client, the session is closed and subsequent requests with the stale `Mcp-Session-Id` get `404 session not found`. The default `0` disables idle eviction so long-lived clients (Claude Code / IDE / `mcp-gateway`) do not hit the failure mode in #14. Trade-off: orphaned sessions (clients that disappear without sending `DELETE`) remain in memory until process shutdown — set a positive value (e.g. `1440` for 24h) if memory growth is a concern. |
| `COPILOT_REVIEW_GATEWAY_INTERNAL_URL` | | _(unset)_ | **Phase B** — Full URL of the mcp-gateway internal whoami endpoint (e.g. `http://127.0.0.1:8080/internal/v1/whoami`). Must be a loopback address. Set together with `COPILOT_REVIEW_GATEWAY_INTERNAL_SECRET` or leave both unset. |
| `COPILOT_REVIEW_GATEWAY_INTERNAL_SECRET` | | _(unset)_ | **Phase B** — Shared bearer secret for the gateway internal API. Must be set together with `COPILOT_REVIEW_GATEWAY_INTERNAL_URL`. |

**Removed in v3.0.0**:`GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `BASE_URL`, `GITHUB_OAUTH_SCOPES`, `SESSION_TTL_MIN`, `TOKEN_CACHE_TTL_MIN`, `TOKEN_EXPIRES_IN_SEC`, `AUTH_MODE`.

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
