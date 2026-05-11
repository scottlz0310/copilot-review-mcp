# 追加調査: Auth Lifecycle Deep Dive

> 前回レポート: `spike-auth-lifecycle-mismatch.md`
> 対象 Issue: https://github.com/scottlz0310/mcp-gateway/issues/70

---

## Q1. watch goroutine は token/login をどこから取っているか

### token の完全な流れ

```
[mcp-gateway]
    │  Authorization: Bearer <token>  ← リクエストごとに注入
    ▼
[middleware/auth.go: Auth()]
    │  extractBearer(r) → token を request context に格納
    │  context.WithValue(ctx, ContextKeyToken, token)
    ▼
[tools/auth_request.go: tokenFromToolRequest(ctx, req)]
    │  優先順位①: req.Extra.Header["Authorization"] (MCP SDK が渡すヘッダー)
    │  優先順位②: middleware.TokenFromContext(ctx)  (HTTP context からのフォールバック)
    ▼
[tools/watch.go: startWatchHandler()]
    │  token := tokenFromToolRequest(ctx, req)
    │  manager.Start(watch.StartInput{Token: token, ...})
    ▼
[watch/manager.go: watchState.token]  ← ここで**固定スナップショット**
    │  watchState.token = in.Token
    │  watchState.client = m.clientFactory(watchCtx, in.Token)
    │    → ghclient.NewClient(watchCtx, token, threshold, nil)
    │      → oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
    ▼
[goroutine: m.run(state)]
    │  client := w.client  ← スナップショット時点のクライアントのみ使用
    │  client.GetReviewData(callCtx, ...)
    │    → HTTP ヘッダー: Authorization: Bearer <snapshotted_token>
```

### login の完全な流れ

```
[mcp-gateway]
    │  X-Authenticated-User: <login>
    ▼
[middleware/auth.go: Auth()]
    │  login := r.Header.Get("X-Authenticated-User")
    │  context.WithValue(ctx, ContextKeyLogin, login)
    ▼
[tools/auth_request.go: loginFromToolRequest(ctx, req)]
    │  優先順位①: req.Extra.TokenInfo.UserID  (MCP SDK の TokenInfo)
    │  優先順位②: middleware.LoginFromContext(ctx)
    ▼
[tools/watch.go: startWatchHandler()]
    │  login := loginFromToolRequest(ctx, req)
    │  manager.Start(watch.StartInput{Login: login, ...})
    ▼
[watch/manager.go: watchState.key.login]  ← 所有権確認のみに使用
    │  CancelByID / GetByID / List の認可チェックで参照
    │  GitHub API 呼び出しには使われない
```

### goroutine 実行中の重要な特性

| 項目 | 実態 |
|---|---|
| トークンの種別 | `oauth2.StaticTokenSource` → **リフレッシュ不可** |
| goroutine の context | `manager.ctx` (= `context.Background()` 由来) — HTTP リクエスト context とは独立 |
| mcp-gateway への依存 | **ゼロ** — goroutine は以後 gateway を一切参照しない |
| トークン更新の唯一の経路 | `start_copilot_review_watch` 再呼び出し時に `tokenChanged` フラグで差し替え |
| `invalidatingTransport` | `InvalidateToken: nil` のためインストール**されない** (server.go:136, watch.Options.InvalidateToken: nil) |

---

## Q2. watch state 復元後に auth をどう扱っているか

### 結論: goroutine の復元は一切行われない

`store/db.go` の `Open()` 関数で、DB 開放と同時に次が実行される:

```go
// store/db.go:124
d := &DB{db: db}
if _, err := d.MarkActiveReviewWatchesStale(staleOnOpenMessage); err != nil {
    db.Close()
    return nil, err
}
```

定数:
```go
const staleOnOpenMessage = "watch became stale because the copilot-review-mcp process restarted"
```

`MarkActiveReviewWatchesStale()` は以下の SQL を実行する:
```sql
UPDATE review_watch
    SET watch_status = 'STALE',
        failure_reason = NULL,
        is_active = 0,
        updated_at = strftime('%s','now'),
        completed_at = COALESCE(completed_at, strftime('%s','now')),
        stale_at = COALESCE(stale_at, strftime('%s','now')),
        last_error = ...
  WHERE is_active = 1
```

### 起動時の状態遷移

```
プロセス再起動
    │
    ▼
store.Open() が呼ばれる
    │
    ▼
MarkActiveReviewWatchesStale() — is_active=1 の全行を即座に STALE 化
    │
    ▼  (認証情報は DB に保存されない — review_watch テーブルに token 列は存在しない)
watch.NewManager() — 空の in-memory マップで開始
    │
    ▼
Manager.GetByID() または GetLatest() が呼ばれると
    │  in-memory に存在しない → DB を参照
    │  DB エントリは is_active=0, watch_status='STALE'
    ▼
snapshotFromReviewWatchEntry() が返す Snapshot:
    Terminal: true   (is_active=false)
    WorkerRunning: false
    WatchStatus: "STALE"
    (token: 保存なし — Snapshot に token フィールドは存在しない)
```

