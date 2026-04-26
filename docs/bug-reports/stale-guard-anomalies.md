# Bug Report: copilot-review-mcp — stale-guard 起因の異常応答

**作成日**: 2026-04-23  
**発見契機**: PR #78 (`docs/pr-review-skill-template`) にて `pr-review-cycle` スキルを動作検証した際に観測  
**対象サービス**: `services/copilot-review-mcp/`  
**関連ソース**: `internal/github/client.go`, `internal/watch/manager.go`, `internal/tools/status.go`, `internal/tools/request.go`, `internal/tools/cycle.go`

---

## 観測タイムライン（再現エビデンス）

| 時刻 (UTC) | イベント |
|---|---|
| `04:55:57Z` | `copilot-pull-request-reviewer[bot]` が PR #78 に `COMMENTED` レビューを提出 |
| ~`04:56:05Z` | Phase 0: `get_copilot_review_status` 呼び出し → **`NOT_REQUESTED`** |
| ~`04:56:08Z` | `request_copilot_review` 呼び出し → `{"ok":true,"trigger":"MANUAL"}` |
| `04:56:11Z` | `start_copilot_review_watch` 開始 / polls_done=1 → `review_status: NOT_REQUESTED` |
| `04:57:42Z` | polls_done=2 → `review_status: NOT_REQUESTED`（レビューから約2分後も検出せず） |
| (解決後即時) | `get_pr_review_cycle_status` → `unresolved_count: 5`（全スレッド解決済みなのに） |
| (再コール) | `get_pr_review_cycle_status` → `unresolved_count: 0`（正常値） |

---

## Bug A: `get_copilot_review_status` が完了済みレビューを `NOT_REQUESTED` で返す

### 症状
- レビューが `04:55:57Z` に提出済み
- `04:56:05Z`（8秒後）に `get_copilot_review_status` を呼び出したが `NOT_REQUESTED` が返った

### 根本原因
`client.go` の `GetReviewData()` → `ListReviews()` (REST API) は、GitHub のバックエンドにおける伝播遅延（数秒〜数十秒）により、直後の呼び出しで空のリストを返す場合がある。

`DeriveStatusWithThreshold` は `data.LatestCopilotReview == nil && !data.IsCopilotInReviewers` の場合に `StatusNotRequested` を返す。レビューが REST API に反映されていない間は、この条件が成立する。

```go
// client.go: DeriveStatusWithThreshold
if data.LatestCopilotReview != nil {
    // ... relevant check ...
    if relevant {
        return StatusCompleted  // ← LatestCopilotReview が nil だと到達しない
    }
}
if data.IsCopilotInReviewers {
    return StatusPending / StatusInProgress
}
return StatusNotRequested  // ← フォールスルー
```

### 影響
単独では「Phase 0 での誤判定」に留まるが、続く Bug B の引き金となる。

---

## Bug B: `request_copilot_review` 後に stale-guard が永続的に発火し `NOT_REQUESTED` に固着する

### 症状
- `request_copilot_review` 呼び出し後、ウォッチが 2 回ポーリングしても `review_status: NOT_REQUESTED`
- 04:57:42Z の時点では既に完了から約 2 分が経過しているにもかかわらず検出されない

### 根本原因（コードで確認済み）

`request_copilot_review` は無条件に `db.Insert(owner, repo, pr, "MANUAL")` を呼び出す（`request.go` L78）。  
このとき `RequestedAt = now()` が記録される。

```go
// request.go
if _, err := db.Insert(in.Owner, in.Repo, in.PR, "MANUAL"); err != nil {
    return nil, RequestOutput{}, ...
}
```

レビューが `04:55:57Z` に提出済みで、`request_copilot_review` が `~04:56:08Z` に呼ばれた場合：

