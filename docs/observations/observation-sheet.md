# 観測シート：Copilot Review 状態取得の前提条件検証

ISSUE #83 の観測結果をステートごとに記録する。

---

## 観測済み：PR #84（`feat/83-review-status-observation`、Draft → Ready）

**条件**: AUTO trigger（automatic review settings）、ドキュメント変更のみ

### タイムライン実測（REST timeline）

| 時刻 (UTC) | イベント | actor | `requested_reviewers` | 備考 |
|---|---|---|---|---|
| 00:59:02Z | `ready_for_review` | scottlz0310-user | `[Copilot]` | Draft 解除と同時発火 |
| 00:59:02Z | `review_requested` | scottlz0310-user | `[Copilot]` | AUTO trigger。MANUAL と actor は同じ |
| 00:59:29Z | `copilot_work_started` | scottlz0310-user | `[Copilot]` | 依頼から **27 秒後** に着手 |
| 01:01:30Z | `reviewed` (COMMENTED) | — | `[]` | 着手から **121 秒後**。完了と**同時に** reviewer 除外 |

### ステート別 API 観測結果

#### NOT_REQUESTED（Draft 状態）

| エンドポイント | 値 |
|---|---|
| REST `requested_reviewers` | `[]` |
| REST `reviews` | `[]` |
| REST `timeline` | `[]`（review 関連イベントなし） |
| GraphQL `timelineItems` | `[]` |
| GraphQL `reviewRequests` | `[]` |
| MCP `status` | `NOT_REQUESTED` ✅ |

#### PENDING（`review_requested` あり、`copilot_work_started` なし）

観測時刻: `00:59:11Z`（`review_requested` から 9 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[{"login":"Copilot","type":"Bot"}]` | login は `"Copilot"`（大文字） |
| REST `reviews` | `[]` | |
| REST `timeline` events | `ready_for_review`, `review_requested` | `copilot_work_started` なし |
| GraphQL `ReviewRequestedEvent` | `createdAt: 00:59:02Z`, `reviewer: copilot-pull-request-reviewer`, `reviewerType: Bot` | login に `[bot]` なし |
| GraphQL `reviewRequests` | `[{"name":"copilot-pull-request-reviewer","type":"Bot"}]` | |
| MCP `status` | `NOT_REQUESTED` ⚠ **乖離** | |

**MCP 乖離の原因**: `requested_reviewers` の login が `"Copilot"` だが `copilotLogins` に未定義。`IsCopilotInReviewers = false` になるため `PENDING` を検出できない。

#### IN_PROGRESS（`copilot_work_started` あり、`PullRequestReview` なし）

観測時刻: `01:00:05Z`（`copilot_work_started` から 36 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[{"login":"Copilot","type":"Bot"}]` | IN_PROGRESS 中も残存 |
| REST `reviews` | `[]` | |
| REST `timeline` events | `review_requested`, `copilot_work_started` | `reviewed` なし |
| GraphQL `timelineItems` | `ReviewRequestedEvent` のみ | `copilot_work_started` は GraphQL に**存在しない** ⚠ |
| MCP `status` | `NOT_REQUESTED` ⚠ **乖離** | PENDING と同じ理由 |

**新発見**: `copilot_work_started` イベントは **REST timeline 専用**。GraphQL `timelineItems` の対応 `__typename` なし。

#### EJECTED（`PullRequestReview` あり、reviewer 除外後）

