# copilot-review-mcp 改善タスク一覧

未整理だったツール動作の調査を経て特定した改善ISSUEと、推奨消化順をまとめる。

## 長期 redesign

`wait_for_copilot_review` を中心とした現行の blocking wait モデルを、
LLM 向けの async watch + notification モデルへ置き換える大きめの redesign を別系統で起票した。

- [#63](https://github.com/scottlz0310/Mcp-Docker/issues/63): epic(copilot-review-mcp): async watch + notification ベースへ再設計し blocking wait を主経路から外す
- [#68](https://github.com/scottlz0310/Mcp-Docker/issues/68): memory-only watch manager を先行導入し active watch を idempotent に扱う
- [#65](https://github.com/scottlz0310/Mcp-Docker/issues/65): SQLite 永続化で review_watch state を追加する
- [#67](https://github.com/scottlz0310/Mcp-Docker/issues/67): watch 系ツールを追加し `wait_for_copilot_review` を legacy 化する
- [#64](https://github.com/scottlz0310/Mcp-Docker/issues/64): Streamable HTTP を stateful session 化し async notification の基盤を作る
- [#66](https://github.com/scottlz0310/Mcp-Docker/issues/66): watch resource と resources/updated 通知を追加する

設計メモ:

`docs/design/copilot-review-watch-redesign.md`

現在の watch tool 運用メモ:

`docs/copilot-review-watch-tools.md`

> この redesign は既存の局所修正（#55, #56, #57, #58）とは別トラックで進める想定。
> 実装に着手する際は、`wait_for_copilot_review` 周辺の重複改修を避けるため、
> 先にどちらの路線で進めるかを決める。

review 反映メモ:

- active watch は `(github_login, owner, repo, pr)` 単位で 1 本に制約し、`start_*` は idempotent にする
- token 失効は `FAILED` + `failure_reason=AUTH_EXPIRED` として扱う
- worker 喪失や再起動の影響を表す `STALE` 状態を設ける
- stateful session 化は先行せず、まず memory-only watch manager → SQLite persistence → tool UX の順で進める

推奨順:

1. [#68](https://github.com/scottlz0310/Mcp-Docker/issues/68) memory-only watch manager
2. [#65](https://github.com/scottlz0310/Mcp-Docker/issues/65) SQLite persistence
3. [#67](https://github.com/scottlz0310/Mcp-Docker/issues/67) watch tool surface / migration
4. [#64](https://github.com/scottlz0310/Mcp-Docker/issues/64) stateful session foundation
5. [#66](https://github.com/scottlz0310/Mcp-Docker/issues/66) resources / notifications

## 推奨消化順

### Step 1 — #56: `wait_for_copilot_review` の動作改善

**他ISSUEへの依存なし。最も独立しており、影響範囲も限定的なため先行着手を推奨。**

- TIMEOUT 後に不要な追加 API コール（`GetReviewData` が重複実行される）を除去する
- コンテキストキャンセル時に進捗情報（PollsDone / WaitedSeconds）が失われる問題を修正する

> 対象ファイル: `services/copilot-review-mcp/internal/tools/wait.go`

---

### Step 2 — #57: `get_pr_review_cycle_status` の入力依存問題

**Step 1 と並行可能。#58 の設計変更とは独立して対応できる。**

- `ci_all_success` が手動入力依存のため誤判定リスクがある（GitHub Checks API 自動取得を検討）
- `last_comment_at` が省略されると `terminateCond2` が常に無効になる（スレッドから自動算出を検討）

> 対象ファイル: `services/copilot-review-mcp/internal/tools/cycle.go`

---

### Step 3 — #55: `get_copilot_review_status` の `blockingThreadCount` バグ

**#58 の設計確定を待ってから対応する。**

`blockingThreadCount` フィールドが常に `0` を返すバグだが、#58 の設計変更によってこのフィールド自体が廃止される可能性がある。
`blockingCount` の責務をMCPサーバーが持つか否かの方針が固まってから修正または廃止を決定する。

> 対象ファイル: `services/copilot-review-mcp/internal/tools/status.go`, `wait.go`

---

### Step 4 — #58 \[DRAFT\]: スレッド分類をLLMルールファイルベースへ移行

**ルールファイル確定後に着手。最も影響範囲が広いため最後に実施。**

- `classifyThread()` および `blockingKeywords` 等のキーワード辞書を MCPサーバーから削除する
- `get_review_threads` のレスポンスから `classification` / `classificationReason` / `summary` フィールドを廃止し、Raw コメントデータのみを返す
- `get_pr_review_cycle_status` の分類サマリ関連フィールドと `blockingCount` 計算をMCPサーバーから除去し、LLMがルールファイルに基づいて判断する設計に変更する
- Step 3（#55）の `blockingThreadCount` 廃止もここで合わせて対応する

> 対象ファイル: `services/copilot-review-mcp/internal/tools/threads.go`, `cycle.go`, `status.go`, `wait.go`

**TODO（ルールファイル確定後に記入）:**
- [ ] ルールファイルのパス:
- [ ] blocking / non-blocking / suggestion の判断基準（日英両対応）:
- [ ] READMEまたはCLAUDE.mdへの記載箇所:

---

## ISSUE 一覧

| ISSUE | 種別 | タイトル | 推奨順 |
|---|---|---|---|
| [#55](https://github.com/scottlz0310/Mcp-Docker/issues/55) | bug | `get_copilot_review_status` の `blockingThreadCount` が常に 0 | Step 3 |
| [#56](https://github.com/scottlz0310/Mcp-Docker/issues/56) | enhancement | `wait_for_copilot_review` のTIMEOUT後の余分なAPIコールとキャンセル時の情報欠落 | Step 1 |
| [#57](https://github.com/scottlz0310/Mcp-Docker/issues/57) | enhancement | `get_pr_review_cycle_status` の `ci_all_success` 手動依存と `last_comment_at` 問題 | Step 2 |
| [#58](https://github.com/scottlz0310/Mcp-Docker/issues/58) | refactor \[DRAFT\] | スレッド分類ロジックをMCPサーバーから除去しLLMルールファイルベースへ移行 | Step 4 |