```
RequestedAt = 04:56:08Z
LatestCopilotReview.SubmittedAt = 04:55:57Z

// DeriveStatusWithThreshold の stale-guard:
relevant = !sat.IsZero() && !sat.Before(*requestedAt)
         = !(04:55:57Z.Before(04:56:08Z))
         = !true
         = false  ← 完了済みレビューが「古い」とみなされる
```

`relevant = false` となるため、完了済みレビューが無視される。  
さらに `IsCopilotInReviewers` もレビュー提出後は `false`（提出後はレビュアーリストから削除される）のため、  
**すべての経路で `StatusNotRequested` が返り続ける**。

ウォッチワーカー（`manager.go`）も同じ `DeriveStatusWithThreshold` を使うため同様の結果となる（L667）。

```go
// manager.go: pollOnce
entry, err = m.db.GetLatest(w.key.owner, w.key.repo, w.key.pr)
var requestedAt *time.Time
if entry != nil {
    requestedAt = &entry.RequestedAt  // ← 上書きされた後の RequestedAt が使われる
}
reviewStatus := ghclient.DeriveStatusWithThreshold(m.threshold, data, requestedAt)
```

### 発生条件
1. Copilot がレビューを自動提出する（auto-trigger または既存リクエスト）
2. `get_copilot_review_status` の呼び出しが GitHub REST API 伝播前（Bug A）または完了直後に行われ、`NOT_REQUESTED` が返る
3. スキルの Phase 0 ロジックが `NOT_REQUESTED` に反応して `request_copilot_review` を呼び出す
4. `request_copilot_review` が DB に `RequestedAt > SubmittedAt` なエントリを挿入する
5. 以降の全ポーリングで stale-guard が発火し、`NOT_REQUESTED` に固着する

### カスケード効果
この状態が続くと：
- スキルが再度 `request_copilot_review` を呼び出そうとする（`HasPending()` は `true` なので拒否されるが、ウォッチキャンセル後の特定タイミングによっては通過するリスクがある → Bug C の仮説 2 参照）
- ウォッチが `TIMEOUT` まで `NOT_REQUESTED` を返し続ける
- `trigger_log.completed_at` はウォッチキャンセル時に更新されない（後述の Bug D）

### Bug D: ウォッチキャンセル時に `trigger_log.completed_at` が更新されない

`CancelByID` / `CancelLatest` は `finishLocked(StatusCancelled)` を呼ぶが、  
`db.UpdateCompletedAt` を呼ばないため `trigger_log.completed_at = NULL` のままになる。

```go
// manager.go: CancelByID
m.finishLocked(state, StatusCancelled, nil, now, "watch was cancelled manually")
// UpdateCompletedAt は呼ばれない
```

結果として `HasPending() = true` が維持されるため、ウォッチキャンセル後に  
`request_copilot_review` を呼ぼうとすると `REVIEW_IN_PROGRESS` で拒否される。  
これは二重リクエスト防止の観点では安全だが、**正しく完了した場合とキャンセルした場合で  
DB 状態が区別できない**という別の問題を生む。新規リクエストを送るには手動での DB クリーンアップが必要。

---

## Bug C: `get_pr_review_cycle_status` が直後の呼び出しで古い `unresolved_count` を返す

### 症状
- 5 件のスレッドを `reply_and_resolve_review_thread` で順次解決（全件 `resolved: true`）
- 即時呼び出しした `get_pr_review_cycle_status` → `merge_conditions.unresolved_count: 5`
- 数分後に再呼び出し → `unresolved_count: 0`

### 根本原因仮説 1: GitHub GraphQL API の伝播遅延（有力）
`cycle.go` の `cycleStatusHandler` は `gh.GetReviewThreads()` で GitHub GraphQL API からスレッド状態を取得する。  
`reply_and_resolve_review_thread` がスレッドを解決した直後は、GraphQL API のキャッシュ/伝播遅延により、  
解決済みスレッドが `IsResolved: false` のまま返ることがある。

