# Usage

[日本語](usage.ja.md)

This guide covers the basic setup needed to run `copilot-review-mcp` as an MCP server:

- Architecture overview (mcp-gateway required)
- Docker start, stop, and logs
- Connecting MCP clients via mcp-gateway
- `pr-review-cycle` skill installation

For the tool-level flow, see [watch-tools.md](watch-tools.md). For the skill template itself, see [skills/pr-review-cycle.md](skills/pr-review-cycle.md).

> **v3.0.0 BREAKING CHANGE**: Standalone OAuth has been removed. mcp-gateway is now required.

## Architecture

```
MCP Client (Claude Code / Claude Desktop / VS Code)
    │
    │  HTTPS / OAuth  (handled by mcp-gateway)
    ▼
mcp-gateway  ──►  X-Authenticated-User + Authorization headers
    │
    │  HTTP (internal only)
    ▼
copilot-review-mcp  :8083
    │
    │  SQLite
    ▼
/data/copilot-review.db
```

`copilot-review-mcp` trusts the headers injected by mcp-gateway and never performs OAuth directly.

## 1. Set up mcp-gateway

Follow the [mcp-gateway documentation](https://github.com/mcp-b/mcp-gateway) to deploy and configure the gateway.

Point one of its upstream routes at `http://localhost:8083` (or the internal hostname where you run `copilot-review-mcp`).

## 2. Run copilot-review-mcp with Docker

### Pull the published image

```bash
docker pull ghcr.io/scottlz0310/copilot-review-mcp:latest
```

### Build locally

```bash
docker build -t copilot-review-mcp:dev .
```

### Start the container

Published image:

```bash
docker run -d --name copilot-review-mcp \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

Local image:

```bash
docker run -d --name copilot-review-mcp \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  copilot-review-mcp:dev
```

Optional environment variables (all have defaults):

```env
MCP_PORT=8083
LOG_LEVEL=info
SQLITE_PATH=/data/copilot-review.db
IN_PROGRESS_THRESHOLD_SEC=30
MCP_SESSION_TIMEOUT_MIN=0
```

### Check health

```bash
curl http://127.0.0.1:8083/health
```

PowerShell:

```powershell
Invoke-RestMethod http://127.0.0.1:8083/health
```

Expected response:

```json
{"status":"ok"}
```

### View logs

```bash
docker logs -f copilot-review-mcp
```

### Stop, start again, and remove

```bash
docker stop copilot-review-mcp
docker start copilot-review-mcp
docker rm -f copilot-review-mcp
```

The named volume keeps the SQLite watch-state database:

```bash
docker volume ls --filter name=copilot-review-data
```

Remove it only when you intentionally want to delete local state:

```bash
docker volume rm copilot-review-data
```

## 3. Configure an MCP client

### Streamable HTTP clients (Claude Code, VS Code)

Point the client at your mcp-gateway URL:

```json
{
  "mcpServers": {
    "copilot-review-mcp": {
      "type": "http",
      "url": "https://your-gateway-url/mcp"
    }
  }
}
```

Some clients use `servers` instead of `mcpServers`, or `streamable-http` instead of `http`. Keep the URL unchanged.

### stdio clients (Claude Desktop, etc.) via mcp-remote

Use [mcp-remote](https://github.com/geelen/mcp-remote) as a bridge:

```json
{
  "mcpServers": {
    "copilot-review-mcp": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://your-gateway-url/mcp"]
    }
  }
}
```

When the client first connects, mcp-gateway handles the OAuth authorization flow. Sign in with GitHub.

## 4. Install the `pr-review-cycle` skill

The repository contains skill templates, but they must be copied into your AI agent's local skill directory before use.

### Codex

PowerShell:

```powershell
$skillDir = "$env:USERPROFILE\.codex\skills\pr-review-cycle"
New-Item -ItemType Directory -Force $skillDir
Copy-Item docs\skills\pr-review-cycle.md "$skillDir\SKILL.md"
```

POSIX shell:

```bash
mkdir -p ~/.codex/skills/pr-review-cycle
cp docs/skills/pr-review-cycle.md ~/.codex/skills/pr-review-cycle/SKILL.md
```

### Claude-style skill directory

```bash
mkdir -p ~/.claude/skills/pr-review-cycle
cp docs/skills/pr-review-cycle.md ~/.claude/skills/pr-review-cycle/SKILL.md
```

Use the Japanese template if preferred:

```bash
cp docs/skills/pr-review-cycle.ja.md ~/.claude/skills/pr-review-cycle/SKILL.md
```

After copying, edit the placeholders in the skill if your client exposes different tool prefixes:

| Placeholder | Meaning |
|---|---|
| `{CRM}` | `copilot-review-mcp` tools |
| `{GH}` | GitHub MCP tools used for comments, CI, and PR operations |

## 5. Basic review-cycle usage

Prerequisites:

- `copilot-review-mcp` is running and accessible via mcp-gateway.
- A GitHub MCP server or GitHub connector is also available.
- The current repository has an open PR.

Typical instruction to the agent:

```text
$pr-review-cycle
```

The skill should:

1. Check or request a Copilot review.
2. Wait for completion with async watch.
3. Fetch review threads.
4. Classify comments.
5. Apply accepted changes.
6. Reply and resolve threads when remote side effects are allowed.
7. Check CI and coverage.

Merging remains a separate explicit user decision.

## Troubleshooting

### `missing_proxy_identity` (401)

The request reached `copilot-review-mcp` without going through mcp-gateway, or the gateway is not configured to inject `X-Authenticated-User`. Ensure all traffic passes through mcp-gateway.

### `session_user_mismatch`

The same MCP session ID was reused with a different GitHub login. Clear the MCP client's cached session or reconnect.

### Container starts but `/health` fails

Check logs:

```bash
docker logs copilot-review-mcp
```

Common causes are a port already in use or a bad `SQLITE_PATH`.
