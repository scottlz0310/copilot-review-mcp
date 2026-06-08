# アーキテクチャ

[English](architecture.md)

## review-raven の位置づけ

`review-raven` はレビュー基盤における **review される側** の MCP server である。レビューを*受けて*修正する側の操作を提供する。

このリポジトリは reviewer 側の操作（review 投稿・webhook 受信・queue 管理）を実装しない。それらは [thread-owl](https://github.com/scottlz0310/thread-owl) の責務である。

## 5本立てレビュー基盤

| # | リポジトリ | 立場 | 責務 |
|---|-----------|------|------|
| 1 | **[thread-owl](https://github.com/scottlz0310/thread-owl)** | review する側 | GitHub App bot。同一アカウントから review・in-line-comment を投稿する |
| 2 | **review-raven**（このリポジトリ） | review される側 | MCP server。スレッド読み取り・返信・resolve/unresolve・再レビュー依頼 |
| 3 | **[mcp-resource-subscriber](https://github.com/scottlz0310/mcp-resource-subscriber)** | 状態購読ブリッジ | review-raven・thread-owl 両方から skill 経由で呼ばれる。`resources/updated` 通知を購読する |
| 4 | **[mcp-gateway](https://github.com/scottlz0310/mcp-gateway)** | MCP reverse proxy | MCP server 群への routing と認証境界 |
| 5 | **[Mcp-Docker](https://github.com/scottlz0310/Mcp-Docker)** | container orchestration | コンテナ管理・gateway route 生成・CLI agent 設定自動化 |

## review-raven の責務

- PR レビュースレッドを読む
- unresolved スレッドを抽出・分類する
- review comment に返信する
- review スレッドを resolve / unresolve する
- 修正完了後に再レビュー依頼を行う
- Copilot review に限らず GitHub review スレッド全般を扱う（AI reviewer / human reviewer / bot reviewer 問わず）

Copilot review は取得 provider の一つであり、このリポジトリの恒久的な中心概念ではない。

## thread-owl との責務境界

| review-raven | thread-owl |
|---|---|
| review される側の操作 | review する側の操作 |
| スレッド読み取り・返信・resolve | review 投稿 |
| 再レビュー依頼 | webhook 受信 |
| — | review candidate queue |
| — | `queue://review/queue` MCP resource |

このリポジトリに reviewer 側の queue / webhook / GitHub App 投稿基盤を追加しない。thread-owl に reviewed 側の thread resolve / re-review request workflow を追加しない。

## mcp-resource-subscriber との責務境界

`mcp-resource-subscriber` は MCP resource subscription を agent workflow に接続する外部 CLI / ブリッジである。review-raven は長時間稼働する subscription client や watcher CLI を内蔵しない。必要な場合は agent skill から `mcp-resource-subscriber` を外部呼び出しする。

## MCP server 詳細

- **server 名**: `review-raven`
- **MCP client キー**: `review-raven`（tool prefix: `mcp__review-raven__*`）
- **Resource URI スキーム**: `review-raven://watch/{watch_id}`
- **認証**: mcp-gateway 経由の gateway 委任（`X-Authenticated-User` + `Authorization` ヘッダー）

## pr-review-subscribe skill との関係

`pr-review-subscribe` skill は review 取得・スレッド処理・修正 workflow を統合する上位 workflow である。review-raven はその中で reviewed-side MCP provider として機能する。

## 将来方針

[github-mcp-server](https://github.com/github/github-mcp-server) の成熟により、review-raven が現在提供している MCP ツール群が不要になる可能性がある。github-mcp-server が全ツールをカバーした場合、review-raven は skill のみの構成になるかもしれない。新しい MCP ツールを追加する前に、github-mcp-server での代替可能性を先に確認すること。

## 関連 ISSUE

- [review-raven #63](https://github.com/scottlz0310/review-raven/issues/63) — 責務境界定義（このドキュメント）
- [thread-owl #75](https://github.com/scottlz0310/thread-owl/issues/75) — Thread Owl の責務境界
- [mcp-resource-subscriber #86](https://github.com/scottlz0310/mcp-resource-subscriber/issues/86) — `--json` output mode
- [mcp-gateway #92](https://github.com/scottlz0310/mcp-gateway/issues/92) — MCP reverse proxy / auth boundary
- [Mcp-Docker #158](https://github.com/scottlz0310/Mcp-Docker/issues/158) — container orchestration