### 重要な含意

- "復元後に auth をどう扱うか" という問いは **現行設計では成立しない**
- プロセス再起動により全 active ウォッチが STALE になるため、auth が必要な goroutine は存在しない
- LLM は `WorkerRunning: false` + `WatchStatus: STALE` を見て `start_copilot_review_watch` を再呼び出しする
- 再呼び出し時の HTTP リクエストには mcp-gateway からの最新トークンが注入される

---

## Q3. background 中に GitHub API を叩く設計が妥当か

### 現行設計の構造的分析

```
manager.Start()
    watchState.ctx = context.WithCancel(manager.ctx)
                     ↑ context.Background() 由来
    watchState.client = ghclient.NewClient(watchCtx, token, ...)
                                           ↑ watchCtx をHTTPクライアントに渡す

ghclient.NewClient():
    src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
    httpClient := oauth2.NewClient(ctx, src)  ← ctx は watchCtx
    // watchCtx がキャンセルされると進行中の HTTP req もキャンセル ← 正しい動作
```

### oauth2.NewClient の context バインディングの意味

`oauth2.NewClient(ctx, src)` の `ctx` は、生成される `http.Client` の transport に紐づく。
この context はキャンセル伝播のためにのみ使われ、**トークンの有効性とは無関係**。
実際の API リクエストのキャンセル境界は `client.GetReviewData(callCtx, ...)` の `callCtx`:

```go
// manager.go:pollOnce()
callCtx, cancel := context.WithTimeout(w.ctx, m.pollTimeout)  // 30秒タイムアウト
defer cancel()
data, err := client.GetReviewData(callCtx, w.key.owner, ...)
```

### 設計の妥当性評価

| シナリオ | 評価 |
|---|---|
| mcp-gateway が長命なトークン (GitHub PAT, 有効期限 > 2時間) を注入 | **問題なし** — ウォッチ最大継続時間 (2h) 以内にトークンが期限切れにならない |
| mcp-gateway が短命なトークン (GitHub App installation token, 有効期限 1h) を注入 | **リスクあり** — ウォッチ途中でトークンが失効する可能性 |
| goroutine のキャンセル伝播 | **正しく実装済み** — `watchCtx.cancel()` → HTTP リクエストがキャンセルされる |
| mcp-gateway のトークンキャッシュ無効化 | **非実装** — `InvalidateToken: nil` のため 401 時に gateway に通知されない |

### 最大リスク: mcp-gateway 側のトークン種別依存

`copilot-review-mcp` 単体では mcp-gateway が何種類のトークンを使うか不可視。
GitHub App installation token のデフォルト有効期限は **1時間**。
ウォッチの最大継続時間は **2時間**。
この組み合わせで必然的にトークン失効が発生する。

---

## Q4. AUTH_CONTEXT_UNAVAILABLE を upstream 側に入れるべきか

### 現行設計で `AUTH_CONTEXT_UNAVAILABLE` が発生するシナリオ

| シナリオ | 現状 |
|---|---|
| サーバー再起動後の active watch | `MarkActiveReviewWatchesStale()` で STALE 化済み → goroutine なし → 発生しない |
| トークン失効 | `FailureReasonAuthExpired` + `StatusFailed` で表現済み |
| goroutine がコンテキストを失う | `watchCtx` (manager.ctx 由来) はプロセス終了まで有効 → 発生しない |

### `AUTH_CONTEXT_UNAVAILABLE` が必要になるシナリオ (将来)

**Option C (gateway 委任型)** を採用した場合:

```
goroutine → gateway の token refresh API を呼ぶ
              ↓
            gateway が停止中 or 認証セッション切れ
              ↓
            トークンを取得できない → AUTH_CONTEXT_UNAVAILABLE
```

### 推奨: 現行設計では**導入不要**

- 現行の `FailureReasonAuthExpired` は "トークンが期限切れになり GitHub API が 401 を返した" を正確に表現している
- `AUTH_CONTEXT_UNAVAILABLE` は "goroutine が auth コンテキストにアクセスする手段がない (gateway 委任設計の場合)" を表す別概念
- 現行の Option B (スナップショット) では、auth コンテキストは常にスナップショット済みである

**もし導入するなら、適切な区別は:**

```
FailureReasonAuthExpired      = "AUTH_EXPIRED"        // 現行: トークンが失効し 401 が返った
(新規) FailureReasonNoContext = "AUTH_CONTEXT_UNAVAILABLE"  // Option C 用: auth を取得する手段がない
```

---

## Q5. Option A (request-scoped) に寄せるべきか

### Option A とは

