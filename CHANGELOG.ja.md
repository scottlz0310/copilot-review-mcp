# Changelog

[English](CHANGELOG.md)

このプロジェクトにおける注目すべき変更は、すべてこのファイルに記録されます。

このフォーマットは [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) に基づいており、
このプロジェクトは [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に準拠しています。

## [Unreleased]

## [3.0.0] - 2026-05-06

### 削除

- **スタンドアロン GitHub OAuth App フローを完全削除。** `internal/auth` パッケージ（handler、session、token cache）を削除。
- `AuthModeStandalone`、`AuthModeGateway` 定数と `AuthMode` 型を `internal/middleware` から削除。
- `TokenInvalidator` インタフェースと `BuildStreamableHandler` の第三引数 `inv TokenInvalidator` を削除。
- 削除された環境変数: `GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`BASE_URL`、`GITHUB_OAUTH_SCOPES`、`SESSION_TTL_MIN`、`TOKEN_CACHE_TTL_MIN`、`TOKEN_EXPIRES_IN_SEC`、`AUTH_MODE`。
- OAuth エンドポイント（`/.well-known/oauth-authorization-server`、`/authorize`、`/callback`、`/token`、`/register`）は **410 Gone** と移行案内を返すようになった。

### 変更

- **認証に mcp-gateway が必須**。サーバーはゲートウェイが注入する `X-Authenticated-User` ヘッダーと `Authorization: Bearer` トークンを信頼する。
- `BuildStreamableHandler(db, threshold)` — 第三引数を削除。
- `middleware.Auth()` — `TokenValidator` と `AuthMode` を引数に取らなくなった（gateway のみ対応）。
- MCP サーバー実装メタデータのバージョンを `3.0.0` に更新。

### 追加

- `BIND_ADDR` 環境変数（デフォルト `127.0.0.1`）。Docker で mcp-gateway（別コンテナ）から到達可能にするには `0.0.0.0` を指定する。

### 移行ガイド

`AUTH_MODE=standalone` または `AUTH_MODE=gateway` で運用していた場合:

1. このサーバーの前段に [mcp-gateway](https://github.com/mcp-b/mcp-gateway) をデプロイする。
2. 以下の環境変数を削除する: `GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`BASE_URL`、`AUTH_MODE`、`GITHUB_OAUTH_SCOPES`、`SESSION_TTL_MIN`、`TOKEN_CACHE_TTL_MIN`、`TOKEN_EXPIRES_IN_SEC`（削除された変数の全リストは上記「破壊的変更」を参照）。
3. MCP クライアントの接続先を mcp-gateway の URL に変更する。stdio クライアントは [mcp-remote](https://github.com/geelen/mcp-remote) を使用する。

## [2.5.0] - 2026-04-26

### 追加

- [scottlz0310/Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker) の `services/copilot-review-mcp/` を独立リポジトリへ分離
- Copilot review workflow 向けの OAuth 対応 Streamable HTTP MCP サーバーを追加
- async watch ツール、review thread の reply/resolve ツール、`pr-review-cycle` skill テンプレートを追加
- SQLite による watch state 永続化と、プロセス再起動後の stale watch 検知を追加
- README、changelog、watch tool docs、skill docs、usage docs を英日バイリンガル化
- test、scan、build、ghcr.io への Docker image 公開 CI を追加

### 補足

- この独立リポジトリでは、Mcp-Docker 時代の `copilot-review-mcp` service 作業から release continuity を引き継ぐ。git 履歴は移行していない。
- 関連する設計・移行経緯は `docs/` 配下を参照。