観測時刻: `01:01:36Z`（`reviewed` から 6 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[]` | 完了と**同時に除外** |
| REST `reviews` | `[{id:4167147795, login:"copilot-pull-request-reviewer[bot]", state:"COMMENTED"}]` | REST は `[bot]` サフィックスあり |
| REST `timeline` `reviewed` | `created_at: null` | マージ前でも null ⚠ |
| GraphQL `PullRequestReview` | `submittedAt: 01:01:30Z`, `author: copilot-pull-request-reviewer` | GraphQL は `[bot]` なし。`submittedAt` は非 null ✅ |
| GraphQL `reviewDecision` | `null` | COMMENTED のみでは reviewDecision に反映されない |
| GraphQL `reviewRequests` | `[]` | `requested_reviewers` と一致 |
| MCP `status` | `COMPLETED` ✅ | `LatestCopilotReview != nil` で正しく検出 |
| MCP `trigger` | `null` | AUTO trigger は DB に記録なし（仕様） |

---

## 観測済み：PR #81（`release/v2.4.0`、MANUAL trigger、マージ済み）

**条件**: MANUAL trigger（`request_copilot_review` 経由）、ドキュメント変更のみ、スレッド 0 件

### タイムライン実測

| 時刻 (UTC) | イベント | 備考 |
|---|---|---|
| 23:24:32Z | `review_requested` | Draft 解除と同時 |
| 23:25:03Z | `copilot_work_started` | 31 秒後 |
| 23:26:57Z | `reviewed` (COMMENTED) | 着手から 114 秒後 |

### MCP 乖離（23:27:xx 時点）

| MCP フィールド | 値 | 実態 | 乖離 |
|---|---|---|---|
| `status` | `NOT_REQUESTED` | `COMPLETED` | ⚠ |
| `trigger` | `MANUAL` | — | DB 記録あり |
| `requestedAt`（内部 DB） | `23:27:xx` | GitHub 側: `23:24:32Z` | ⚠ 2 分以上ずれ |

**乖離の原因**: DB `requested_at` が GitHub 側より後のため stale 判定が誤発動した。

---

## 確定した知見

### 1. `copilotLogins` に `"Copilot"` が抜けている（重大バグ）

REST `requested_reviewers` が返す Bot の login は **`"Copilot"`**（先頭大文字）。
現在の `copilotLogins` は以下のみを定義しており、`"Copilot"` を含まない：

```go
var copilotLogins = []string{
    "github-copilot[bot]",
    "copilot-pull-request-reviewer[bot]",
    "github-copilot",
}
```

→ **`IsCopilotInReviewers` が常に `false`** になり、PENDING/IN_PROGRESS を検出できない。  
→ AUTO trigger の場合、`trigger_log` がないため stale 判定もスキップされ、COMPLETED は `LatestCopilotReview` 経路で偶然に正しく返る。  
→ MANUAL trigger では `requestedAt` がずれると COMPLETED が `NOT_REQUESTED` になる（PR #81 の乖離）。

### 2. `copilot_work_started` は REST timeline 専用

GraphQL `timelineItems` に対応する `__typename` が存在しない。`PENDING`/`IN_PROGRESS` 境界の検出に GraphQL は使えない。

### 3. `reviewed` イベントの `created_at` は常に null

REST timeline の `reviewed` イベントは `created_at` が null。GraphQL `PullRequestReview.submittedAt` を使うこと。

### 4. `requested_reviewers` からの除外はレビュー完了と同時

`reviewed` イベント発火 ≒ `requested_reviewers` 空になる。数秒以内のラグで除外される。

### 5. AUTO trigger の actor は MANUAL と区別できない（暫定）

`review_requested` の `actor` は `scottlz0310-user`（MANUAL と同じ）。  
`ready_for_review` と同時発火するのが AUTO trigger の特徴だが、単独で依頼した場合との区別は timeline では困難。→ **DB の `trigger` フィールドを信頼する**のが現実的。

### 6. login 名の形式まとめ

| 取得方法 | login 値 |
|---|---|
| REST `requested_reviewers` | `"Copilot"` |
| REST `reviews` | `"copilot-pull-request-reviewer[bot]"` |
| GraphQL `Bot { login }` | `"copilot-pull-request-reviewer"` |
| GraphQL `reviewRequests` | `"copilot-pull-request-reviewer"` |

→ `copilotLogins` に `"Copilot"` の追加が必須。

---

## 観測済み：PR #84（2 サイクル目、`rereview`、MANUAL trigger）

**条件**: MANUAL trigger（`request_copilot_review` 経由）、スクリプト修正コミット後の再依頼

### タイムライン実測（REST timeline、2 サイクル通し）

| 時刻 (UTC) | イベント | サイクル | 備考 |
|---|---|---|---|
| 00:59:02Z | `ready_for_review` | — | Draft 解除 |
| 00:59:02Z | `review_requested` | 1 | AUTO trigger |
| 00:59:29Z | `copilot_work_started` | 1 | 27 秒後 |
| 01:01:30Z | `reviewed` (COMMENTED) | 1 | 121 秒後。`created_at: null` |
| 01:11:32Z | `review_requested` | 2 | MANUAL trigger |
| 01:11:59Z | `copilot_work_started` | 2 | 27 秒後（1 サイクル目と同じ間隔） |
| 01:14:37Z | `reviewed` (COMMENTED) | 2 | 158 秒後。`created_at: null` |

### ステート別 API 観測結果

#### rereview PENDING（`review_requested` あり、`copilot_work_started` なし）

観測時刻: `01:11:51Z`（`review_requested` から 19 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[{"login":"Copilot","type":"Bot"}]` | PENDING 中は在籍 |
| REST `reviews` | `[{id:4167147795, state:COMMENTED}]` | 前サイクルのレビューのみ |
| REST `timeline` | 1 サイクル目全イベント + `review_requested`(2) | `copilot_work_started`(2) なし |
| GraphQL `timelineItems` | `ReviewRequestedEvent`×2, `PullRequestReview`×1 | `copilot_work_started` は GraphQL に存在しない |
| GraphQL `reviewRequests` | `[{"name":"copilot-pull-request-reviewer"}]` | |
| MCP `watch.review_status` | `NOT_REQUESTED` ⚠ **乖離** | copilotLogins バグ（同上） |

