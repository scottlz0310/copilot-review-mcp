# Changelog

[English](CHANGELOG.md)

このプロジェクトにおける注目すべき変更は、すべてこのファイルに記録されます。

このフォーマットは [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) に基づいており、
このプロジェクトは [Semantic Versioning](https://semver.org/spec/v2.0.0.html) に準拠しています。

## [Unreleased]

### 追加

- `AUTH_MODE=gateway` 対応: `gateway` に設定すると、auth ミドルウェアが上流プロキシ（例: mcp-gateway）から注入された `X-Authenticated-User` ヘッダーを信頼し、GitHub API によるトークン検証をスキップする（二重検証の排除）
- `AUTH_MODE=gateway` 時は `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` が不要になった
- Streamable HTTP セッションの idle timeout を設定する `MCP_SESSION_TIMEOUT_MIN` 環境変数を追加。**既定値を `0`（idle で閉じない）に変更**し、`mcp-gateway` 経由で 30 分前後 idle 後に発生していた `session not found` を解消（#14）。eviction が必要な運用では正の値（例: 24 時間なら `1440`）を指定する。`DELETE` を送らずに消えたクライアントの session が残るメモリ増加トレードオフは README 参照。

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
