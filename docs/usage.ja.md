# 使い方

[English](usage.md)

このドキュメントでは、`copilot-review-mcp` を MCP サーバーとして動かすための基本設定をまとめる。

- GitHub OAuth App の設定
- Docker コンテナのビルド、起動、終了、ログ確認
- MCP クライアント側の基本設定例
- `pr-review-cycle` skill の配置方法

ツール単位の流れは [watch-tools.ja.md](watch-tools.ja.md)、skill テンプレート本体は [skills/pr-review-cycle.ja.md](skills/pr-review-cycle.ja.md) を参照。

## 1. GitHub OAuth App を作成する

利用する公開 URL ごとに OAuth App を 1 つ作る。

ローカル Docker で使う場合:

| 項目 | 値 |
|---|---|
| Application name | `copilot-review-mcp local` |
| Homepage URL | `http://localhost:8083` |
| Authorization callback URL | `http://localhost:8083/callback` |

外部ホストで使う場合:

| 項目 | 値 |
|---|---|
| Application name | `copilot-review-mcp` |
| Homepage URL | `https://<your-host>` |
| Authorization callback URL | `https://<your-host>/callback` |

作成後に行うこと:

1. Client ID を `GITHUB_CLIENT_ID` に設定する。
2. Client Secret を生成し、`GITHUB_CLIENT_SECRET` に設定する。
3. secret は Git に入れない。

GitHub OAuth App の設定画面:

- 個人アカウント: <https://github.com/settings/developers>
- Organization: `https://github.com/organizations/<org>/settings/applications`

参考: [GitHub Docs: Creating an OAuth app](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)

## 2. 環境変数を準備する

`.env.template` から `.env` を作成し、OAuth App の値を入れる。

```bash
cp .env.template .env
```

PowerShell:

```powershell
Copy-Item .env.template .env
```

ローカルでの最小設定:

```env
GITHUB_CLIENT_ID=your_client_id
GITHUB_CLIENT_SECRET=your_client_secret
BASE_URL=http://localhost:8083
GITHUB_OAUTH_SCOPES=repo,user
MCP_PORT=8083
SQLITE_PATH=/data/copilot-review.db
```

注意:

- `BASE_URL` は OAuth App の callback URL の host と一致させる。
- GitHub OAuth App の callback URL は `$BASE_URL/callback` にする。
- 現在の redirect host allowlist の既定値は `localhost`, `127.0.0.1`, `vscode.dev`。
- hosted `claude.ai` 運用には [#6](https://github.com/scottlz0310/copilot-review-mcp/issues/6) で扱う redirect host allowlist の設定対応が必要。

## 3. Docker で起動する

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
  --env-file .env \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

ローカル build イメージ:

```bash
docker run -d --name copilot-review-mcp \
  --env-file .env \
  -p 127.0.0.1:8083:8083 \
  -v copilot-review-data:/data \
  copilot-review-mcp:dev
```

PowerShell では 1 行で実行すると確実。

```powershell
docker run -d --name copilot-review-mcp --env-file .env -p 127.0.0.1:8083:8083 -v copilot-review-data:/data ghcr.io/scottlz0310/copilot-review-mcp:latest
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

## 4. MCP クライアントを設定する

MCP endpoint は以下。

```text
http://localhost:8083/mcp
```

Streamable HTTP と OAuth に対応した MCP クライアントにこの URL を登録する。設定ファイルの形はクライアントごとに異なるが、基本値は以下。

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

クライアントによっては `mcpServers` ではなく `servers`、または `http` ではなく `streamable-http` を使う。URL は同じまま、利用中のクライアントの設定名に合わせる。

VS Code 系の MCP config では、おおむね次の形になる。

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

初回接続時に OAuth 認可フローが開く。GitHub にログインし、作成した OAuth App を認可する。

## 5. `pr-review-cycle` skill を配置する

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
cp docs/skills/pr-review-cycle.md ~/.codex/skills/pr-review-cycle/SKILL.md
```

コピー後、利用中のクライアントで tool prefix が異なる場合は skill 内のプレースホルダーを修正する。

| プレースホルダー | 意味 |
|---|---|
| `{CRM}` | `copilot-review-mcp` のツール |
| `{GH}` | コメント、CI、PR 操作に使う GitHub MCP ツール |

## 6. 基本的な review cycle の使い方

前提:

- `copilot-review-mcp` が起動し、MCP クライアントから接続できる。
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

### `redirect_uri host not permitted`

MCP クライアントが送った redirect URI の host がサーバー側で許可されていない。ローカル利用では `localhost`, `127.0.0.1`, `vscode.dev` を使う。Claude Web の hosted 運用には #6 の redirect host 設定対応が必要。

### `invalid_token`

MCP クライアント側で OAuth フローをやり直す。GitHub 側で token を revoke した場合も再認証が必要。

### `session_user_mismatch`

同じ MCP session ID が別の GitHub login で使われた。MCP クライアント側の session cache を削除するか、再接続する。

### コンテナは起動したが `/health` が失敗する

ログを確認する。

```bash
docker logs copilot-review-mcp
```

よくある原因は `GITHUB_CLIENT_ID` 未設定、`GITHUB_CLIENT_SECRET` 未設定、または port の競合。
