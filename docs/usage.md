# Usage

[日本語](usage.ja.md)

This guide covers the basic setup needed to run `copilot-review-mcp` as an MCP server:

- GitHub OAuth App setup
- Docker build, start, stop, and logs
- Basic MCP client configuration
- `pr-review-cycle` skill installation

For the tool-level flow, see [watch-tools.md](watch-tools.md). For the skill template itself, see [skills/pr-review-cycle.md](skills/pr-review-cycle.md).

## 1. Create a GitHub OAuth App

Create one OAuth App for each public server URL you use.

For local Docker usage:

| Field | Value |
|---|---|
| Application name | `copilot-review-mcp local` |
| Homepage URL | `http://localhost:8083` |
| Authorization callback URL | `http://localhost:8083/callback` |

For hosted usage:

| Field | Value |
|---|---|
| Application name | `copilot-review-mcp` |
| Homepage URL | `https://<your-host>` |
| Authorization callback URL | `https://<your-host>/callback` |

After creating the app:

1. Copy the Client ID into `GITHUB_CLIENT_ID`.
2. Generate a Client Secret and copy it into `GITHUB_CLIENT_SECRET`.
3. Keep the secret out of Git.

GitHub OAuth App settings are managed from:

- Personal account: <https://github.com/settings/developers>
- Organization: `https://github.com/organizations/<org>/settings/applications`

Reference: [GitHub Docs: Creating an OAuth app](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)

## 2. Prepare environment variables

Create `.env` from `.env.template` and fill in the OAuth App values.

```bash
cp .env.template .env
```

PowerShell:

```powershell
Copy-Item .env.template .env
```

Minimum local configuration:

```env
GITHUB_CLIENT_ID=your_client_id
GITHUB_CLIENT_SECRET=your_client_secret
BASE_URL=http://localhost:8083
GITHUB_OAUTH_SCOPES=repo,user
MCP_PORT=8083
SQLITE_PATH=/data/copilot-review.db
```

Notes:

- `BASE_URL` must match the OAuth App callback host.
- The GitHub OAuth App callback URL must be `$BASE_URL/callback`.
- The default redirect host allowlist is currently `localhost`, `127.0.0.1`, and `vscode.dev`.
- Hosted `claude.ai` operation needs the redirect host allowlist work tracked in [#6](https://github.com/scottlz0310/copilot-review-mcp/issues/6).

## 3. Run with Docker

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
  --env-file .env \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

Local image:

```bash
docker run -d --name copilot-review-mcp \
  --env-file .env \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  copilot-review-mcp:dev
```

PowerShell can use the same command on one line:

```powershell
docker run -d --name copilot-review-mcp --env-file .env -p 127.0.0.1:8083:8083 -v copilot-review-data:/data ghcr.io/scottlz0310/copilot-review-mcp:latest
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

## 4. Configure an MCP client

The MCP endpoint is:

```text
http://localhost:8083/mcp
```

Use an MCP client that supports Streamable HTTP and OAuth. The exact config shape differs by client, but the core values are:

```json
{
  "mcpServers": {
    "copilot-review-mcp": {
      "type": "http",
      "url": "http://localhost:8083/mcp"
    }
  }
}
```

Some clients use `servers` instead of `mcpServers`, or `streamable-http` instead of `http`. Keep the URL unchanged and adapt the field names to your client.

For VS Code-style MCP config, the shape is typically:

```json
{
  "servers": {
    "copilot-review-mcp": {
      "type": "http",
      "url": "http://localhost:8083/mcp"
    }
  }
}
```

When the client first connects, it should open the OAuth authorization flow. Sign in with GitHub and authorize the OAuth App.

## 5. Install the `pr-review-cycle` skill

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

## 6. Basic review-cycle usage

Prerequisites:

- `copilot-review-mcp` is running and connected in the MCP client.
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

### `redirect_uri host not permitted`

The MCP client sent a redirect URI whose host is not allowed by the server. For local usage, use `localhost`, `127.0.0.1`, or `vscode.dev`. Hosted Claude Web usage requires the redirect-host configuration tracked in #6.

### `invalid_token`

Re-run the OAuth flow in the MCP client. If the token was revoked in GitHub, the client must authenticate again.

### `session_user_mismatch`

The same MCP session ID was reused with a different GitHub login. Clear the MCP client's cached session or reconnect.

### Container starts but `/health` fails

Check logs:

```bash
docker logs copilot-review-mcp
```

Common causes are missing `GITHUB_CLIENT_ID`, missing `GITHUB_CLIENT_SECRET`, or a port already in use.
