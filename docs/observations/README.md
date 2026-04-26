# Copilot Review 状態観測記録

ISSUE #83「状態取得の前提条件検証」のための観測ログを蓄積するディレクトリ。

## 目的

`get_copilot_review_status` (MCP) が返す状態と GitHub API の生データを突き合わせ、
以下を実証的に確定する：

- `review_requested` / `copilot_work_started` / `reviewed` イベントの信頼性
- `requested_reviewers` の出現・消滅タイミング
- `PENDING` / `IN_PROGRESS` 境界をイベントベースで再定義できるかの検証
- AUTO trigger と MANUAL trigger の違い
- 再レビュー（2 サイクル目）での各イベントの挙動

## 観測手順

### 1. スクリプトを実行してログを保存

```bash
./scripts/verify-review-status.sh <owner> <repo> <pr> \
  | tee docs/observations/<label>-pr<number>-<timestamp>.log
```

例:

```bash
./scripts/verify-review-status.sh scottlz0310 Mcp-Docker 99 \
  | tee docs/observations/pending-pr99-20260424T120000Z.log
```

### 2. MCP ツールも同タイミングで実行

`get_copilot_review_status` を呼んだ結果をログ末尾に手動で追記する（または別ファイルに保存）。

### 3. 観測シートに記録

`docs/observations/observation-sheet.md` に観測結果をまとめる。

## ファイル命名規則

```
<state_label>-pr<number>-<timestamp>.log
```

| state_label | 観測タイミング |
|---|---|
| `not-requested` | レビュー依頼前 |
| `pending` | `review_requested` 後・`copilot_work_started` 前 |
| `in-progress` | `copilot_work_started` 後・`PullRequestReview` 前 |
| `completed` | `PullRequestReview` 投稿直後（reviewer 除外前） |
| `ejected` | 完了後しばらく経過（reviewer 除外後） |
| `blocked` | `CHANGES_REQUESTED` 後 |
| `auto-trigger` | AUTO trigger による依頼時 |
| `rereview` | 再レビュー依頼時（2 サイクル目） |

## 観測チェックリスト

### 基本ステート

- [ ] `not-requested`：`requested_reviewers` が空、timeline に `review_requested` なし
- [ ] `pending`：`review_requested` あり、`copilot_work_started` なし
- [ ] `in-progress`：`copilot_work_started` あり、`PullRequestReview` なし
- [ ] `completed`：`PullRequestReview(COMMENTED)` あり、reviewer 除外前
- [ ] `ejected`：`PullRequestReview` あり、`requested_reviewers` から除外済み
- [ ] `blocked`：`PullRequestReview(CHANGES_REQUESTED)` あり

### 追加観測

- [ ] AUTO trigger 時の `review_requested` イベントの `actor` （システム自動 vs ユーザー）
- [ ] 再レビュー依頼時の `review_requested` / `copilot_work_started` の多重発火
- [ ] `reviewed` イベントの `created_at` が null になる条件
- [ ] Bot login の `[bot]` サフィックス有無（REST vs GraphQL）
- [ ] Check Runs に Copilot 関連エントリが現れるか

## 観測シート

詳細は `observation-sheet.md` を参照。