```go
// cycle.go
rawThreads, err := gh.GetReviewThreads(ctx, in.Owner, in.Repo, in.PR)
for _, t := range rawThreads {
    if !t.IsResolved {
        unresolvedCount++  // ← GraphQL が古いキャッシュを返すと誤カウントされる
    }
}
```

### 根本原因仮説 2: Bug B 起因の二重レビューリクエスト（要調査）

Bug B によりウォッチが `NOT_REQUESTED` に固着している間、スキルが再度 `request_copilot_review` を呼び出した場合、  
**ウォッチキャンセル後のタイミング**によっては 2 回目のリクエストが受け付けられる可能性がある。

コード確認による検証：

```go
// manager.go: CancelByID / CancelLatest
m.finishLocked(state, StatusCancelled, nil, now, "watch was cancelled manually")
// ↑ UpdateCompletedAt を呼ばない → trigger_log.completed_at = NULL のまま
```

```go
// store/db.go: HasPending
SELECT COUNT(*) FROM trigger_log
WHERE owner = ? AND repo = ? AND pr = ? AND completed_at IS NULL
// ↑ キャンセル後も completed_at = NULL → HasPending() = true
```

**現時点での結論**: ウォッチをキャンセルしても `trigger_log.completed_at` は更新されないため、  
`HasPending()` は `true` を返し続ける。よって 2 回目の `request_copilot_review` は  
`REVIEW_IN_PROGRESS` で拒否されるはず。PR #78 の観測では仮説 2 は成立しない可能性が高い。

ただし、以下の **エッジケース** では二重発火が起こりうる（未検証）：
- `trigger_log` エントリが存在しない状態でウォッチが走っている場合（AUTO trigger 等で DB 未記録）
- ウォッチ開始前に別のパスで `completed_at` が更新された場合

**この点は仮説 1 と合わせて再現テストで確認が必要。**

### 影響
- `recommended_action: REPLY_RESOLVE` という誤った推奨が返る
- スキルが不要な解決処理を再実行しようとする（ただし、`reply_and_resolve` はべき等なので二重解決の実害は小さい）
- 即時リトライで正しい値が返るため、単体では軽微なバグ
- **ただし二重レビューが発火していた場合は、新規スレッドが 5 件追加されたことになり、重大度が上がる**

---

## 修正案

### Bug A・B への修正候補

**Option 1（推奨）: `request_copilot_review` が直前の完了済みレビューを確認して `RequestedAt` を調整する**

`request_copilot_review` を呼び出す直前に `GetReviewData()` を取得している（既存コード）。  
ここで `LatestCopilotReview` が存在する場合は、そのレビューの `SubmittedAt` を `RequestedAt` として記録する（または `SubmittedAt - 1ns`）。  
こうすることで stale-guard が `!sat.Before(*requestedAt)` = `true` となり、既存のレビューが正しく認識される。

```go
// request.go の修正案（requestHandler） — 実装済み
data, err := ghClient.GetReviewData(ctx, in.Owner, in.Repo, in.PR)
// ...
// 直近の完了済み Copilot レビューがある場合は:
//   1. requested_at = sat+1s (タイムスタンプ基準の後方互換フォールバック用)
//   2. prev_review_id = 既存レビューの ID (ID 基準の正確な判定用)
if data.LatestCopilotReview != nil {
    sat := data.LatestCopilotReview.GetSubmittedAt().Time
    if !sat.IsZero() {
        candidate := sat.UTC().Add(time.Second)
        latest, _ := db.GetLatest(owner, repo, pr)
        if latest == nil || candidate.After(latest.RequestedAt) {
            prevID := fmt.Sprintf("%d", data.LatestCopilotReview.GetID())
            _, err = db.InsertWithPrevReviewID(owner, repo, pr, "MANUAL", candidate, prevID)
        } else {
            _, err = db.Insert(owner, repo, pr, "MANUAL")
        }
    } else {
        _, err = db.Insert(owner, repo, pr, "MANUAL")
    }
} else {
    _, err = db.Insert(owner, repo, pr, "MANUAL")
}
```

