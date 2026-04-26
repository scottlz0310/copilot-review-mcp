# copilot-review-mcp

[English](README.md)

GitHub Copilot の PR レビューサイクルを管理する MCP（Model Context Protocol）サーバー。レビュー依頼・完了検知・staleness 判定・スレッド返信／解決までを LLM 向けの async watch + notification モデルで提供する。

OAuth ファサードを内蔵しており、Streamable HTTP transport で `claude.ai`、Claude Code、VS Code などの MCP クライアントから直接接続できる。

## 特徴

- **async watch + notification** ベース。`start_copilot_review_watch` で background watch を開始し、`get_copilot_review_watch_status` の cheap read と `notifications/resources/updated` で進捗を取る
- **GraphQL ベースの Copilot review request**。REST `requested_reviewers` が bot actor を黙って無視する問題を回避する
- **PR レビュースレッド単位の操作**。`PRRT_xxx` ノード ID で reply / resolve / reply+resolve を行う
- **OAuth Authorization Code flow** を MCP クライアント向けに提供（GitHub OAuth App をバックエンドに使用）
- **Stateful session**。`Mcp-Session-Id` を GitHub login にバインドし、idle timeout で自動 prune
- **SQLite による watch state 永続化**。プロセス再起動後の active watch は `STALE` として観測できる

## 提供ツール

| ツール | 用途 |
|---|---|
| `request_copilot_review` | PR に Copilot レビューを依頼する |
| `get_copilot_review_status` | GitHub から即時 snapshot を取る |
| `start_copilot_review_watch` | background watch を開始（推奨経路の入口） |
| `get_copilot_review_watch_status` | watch の現在状態を cheap read |
| `list_copilot_review_watches` | 自分の active / recent watch 一覧 |
| `cancel_copilot_review_watch` | watch を停止 |
| `get_pr_review_cycle_status` | レビューサイクル全体の状態と次のアクション提案 |
| `get_review_threads` | レビュースレッド一覧（Raw データ。分類は呼び出し元 LLM 側で行う） |
| `reply_to_review_thread` | スレッドに返信 |
| `resolve_review_thread` | スレッドを解決済みにする |
| `reply_and_resolve_review_thread` | 返信→解決を順次実行 |
| `wait_for_copilot_review` | legacy blocking wait（fallback） |

詳細は [docs/watch-tools.ja.md](docs/watch-tools.ja.md) と [docs/skills/pr-review-cycle.ja.md](docs/skills/pr-review-cycle.ja.md) を参照。

## クイックスタート（Docker）

GitHub OAuth App を作成し、Client ID / Client Secret を取得しておく（コールバック URL: `http://localhost:8083/callback`）。

```bash
docker run --rm -p 127.0.0.1:8083:8083 \
  -e GITHUB_CLIENT_ID=... \
  -e GITHUB_CLIENT_SECRET=... \
  -e BASE_URL=http://localhost:8083 \
  -v copilot-review-data:/data \
  ghcr.io/scottlz0310/copilot-review-mcp:latest
```

MCP クライアント（Claude Code 等）には `http://localhost:8083/mcp` を OAuth 対応 MCP サーバーとして登録する。

## 環境変数

| 変数 | 必須 | 既定値 | 説明 |
|---|---|---|---|
| `GITHUB_CLIENT_ID` | ✓ | — | GitHub OAuth App の Client ID |
| `GITHUB_CLIENT_SECRET` | ✓ | — | GitHub OAuth App の Client Secret |
| `BASE_URL` | | `http://localhost:8083` | MCP サーバーの公開 URL |
| `GITHUB_OAUTH_SCOPES` | | `repo,user` | OAuth スコープ |
| `MCP_PORT` | | `8083` | リッスンポート |
| `LOG_LEVEL` | | `info` | `debug` / `info` / `warn` / `error` |
| `SESSION_TTL_MIN` | | `10` | OAuth セッション TTL（分） |
| `TOKEN_CACHE_TTL_MIN` | | `30` | トークン検証キャッシュ TTL（分） |
| `TOKEN_EXPIRES_IN_SEC` | | `7776000` | クライアントへ告知するトークン有効期限（秒） |
| `SQLITE_PATH` | | `/data/copilot-review.db` | watch state DB のパス |
| `IN_PROGRESS_THRESHOLD_SEC` | | `30` | review request から in-progress とみなすまでの猶予（秒） |

## ローカル開発

Go 1.26+ が必要。

```bash
# テスト
go test ./...

# ビルド
go build -o bin/copilot-review-mcp ./cmd/server

# Dockerイメージビルド
docker build -t copilot-review-mcp:dev .
```

## 履歴

このリポジトリは [scottlz0310/Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker) の `services/copilot-review-mcp/` を分離したもの。git 履歴は移行していない。Mcp-Docker 側の関連 PR / Issue（`#47`, `#52`, `#53`, `#55`–`#58`, `#62`, `#63`–`#68`, `#74`–`#77`, `#92` など）は `docs/` 配下のドキュメントから参照される。

## ライセンス

MIT License — [LICENSE](LICENSE) を参照。
