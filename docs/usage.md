# Usage

[日本語](usage.ja.md)

This guide covers the basic setup needed to run `review-raven` as an MCP server:

- Architecture overview (mcp-gateway required)
- Docker start, stop, and logs
- Connecting MCP clients via mcp-gateway
- Mcp-Docker 経由のレビュー対応 skill の配置

ツール単位の流れは [watch-tools.md](watch-tools.md)、skill の所在と配置は [案内](skills/README.md)を参照してください。

> **mcp-gateway required**: Standalone OAuth is not supported. All traffic must pass through mcp-gateway. Migrating from `copilot-review-mcp`? See [architecture.md — Migration / Compatibility](architecture.md#migration--compatibility).

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
review-raven  :8083
    │
    │  SQLite
    ▼
/data/review-raven.db
```

`review-raven` trusts the headers injected by mcp-gateway and never performs OAuth directly.

## 1. Set up mcp-gateway

Follow the [mcp-gateway documentation](https://github.com/mcp-b/mcp-gateway) to deploy and configure the gateway.

Point one of its upstream routes at the address reachable **from the gateway** (e.g., `http://review-raven:8083` when both run on the same Docker network, or `http://host.docker.internal:8083` on Docker Desktop).

## 2. Run review-raven with Docker

### Pull the published image

```bash
docker pull ghcr.io/scottlz0310/review-raven:latest
```

### Build locally

```bash
docker build -t review-raven:dev .
```

### Start the container

Published image:

```bash
docker run -d --name review-raven \
  -p 127.0.0.1:8083:8083 \
  -e BIND_ADDR=0.0.0.0 \
  -v review-raven-data:/data \
  ghcr.io/scottlz0310/review-raven:latest
```

Local image:

```bash
docker run -d --name review-raven \
  -p 127.0.0.1:8083:8083 \
  -e BIND_ADDR=0.0.0.0 \
  -v review-raven-data:/data \
  review-raven:dev
```

Optional environment variables (all have defaults):

```env
MCP_PORT=8083
BIND_ADDR=127.0.0.1   # Use 0.0.0.0 in Docker so mcp-gateway (other container) can reach this server
LOG_LEVEL=info
SQLITE_PATH=/data/review-raven.db
IN_PROGRESS_THRESHOLD_SEC=30
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
docker logs -f review-raven
```

### Stop, start again, and remove

```bash
docker stop review-raven
docker start review-raven
docker rm -f review-raven
```

The named volume keeps the SQLite watch-state database:

```bash
docker volume ls --filter name=review-raven-data
```

Remove it only when you intentionally want to delete local state:

```bash
docker volume rm review-raven-data
```

## 3. Configure an MCP client

### Streamable HTTP clients (Claude Code, VS Code)

Point the client at your mcp-gateway URL:

```json
{
  "mcpServers": {
    "review-raven": {
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
    "review-raven": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://your-gateway-url/mcp"]
    }
  }
}
```

When the client first connects, mcp-gateway handles the OAuth authorization flow. Sign in with GitHub.

## 4. レビュー対応 skill を配置する

`review-raven-thread-owl-cycle` の正本は [Mcp-Docker の SKILL.md](https://github.com/scottlz0310/Mcp-Docker/blob/main/skills/review-raven-thread-owl-cycle/SKILL.md) です。収蔵・配置は Mcp-Docker が管理します。

[Mcp-Docker のリリース](https://github.com/scottlz0310/Mcp-Docker/releases)から skill サブコマンドを備えた v2.18.0 以降を導入し、次を実行します。

```shell
mcp-docker skill install
mcp-docker skill status
```

配置対象は Claude / Copilot / Codex / Antigravity CLI です。対象の絞り込みや更新方法は [Mcp-Docker の利用案内](https://github.com/scottlz0310/Mcp-Docker#readme)を参照してください。手動配置済みの skill は管理外として扱われ、上書き時に確認されます。

このリポジトリの skill テンプレートは廃止しました。`review-raven-thread-owl-cycle` は日本語の正本に統一し、英語版と未使用の Copilot 専用 `pr-review-cycle` 日英版は削除しました。Copilot 用 MCP ツールの利用手順は[ツールドキュメント](watch-tools.md)を参照してください。

## 5. 基本的なレビュー対応

thread-owl のレビューコメントを受けた実装側エージェントへ、対象 PR を指定して依頼します。

```text
$review-raven-thread-owl-cycle owner/repo#123
```

必要な接続と修正・返信・再レビュー依頼の手順は、[skill の正本](https://github.com/scottlz0310/Mcp-Docker/blob/main/skills/review-raven-thread-owl-cycle/SKILL.md)に従ってください。独立 reviewer の起動とマージは別の操作であり、マージにはユーザーの明示許可が必要です。

## Troubleshooting

### `missing_proxy_identity` (401)

The request reached `review-raven` without going through mcp-gateway, or the gateway is not configured to inject `X-Authenticated-User`. Ensure all traffic passes through mcp-gateway.

### `session_user_mismatch`

The same MCP session ID was reused with a different GitHub login. Clear the MCP client's cached session or reconnect.

### Container starts but `/health` fails

Check logs:

```bash
docker logs review-raven
```

Common causes are a port already in use or a bad `SQLITE_PATH`.