`InsertWithPrevReviewID` は `requested_at`（sat+1s）と `prev_review_id`（既存レビューの ID）を  
同時に `trigger_log` に記録する。`DeriveStatus` は `prevReviewID` が非 nil のとき ID 比較を優先し、  
nil のときはタイムスタンプ比較にフォールバックする（後方互換）。

ただし、`db.Insert` がタイムスタンプと prev_review_id を外部から受け取れるよう `InsertWithPrevReviewID` を追加する必要がある。

**Option 2: `get_copilot_review_status` に「直近 N 分以内のレビューを無条件で有効」とするフォールバックを追加する**

`DeriveStatusWithThreshold` で `relevant = false` になった場合でも、  
レビューが直近 N 分以内（例: 10 分）に提出されたなら `COMPLETED` を返すフォールバックを設ける。  
ただし、このアプローチは stale-guard の意図（古いレビューを無視する）を弱める。

**Option 3（最小修正）: スキル側のワークアラウンド**

Phase 0 で `NOT_REQUESTED` が返った場合、`request_copilot_review` を呼ぶ前に GitHub のレビューリスト（`{GH}:get_pull_request_reviews`）を直接確認し、  
完了済みレビューがあれば Phase 2 に移行する。本修正が実装されるまでのワークアラウンドとして `docs/skills/pr-review-cycle.md` に記載する。

### Bug C への修正候補

**Option 1（推奨）: `get_pr_review_cycle_status` に短い wait またはリトライを追加する**

スレッド解決直後の呼び出しに対しては、1〜2 秒待機後にリトライすることで伝播を待つ。

**Option 2: スキル側のワークアラウンド**

`recommended_action: REPLY_RESOLVE` が返ったが、全スレッドを直前に解決した場合は、  
数秒待ってから再呼び出しする。`unresolved_count > 0` でも `reply_and_resolve_review_thread` はべき等なため、  
即座に再解決しても実害はないが、不必要な API 呼び出しを省ける。

---

## 関連ファイル

| ファイル | 関連度 |
|---|---|
| [services/copilot-review-mcp/internal/tools/request.go](../../services/copilot-review-mcp/internal/tools/request.go) | Bug B の直接原因 (`db.Insert` のタイミング) |
| [services/copilot-review-mcp/internal/github/client.go](../../services/copilot-review-mcp/internal/github/client.go) | Bug A・B (`GetReviewData`, `DeriveStatusWithThreshold`) |
| [services/copilot-review-mcp/internal/watch/manager.go](../../services/copilot-review-mcp/internal/watch/manager.go) | Bug B のウォッチワーカー側 (`pollOnce` L603–L700) |
| [services/copilot-review-mcp/internal/tools/cycle.go](../../services/copilot-review-mcp/internal/tools/cycle.go) | Bug C (`GetReviewThreads` 直後の伝播遅延) |
| [services/copilot-review-mcp/internal/tools/status.go](../../services/copilot-review-mcp/internal/tools/status.go) | Bug A の呼び出し元 |

---

## 優先度

| Bug | 重大度 | 再現性 | 優先度 |
|---|---|---|---|
| **Bug B** (stale-guard 固着) | 高 — ウォッチが永続的に `NOT_REQUESTED` になる | 中（Copilot の自動提出タイミング依存） | **高** |
| **Bug A** (REST 伝播遅延) | 中 — Bug B の引き金。単体では一時的な誤値 | 中 | 中 |
| **Bug D** (キャンセル時 `completed_at` 未更新) | 中 — 次のリクエストが `REVIEW_IN_PROGRESS` で拒否され続ける | 高（キャンセルするたびに必ず発生） | 中 |
| **Bug C** (GraphQL キャッシュ遅延) | 低〜高 — 単純な伝播遅延なら軽微。二重レビュー発火なら高 | 高（再現容易） | 中（要調査） |