#### rereview IN_PROGRESS（`copilot_work_started` あり、`PullRequestReview`(2) なし）

観測時刻: `01:13:05Z`（`copilot_work_started` から 66 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[{"login":"Copilot","type":"Bot"}]` | IN_PROGRESS 中も在籍 |
| REST `reviews` | `[{id:4167147795, state:COMMENTED}]` | 前サイクルのみ。新レビュー未着 |
| REST `timeline` | + `copilot_work_started`(2) | 2 件目が追加 |
| GraphQL `timelineItems` | `ReviewRequestedEvent`×2, `PullRequestReview`×1 | 変化なし（GraphQL に work_started なし） |
| MCP `watch.review_status` | `NOT_REQUESTED` ⚠ **乖離** | |

#### rereview COMPLETED（`PullRequestReview`(2) あり、reviewer 除外後）

観測時刻: `01:16:11Z`（`reviewed`(2) から 94 秒後）

| エンドポイント | 値 | 備考 |
|---|---|---|
| REST `requested_reviewers` | `[]` | 完了と同時に除外（1 サイクル目と同じ） |
| REST `reviews` | `[{id:4167147795}, {id:4167198435, submitted_at:01:14:37Z}]` | **累積**（2 件になる） |
| REST `timeline` `reviewed`(2) | `created_at: null` | 2 サイクル目も null |
| GraphQL `timelineItems` | `ReviewRequestedEvent`×2, `PullRequestReview`×2 | **累積**。2 件目: `submittedAt: 01:14:37Z` |
| GraphQL `reviewRequests` | `[]` | 完了後クリア |
| MCP `watch.review_status` | `COMPLETED` ✅ | `LatestCopilotReview` 経路（id:4167198435）で正しく検出 |
| MCP `watch.completed_at` | `01:14:38Z` | `submittedAt: 01:14:37Z` から **1 秒後** |

### rereview で確定した知見

#### 7. `review_requested` / `copilot_work_started` / `reviewed` はサイクルごとに累積追加される

REST timeline に同イベントが複数件現れる。最新（末尾）が現在サイクルのもの。  
→ 実装では「最後の `copilot_work_started` 以降に `reviewed` があるか」で COMPLETED を判定できる。

#### 8. `reviews` エンドポイントもサイクルごとに累積される

2 サイクル目完了後 `reviews` は 2 件になる。`LatestCopilotReview` は `submittedAt` が最新のものを取ること。  
GraphQL `PullRequestReview` の `submittedAt` で比較するのが安全（REST `submitted_at` は同形式）。

#### 9. rereview の `copilot_work_started` 間隔は 1 サイクル目と同じ

両サイクルとも `review_requested` → `copilot_work_started` は **27 秒**。  
→ インフラ側の定常ディレイと考えられる。

#### 10. GraphQL `timelineItems` の `PullRequestReview` は両サイクル分が返る

最新レビューを取得するには `submittedAt` でソートして末尾を取るか、`reviews` REST の末尾 id を使う。

---

## 未観測ステート

### `blocked`（CHANGES_REQUESTED）

実際の CHANGES_REQUESTED が出た PR で観測が必要。

---

## 次のアクション（本実装向け）

1. **`copilotLogins` に `"Copilot"` を追加**（即時対応可能）
2. **`PENDING`/`IN_PROGRESS` 境界を `copilot_work_started` イベントベースに変更**（要 REST timeline 呼び出し追加）
3. **`requestedAt` を `ReviewRequestedEvent.createdAt` から取得**（DB 記録との 2 分ずれ解消）
4. **`threshold` パラメータを廃止**（イベントベース判定で不要になる）
