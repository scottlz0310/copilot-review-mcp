# 使い方

[English](usage.md)

このドキュメントでは、`copilot-review-mcp` を MCP サーバーとして動かすための基本設定をまとめる。

- アーキテクチャ概要（mcp-gateway 必須）
- Docker コンテナの起動、終了、ログ確認
- mcp-gateway 経由の MCP クライアント接続
- `pr-review-cycle` skill の配置方法

ツール単位の流れは [watch-tools.ja.md](watch-tools.ja.md)、skill テンプレート本体は [skills/pr-review-cycle.ja.md](skills/pr-review-cycle.ja.md) を参照。

> **v3.0.0 BREAKING CHANGE**: スタンドアロン OAuth を削除。mcp-gateway が必須になりました。

## アーキテクチャ

```
MCP クライアント（Claude Code / Claude Desktop / VS Code）
    │
    │  HTTPS / OAuth（mcp-gateway が処理）
    ▼
mcp-gateway  ──►  X-Authenticated-User + Authorization ヘッダーを注入
    │
    │  HTTP（内部通信のみ）
    ▼
copilot-review-mcp  :8083
    │
    │  SQLite
    ▼
/data/copilot-review.db
```

`copilot-review-mcp` は mcp-gateway が注入したヘッダーを信頼し、OAuth を直接行わない。

## 1. mcp-gateway をセットアップする

[mcp-gateway のドキュメント](https://github.com/mcp-b/mcp-gateway) に従ってデプロイ・設定する。

gateway の upstream ルートのひとつを `http://localhost:8083`（または `copilot-review-mcp` を動かす内部ホスト名）に向ける。

## 2. Docker で copilot-review-mcp を起動する

### 公開済みイメージを pull

```bash
docker pull ghcr.io/scottlz0310/copilot-review-mcp:latest
```

### ローカルで build

```bash
docker build -t copilot-review-mcp:dev .
```

### コンテナを起動

公開済みイメージ:

```bash
docker run -d --name copilot-review-mcp \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

ローカル build イメージ:

```bash
docker run -d --name copilot-review-mcp \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  copilot-review-mcp:dev
```

任意の環境変数（すべてデフォルト値あり）:

```env
MCP_PORT=8083
LOG_LEVEL=info
SQLITE_PATH=/data/copilot-review.db
IN_PROGRESS_THRESHOLD_SEC=30
MCP_SESSION_TIMEOUT_MIN=0
```

### health check

```bash
curl http://127.0.0.1:8083/health
```

PowerShell:

```powershell
Invoke-RestMethod http://127.0.0.1:8083/health
```

期待するレスポンス:

```json
{"status":"ok"}
```

### ログ確認

```bash
docker logs -f copilot-review-mcp
```

### 停止、再起動、削除

```bash
docker stop copilot-review-mcp
docker start copilot-review-mcp
docker rm -f copilot-review-mcp
```

named volume には SQLite の watch state DB が残る。

```bash
docker volume ls --filter name=copilot-review-data
```

ローカル状態を削除したい場合だけ volume を削除する。

```bash
docker volume rm copilot-review-data
```

## 3. MCP クライアントを設定する

### Streamable HTTP クライアント（Claude Code、VS Code）

mcp-gateway の URL をクライアントに登録する:

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

クライアントによっては `mcpServers` ではなく `servers`、または `http` ではなく `streamable-http` を使う。URL は変えずにフィールド名を合わせる。

### stdio クライアント（Claude Desktop 等）— mcp-remote 経由

[mcp-remote](https://github.com/geelen/mcp-remote) をブリッジとして使用する:

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

初回接続時に mcp-gateway が OAuth 認可フローを処理する。GitHub にログインして認可する。

## 4. `pr-review-cycle` skill を配置する

このリポジトリには skill テンプレートが入っているが、使う前に AI エージェント側のローカル skill ディレクトリへコピーする。

### Codex

PowerShell:

```powershell
$skillDir = "$env:USERPROFILE\.codex\skills\pr-review-cycle"
New-Item -ItemType Directory -Force $skillDir
Copy-Item docs\skills\pr-review-cycle.ja.md "$skillDir\SKILL.md"
```

POSIX shell:

```bash
mkdir -p ~/.codex/skills/pr-review-cycle
cp docs/skills/pr-review-cycle.ja.md ~/.codex/skills/pr-review-cycle/SKILL.md
```

### Claude 系の skill ディレクトリ

```bash
mkdir -p ~/.claude/skills/pr-review-cycle
cp docs/skills/pr-review-cycle.ja.md ~/.claude/skills/pr-review-cycle/SKILL.md
```

英語版を使う場合:

```bash
cp docs/skills/pr-review-cycle.md ~/.claude/skills/pr-review-cycle/SKILL.md
```

コピー後、利用中のクライアントで tool prefix が異なる場合は skill 内のプレースホルダーを修正する。

| プレースホルダー | 意味 |
|---|---|
| `{CRM}` | `copilot-review-mcp` のツール |
| `{GH}` | コメント、CI、PR 操作に使う GitHub MCP ツール |

## 5. 基本的な review cycle の使い方

前提:

- `copilot-review-mcp` が起動し、mcp-gateway 経由で接続できる。
- GitHub MCP サーバーまたは GitHub connector も利用できる。
- 対象リポジトリに open PR がある。

AI エージェントへの典型的な指示:

```text
$pr-review-cycle
```

skill は以下を行う。

1. Copilot review の状態確認または依頼
2. async watch による完了待機
3. review thread の取得
4. コメント分類
5. accepted な修正の実装
6. remote side effect が許可されている場合の返信・resolve
7. CI と coverage の確認

マージは、ユーザーが明示的に許可した場合だけ別途行う。

## トラブルシュート

### `missing_proxy_identity`（401）

リクエストが mcp-gateway を経由せずに `copilot-review-mcp` に届いた、または gateway が `X-Authenticated-User` を注入するよう設定されていない。すべてのトラフィックが mcp-gateway を通るよう確認する。

### `session_user_mismatch`

同じ MCP session ID が別の GitHub login で使われた。MCP クライアント側の session cache を削除するか、再接続する。

### コンテナは起動したが `/health` が失敗する

ログを確認する。

```bash
docker logs copilot-review-mcp
```

よくある原因は port の競合または `SQLITE_PATH` の誤り。