> GitHub API 呼び出しを全てリクエストスコープ内に限定し、バックグラウンド goroutine を廃止する。
> ウォッチツールを同期ブロッキングに変更する。

### 既存の Option A 実装: `wait_for_copilot_review`

`tools/wait.go` に既に実装済みであり、ツール定義に明示:

```go
var waitTool = &mcp.Tool{
    Name: "wait_for_copilot_review",
    Description: "Legacy fallback。" +
        "Copilot のレビューが COMPLETED または BLOCKED になるまで、" +
        "この tool call 自体を block しながら定期ポーリングする。" +
        "通常は get_copilot_review_status と watch 系ツールを優先し、" +
        "この tool は host が通知や cheap status read を扱いにくい場合だけ使う。",
}
```

これはまさに Option A の実装であり、**Legacy fallback として位置づけられ非推奨化が進んでいる**。

### Option A が問題を引き起こす根拠

**① HTTP タイムアウトの危険性**

`main.go:69`:
```go
// WriteTimeout remains unlimited because legacy wait_for_copilot_review still exists
// as a blocking fallback and may occupy one tool call for up to 30 minutes.
server := &http.Server{
    WriteTimeout: 0,  // 無制限 — セキュリティリスク
}
```

Option A 化により `WriteTimeout: 0` が永続的に必要になる。

**② wait の上限制約**

`tools/wait.go`:
```go
const maxTotalWait = 30 * time.Minute
totalWait := time.Duration(in.PollIntervalSeconds) * time.Duration(in.MaxPolls-1) * time.Second
if totalWait > maxTotalWait {
    return error...
}
```

Copilot のレビューが 30 分以上かかるケースでは Option A ではカバー不可。
現行の非同期ウォッチは最大 **2時間** 継続可能。

**③ MCP Streamable HTTP での影響**

`mcp.NewStreamableHTTPHandler` は複数リクエストを同一 HTTP/2 ストリームまたは SSE で処理する。
1 つのツール呼び出しが数十分ブロックすると、同セッションの他ツール呼び出しが遅延する。

**④ mcp-gateway 側のプロキシタイムアウト**

mcp-gateway がアップストリームへの接続タイムアウトを設定している場合、長時間のブロッキングリクエストが途中で切断される。
これは Option B (非同期) では問題にならない — ウォッチ開始リクエストは即座に return するため。

### 比較表

| 観点 | Option A (request-scoped) | Option B (現行スナップショット) |
|---|---|---|
| 認証の単純さ | リクエストのトークンをそのまま使用 | スナップショット必要 |
| 接続ブロッキング | 最長 30 分 HTTP を占有 | ウォッチ開始は即 return |
| max 継続時間 | 30 分 (上限) | 2 時間 |
| WriteTimeout | 0 (無制限) 必須 | 同じく 0 (wait ツールが残る限り) |
| トークンローテーション対応 | 自動 (リクエストごとに最新) | スナップショット固定 |
| mcp-gateway 切断耐性 | なし (接続切断 = ウォッチ終了) | あり (goroutine は継続) |
| 既存実装 | `wait_for_copilot_review` (Legacy) | `start_copilot_review_watch` (現行) |

### 推奨: Option A への回帰は**非推奨**

- Option A は `wait_for_copilot_review` として既に存在し、"Legacy fallback" として明示的に降格済み
- 非同期設計の目的は HTTP ブロッキングを避け、mcp-gateway との接続をステートレスに保つことにある
- Option A に全面移行すると既存の設計的改善 (v3.0.0 の非同期ウォッチ導入) が無駄になる
- `WriteTimeout: 0` の危険な設定を `wait_for_copilot_review` の削除後に解消できなくなる

---

## まとめ: 各質問への回答

| 質問 | 回答 |
|---|---|
| **goroutine の token/login の取得元** | HTTP リクエストの Authorization / X-Authenticated-User ヘッダーから `startWatchHandler` が抽出し、`watchState.token` / `watchState.key.login` にスナップショット。goroutine はそのコピーのみを使用し、以後 mcp-gateway に依存しない |
| **watch state 復元後の auth** | 復元は行われない。`store.Open()` 時に `MarkActiveReviewWatchesStale()` が全 active ウォッチを STALE 化するため、auth が必要な goroutine は起動しない |
| **background GitHub API 設計の妥当性** | 長命トークン (PAT 等) では問題なし。短命トークン (GitHub App installation token, 1h) ではウォッチ途中で失効リスクあり。`InvalidateToken: nil` による gateway 通知欠如が副次的問題 |
| **AUTH_CONTEXT_UNAVAILABLE の導入** | 現行設計 (Option B スナップショット) では不要。Option C (gateway 委任) 採用時に初めて必要になる |
| **Option A への寄せ** | 非推奨。既に `wait_for_copilot_review` (Legacy) として実装済みであり、非同期設計の設計的優位性 (ブロッキング回避・2時間継続) を失う |
