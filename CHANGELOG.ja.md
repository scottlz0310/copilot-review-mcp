# Changelog

[English](CHANGELOG.md)

このプロジェクトにおける注目すべき変更は、すべてこのファイルに記録されます。

このフォーマットは [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) に基づいており、
このプロジェクトは [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に準拠しています。

## [Unreleased]

### 追加

- **Phase B 委譲バックグラウンドアクセス — gateway 統合テスト (PR-C)** — [Issue #40](https://github.com/scottlz0310/copilot-review-mcp/issues/40)（[Issue #29](https://github.com/scottlz0310/copilot-review-mcp/issues/29) の一部）:
  - `internal/watch/gateway_integration_test.go` で `gatewayTokenSource → oauth2.ReuseTokenSource → oauth2.Transport → *ghclient.Client → watch.Manager.pollOnce` の経路全体を fake `POST /internal/v1/whoami` と最小限の fake GitHub REST サーバで end-to-end 実行。production 配線 (`cmd/server/main.go` の `buildGatewayClientFactory`) と同じ組み立てを再現する。
  - 6 シナリオを網羅: happy path (200 → `COMPLETED`)、subject gone (404 → `FAILED`/`AUTH_EXPIRED` + re-seed hint)、rotation_failed (502/rotation_failed → `FAILED`/`AUTH_EXPIRED` + refresh-rejected hint)、upstream_failure 単発で `WATCHING` 維持、upstream_failure 連続で `FAILED`/`AUTH_EXPIRED` + consecutive-polls hint、token rotation が GitHub 側に観測可能 (`oauth2.ReuseTokenSource` が rotate 後の値を取り直す)。
  - 既存 `manager_test.go` の sentinel error 直接注入とは独立した経路で結線を検証し、factory リファクタ等で chain が壊れた場合に fail-closed する。
- **Phase B 委譲バックグラウンドアクセス — クライアントコア (PR-A)** — [Issue #29](https://github.com/scottlz0310/copilot-review-mcp/issues/29):
  - `internal/github/gateway_token_source.go` — gateway の `POST /internal/v1/whoami` を叩く `oauth2.TokenSource` 実装 `gatewayTokenSource`。コンストラクタで loopback ホスト (`127.0.0.1` / `::1` / `localhost`) を検証。`expires_at` を `oauth2.Token.Expiry` に反映するため `oauth2.ReuseTokenSource` で whoami 呼び出しを抑制可能。
  - Sentinel エラー `ErrGatewaySubjectGone` (404)、`ErrGatewayUnauthorized` (401)、`ErrGatewayLoopbackRequired` (403)、`ErrGatewayUpstreamFailure` (502)、`ErrGatewayBadRequest` (その他 4xx)、`ErrGatewayNonLoopback`。`FailureReasonAuthExpired` / recovery hint へのマッピングは PR-B に延期。
  - `internal/github/client.go` — 動的トークン用 `NewClientWithTokenSource(ctx, ts, threshold)` を追加（`invalidatingTransport` は付与せず、PR-B で対応）。
  - `internal/tools/server.go` — `BuilderOptions{GatewayClientFactory}` と `BuildStreamableHandlerWithOptions` を追加。既存 `BuildStreamableHandler(db, threshold)` のシグネチャは維持。
  - `cmd/server/main.go` — `COPILOT_REVIEW_GATEWAY_INTERNAL_URL` と `COPILOT_REVIEW_GATEWAY_INTERNAL_SECRET` の両方を設定したときのみ opt-in。未設定時は従来通り `oauth2.StaticTokenSource`（動作変更なし）。片方のみ設定された場合は fail-closed で起動を中断。
  - gateway に送る **subject** は認証済み GitHub login（gateway 仕様に準拠）。
  - **制約**: PoC のためクライアントと gateway は同一ホスト (loopback) 必須。Docker Compose の複数コンテナ構成は PR-A では非対応。

### 変更

- `watch.Options.ClientFactory` のシグネチャを `func(ctx, token string) ReviewDataFetcher` から `func(ctx, token, login string) ReviewDataFetcher` に拡張。内部呼び出し側のみの修正。
- **Phase B PR-A レビュー反映 (PR #30 Copilot レビュー)**:
  - `gatewayTokenSource.Token()` のリクエストコンテキストを設定可能な親 (`GatewayTokenSourceConfig.Context`) と単一の `defaultGatewayTimeout` 定数 (10秒) から派生するよう変更。watch のキャンセル/サーバーシャットダウンが in-flight な whoami 呼び出しに伝播するようになった。
  - 非 200 応答時にレスポンスボディの一部を破棄してから sentinel エラーを返すことで、`net/http` の keep-alive 接続再利用を可能化。
  - `ghclient.ValidateGatewayEndpoint(url, secret)` を新設し、`loadConfig` の起動時チェックに組み込み。不正な URL・非 http(s) スキーム・非ループバックホスト・空シークレットは起動時に fail-fast。watch 毎に static トークンへサイレント降格していた挙動を排除。
  - `buildGatewayClientFactory` で `*http.Client` を 1 度だけ生成し、`GatewayTokenSourceConfig.HTTPClient` 経由で全 watch のトークンソースに共有 (transport / idle 接続プール再利用)。空 login 時のみ到達する static トークンへの runtime フォールバックは `slog.Error` でログ。
  - `GatewayTokenSourceConfig.HTTPClient` の docstring を修正: トークンソースは subject 毎だが、内部の `*http.Client` / `http.Transport` は並行再利用可能で watch 間で共有すべき。

## [3.1.0] - 2026-05-09

### 追加

- **5 つの新しい構造化エラー型** を `internal/autherr` に追加し、[Issue #21](https://github.com/scottlz0310/copilot-review-mcp/issues/21) を完結:
  - `PERMISSION_DENIED` — HTTP 403 レスポンス（rate limit 以外）
  - `RATE_LIMITED` — プライマリ rate limit（`*github.RateLimitError`）とセカンダリ/abuse rate limit（`*github.AbuseRateLimitError`）。`retryable` と `safe_to_continue` は状況依存
  - `NOT_FOUND` — HTTP 404 レスポンス
  - `VALIDATION_ERROR` — HTTP 400 / 422 レスポンス
  - `TRANSIENT_UPSTREAM_ERROR` — HTTP 5xx レスポンス（retryable）
- **`ClassifyGitHubError(err error) *autherr.AuthError`** を `internal/github/client.go` に追加。REST `*github.ErrorResponse`、`*github.RateLimitError`、`*github.AbuseRateLimitError`、shurcooL/githubv4 の文字列マッチ、既分類済みの `*autherr.AuthError` を含む任意の GitHub API エラーを適切な構造化エラー型に変換する単一エントリポイント。
- `internal/tools/auth_result.go` の `tryAuthResult` および `authErrString` が `IsAuthError` の代わりに `ClassifyGitHubError` を呼ぶようになり、8 種類のエラー型すべてに対してハンドラごとの変更なしに構造化エラーを返せるようになった。

### 変更

- skill テンプレート（`docs/skills/`）の MCP サーバーキーを `copilot-review`（旧 `copilot-review-mcp`）と `github`（旧 `github-mcp-server-docker`）に統一。mcp-docker / mcp-gateway のデフォルト設定に合わせた規約変更（#23）。usage docs（`docs/usage.md`、`docs/usage.ja.md`）も同規約に合わせて更新。

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
