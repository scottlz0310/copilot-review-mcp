# copilot-review-mcp Watch ツールフロー

`services/copilot-review-mcp` の主経路は、blocking wait ではなく async watch です。
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
  Copilot review が `COMPLETED` または `BLOCKED` に到達した。次は `get_review_threads` などへ進む。
- `START_NEW_WATCH`
  現在の watch は継続しない。必要なら `get_copilot_review_status` を再確認してから、新しい watch を開始する。
  `RATE_LIMITED` の場合は `next_poll_seconds` が再開目安になる。
- `REAUTH_AND_START_NEW_WATCH`
  token の再取得後に watch を作り直す。
- `CHECK_FAILURE`
  `last_error` / `failure_reason` を確認し、原因を解消してから次のアクションを決める。

## 補足

- `resource_uri` は将来の resource 公開フェーズに備えた安定 ID です。#67 時点では read/subscribe は未提供です。
- watch state は SQLite に保存されますが、worker 自体は memory-only です。プロセス再起動後の active watch は `STALE` になります。
- 一覧系は同一 `github_login` の watch だけを返します。

## Stateful Session 基盤（#64）

#64 以降、`copilot-review-mcp` の Streamable HTTP は stateless ではなく stateful session として扱います。

- 初回 initialize で発行された `Mcp-Session-Id` を後続 request で再利用します。
- MCP server は request ごとに作成されず、プロセス内の長寿命 server が複数 stateful session を保持します。
- GitHub client は長寿命 server へ閉じ込めず、各 tool request の認証済み header から作成します。
- `Mcp-Session-Id` は GitHub login と対応付け、別 login から同じ session ID が使われた場合は拒否します。
- idle session は server 側 timeout で閉じられます。
- `EventStore` は memory store を使い、future resource notification / SSE replay の土台を用意しています。

テスト観点:

- initialize 後の複数 request が同一 stateful session と長寿命 server を再利用すること。
- 別 GitHub login が既存 `Mcp-Session-Id` を使うと JSON error body 付きの 403 になること。
- timeout などで server から消えた session の login binding が periodic pruning で消えること。
- handler shutdown で active session と background watch manager が停止すること。
- resource notification 追加時は `resources/subscribe` 済み session に `notifications/resources/updated` が届き、通知不可 host では watch status read fallback が維持されること。
