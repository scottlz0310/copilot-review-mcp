# review-raven Watch ツールフロー

[English](watch-tools.md)

このリポジトリの主経路は、blocking wait ではなく async watch です。
このドキュメントは #67 時点の推奨フローと各 tool の役割をまとめます。

## 推奨フロー

1. `get_copilot_review_status(owner, repo, pr)`
2. status が `COMPLETED` / `BLOCKED` でなければ `start_copilot_review_watch(owner, repo, pr)`
3. 他の作業を進める
4. 次の判断点で `get_copilot_review_watch_status(watch_id)` を呼ぶ
5. `watch_id` を見失ったら `list_copilot_review_watches(...)` で回復する
6. watch が不要になったら `cancel_copilot_review_watch(...)` を呼ぶ

## 各ツールの役割

- `get_copilot_review_status`
  GitHub API から即時 snapshot を取る。watch を始める前や、watch が `STALE` / `TIMEOUT` / `CANCELLED` になった後の再確認に使う。
- `start_copilot_review_watch`
  background watch を開始する。active watch が既にあれば idempotent に再利用する。
- `get_copilot_review_watch_status`
  ローカル state を返す cheap read。`watch_id` 優先、なければ `(owner, repo, pr)` lookup が使える。
- `list_copilot_review_watches`
  active / recent watch を一覧する。human debug と watch 回復用。
- `cancel_copilot_review_watch`
  不要な active watch を止める。
- `wait_for_copilot_review`
  legacy fallback。host の都合で blocking wait が必要な場合だけ使う。

## LLM 向けヒント

watch 系ツールは `recommended_next_action` と、必要に応じて `next_poll_seconds` を返します。

- `POLL_AFTER`
  watch はまだ進行中。`next_poll_seconds` 秒後に同じ watch を再確認する。
- `READ_REVIEW_THREADS`
  review が `COMPLETED` または `BLOCKED` に到達した。次は `get_review_threads` などへ進む。
- `START_NEW_WATCH`
  現在の watch は継続しない。必要なら `get_copilot_review_status` を再確認してから、新しい watch を開始する。
  `RATE_LIMITED` の場合は `next_poll_seconds` が再開目安になる。
- `REAUTH_AND_START_NEW_WATCH`
  token の再取得後に watch を作り直す。
- `CHECK_FAILURE`
  `last_error` / `failure_reason` を確認し、原因を解消してから次のアクションを決める。

## 補足

- `resource_uri` は watch の安定 ID です。`review-raven://watch/{watch_id}` スキームで read/subscribe が利用可能です（`RegisterWatchResources` / `SubscribeHandler` 実装済み）。
- watch state は SQLite に保存されますが、worker 自体は memory-only です。プロセス再起動後の active watch は `STALE` になります。
- 一覧系は同一 `github_login` の watch だけを返します。

## Stateless Streamable HTTP（#111）

#111（MCP `2026-07-28` 移行、横断 tracker: [thread-owl#165](https://github.com/scottlz0310/thread-owl/issues/165)）以降、`review-raven` の Streamable HTTP は stateless です（`StreamableHTTPOptions.Stateless: true`）。

- `Mcp-Session-Id` を発行・参照しません。各 request は per-request の一時 session で処理されます（go-sdk の仕様上、protocol `2026-07-28` の negotiation には stateless が必須 — stateful server は `2025-11-25` へフォールバックします）。
- MCP server（`*mcp.Server`）自体は単一の長寿命インスタンスのままです。per-request なのは「session」のみで、server 本体や登録済み tool/resource ではありません。
- GitHub client は従来どおり各 tool request の認証済み header から作成され、session 状態とは無関係です。
- session と login の紐付けは存在しません。認可境界は per-request の GitHub token 認証（`middleware` 経由）のみです。これにより旧 `sessionLogins` map が防いでいた session hijacking の攻撃面自体が消滅しました — session が無いので奪い取るものがありません。
- `EventStore` / `Last-Event-ID` による stream resumption は使用しません（`2026-07-28` では非サポート）。
- GET / DELETE request は `405 Method Not Allowed` を返します（stateless server は standalone SSE stream を開かず、session teardown も受け付けません）。

テスト観点:

- legacy `copilot-review://watch/...` URI への `subscriptions/listen` は server 側で引き続き拒否されること（`SubscribeHandler` が `ResourceNotFoundError` を返す）— `mcp.ClientSession.Subscribe()` ではなく生の HTTP response で検証する。go-sdk v1.7.0 時点で、client 側の `subscriptionsListen()` はこの method のエラーを握りつぶす（wire capture で確認済み: server は正しく `400` + error を返すが、client は `nil` を返す）。これは review-raven 側の認可漏れではなく go-sdk client 側の既知不具合。
- handler shutdown で background watch manager が停止すること。
- `subscriptions/listen` stream への `notifications/resources/updated` 配信と、通知不可 host 向けの watch status read fallback は session モデルの変更による影響を受けないこと。
