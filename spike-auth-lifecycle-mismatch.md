# Spike 調査結果: Auth Lifecycle Mismatch for Background MCP Workflows

> 対象 Issue: https://github.com/scottlz0310/mcp-gateway/issues/70
> 調査リポジトリ: `scottlz0310/copilot-review-mcp`

---

## 1. 現在の認証ライフサイクル

```
[クライアント]
    │
    ▼ HTTP Request
[mcp-gateway]  ── OAuth/Token管理 ──► GitHub OAuth
    │
    ▼ ヘッダー注入
    Authorization: Bearer <token>
    X-Authenticated-User: <login>
    │
    ▼ HTTP Request (per-request)
[copilot-review-mcp / middleware/auth.go]
    │ login, token を request context に格納
    │ (リクエストが終わると context は破棄される)
    ▼
[tools/watch.go: startWatchHandler]
    │ loginFromToolRequest(ctx, req) → login 取得
    │ tokenFromToolRequest(ctx, req) → token 取得
    │
    ▼ manager.Start(StartInput{Login, Token, ...})
[watch/manager.go: watchState]
    │ watchState.token = <snapshotted token>  ← リクエスト時点で固定
    │ watchState.client = ghclient.NewClient(watchCtx, token, ...)
    │ watchState.ctx = context.WithCancel(manager.ctx)
    │                   ← manager.ctx は context.Background() 由来
    │                   ← 元の HTTP リクエスト context とは無関係
    ▼
[go m.run(state)]  ← バックグラウンドゴルーチン (最大2時間)
    │ snapshotされたトークンで定期ポーリング (デフォルト90秒間隔)
```

---

## 2. ウォッチワークフローのライフサイクル

| フェーズ | 詳細 |
|---|---|
| **開始** | `start_copilot_review_watch` 呼び出し時のリクエストから token をスナップショット |
| **実行中** | `manager.ctx` (process-global) 配下で goroutine が動作、元リクエスト context とは独立 |
| **ポーリング** | 90秒ごとに GitHub API (REST: ListReviewers, ListReviews, timeline) を呼び出す |
| **トークン更新** | 同一ユーザー/PRで再度 `start_copilot_review_watch` を呼ぶと新トークンに差し替え可能 |
| **最大継続時間** | `defaultMaxWatchDuration = 2 * time.Hour` で強制終了 |
| **認証失敗時** | HTTP 401 → `FailureReasonAuthExpired` → `StatusFailed` → LLM に `REAUTH_AND_START_NEW_WATCH` を返す |

---

## 3. 認証が失効/利用不可になる経路

| 経路 | 状況 | 現在の挙動 |
|---|---|---|
| **① mcp-gateway がトークンをローテーション** | ウォッチ実行中 (最大2時間) に gateway がトークンを更新 | スナップショットトークンが陳腐化。次回ポーリングで HTTP 401 → `FailureReasonAuthExpired` で終了 |
| **② サーバー再起動** | in-memory ウォッチが消滅、DBには状態が残るが token は未保存 | DB から snapshot を返すが worker は不在。`start_copilot_review_watch` 再呼び出しで新ウォッチが起動 |
| **③ InvalidateToken が nil** | `server.go:175` で `InvalidateToken: nil` が設定されている | ウォッチクライアントに `invalidatingTransport` がインストールされない。401 時に gateway へのトークン無効化通知が行われない |
| **④ MCP セッション切断後の resource 通知** | クライアントがウォッチリソースを subscribe 後にセッションが切断 | SDK が `ResourceUpdated` を送出するが、切断済みセッションには届かない |

---

## 4. 各仮説の検証結果

### 仮説1: リクエストスコープの認証とバックグラウンドウォッチのライフサイクル不一致

**→ 確認済み (アーキテクチャ上の現実) だが既存設計で部分対処済み**

- `watchState.token` にトークンをスナップショットする設計は **実装済み** (`watch/manager.go:328`)
- バックグラウンドゴルーチンは `manager.ctx` (process-global) を使用し、HTTP リクエスト context から独立している
- **リスク**: ウォッチ実行中に mcp-gateway がトークンをローテーションした場合、スナップショットが陳腐化する

### 仮説2: ゲートウェイのトークン更新と上流ヘッダー注入が経路によって異なる

**→ 部分的に関連あり**

- 同一ユーザー/PR で `start_copilot_review_watch` を再呼び出すとトークンが差し替えられる仕組みは **実装済み** (`manager.go:295-313`)
- ただし、ウォッチ自身がトークン更新をゲートウェイに要求する能動的な仕組みは **存在しない**
- `InvalidateToken: nil` のため、HTTP 401 時にゲートウェイへのキャッシュ無効化通知が行われない

### 仮説3: 複数コンポーネントのライフサイクル非整合

**→ 確認済み (既存設計で部分対処済み)**

```
MCP セッション    : 無期限 (MCP_SESSION_TIMEOUT_MIN=0 がデフォルト)
gateway トークン  : gateway の管理下 (copilot-review-mcp からは不可視)
GitHub アクセストークン : トークン有効期限まで
ウォッチ          : 最大 2 時間
```

最大2時間のウォッチ期間内にゲートウェイのトークン更新が起きることが最も現実的なリスク。

### 仮説4: AUTH_CONTEXT_UNAVAILABLE エラーカテゴリが必要

**→ 現行設計では不要だが、サーバー再起動シナリオで有用**

- トークンは常にウォッチ開始時のリクエストから取得されるため、「コンテキストなし」状態は通常発生しない
- **例外**: サーバー再起動後、DB上の `is_active=true` ウォッチは worker が不在
  - 現在: `WorkerRunning: false` として報告し、LLM が `start_copilot_review_watch` を再呼び出す
  - `AUTH_CONTEXT_UNAVAILABLE` を導入すれば再起動後の状態をより明確に伝えられる

---

## 5. 修正はどのコンポーネントに属するか

| 問題 | 修正コンポーネント | 優先度 |
|---|---|---|
| トークン陳腐化による `FailureReasonAuthExpired` | **設計上の許容範囲** — LLM が `REAUTH_AND_START_NEW_WATCH` に従えばよい | 低 |
| `InvalidateToken: nil` (gateway への 401 通知なし) | `copilot-review-mcp` (`server.go:175`) + `mcp-gateway` が無効化 API を提供する必要 | 中 |
| mcp-gateway がトークン更新した際の upstream への通知 | **mcp-gateway** が更新トークンを後続リクエストで注入する | gateway 側の設計次第 |
| サーバー再起動後の `is_active` ウォッチの不整合 | `copilot-review-mcp` — 起動時に DB の `is_active` ウォッチを `STALE` に移行するクリーンアップ | 中 |

---

## 6. 推奨設計オプション

### 推奨: オプションB (現行) + 起動時クリーンアップ

- トークンスナップショット設計は正しく、追加の複雑さなしにバックグラウンドポーリングを実現
- 追加で実施すべき:
  1. **起動時DBクリーンアップ**: 起動時に `is_active=true` の DB エントリを `STALE` に更新し、サーバー再起動後の phantom active watches を解消
  2. **`InvalidateToken` の活性化**: mcp-gateway が token invalidation endpoint を提供するなら、`server.go` の `InvalidateToken: nil` を実際のコールバックに置き換える
  3. **新エラー区分**: `AUTH_CONTEXT_UNAVAILABLE` は現行では不要。ただし将来的にオプションC (gateway委任) を採用する場合は導入価値あり

### オプションC (gateway委任) は長期的に検討

- ウォッチジョブがトークンを保持せず、gateway に都度 authenticated API call を依頼する設計
- セキュリティ上の利点 (スナップショットトークンなし) はあるが、アーキテクチャの複雑度が大きく増加

---

## 7. 観察された認証期限切れの再現条件

**最も発生しやすいシナリオ**:
1. mcp-gateway がセッション単位ではなくリクエスト単位でトークンを更新するケース
2. GitHub OAuth アクセストークンの有効期限が2時間未満のケース (細粒度アクセストークン等)
3. サーバー再起動後に DB から古いウォッチ状態を読み込んで worker がいないケース

**再現手順** (シナリオ①):
1. `start_copilot_review_watch` を呼び出しウォッチ開始
2. mcp-gateway 側でトークンをローテーション
3. 次のポーリングサイクル (90秒後) で GitHub API が HTTP 401 を返す
4. `FailureReasonAuthExpired` + `StatusFailed` になることを確認

---

## 8. 調査対象ファイル

| ファイル | 調査内容 |
|---|---|
| `internal/middleware/auth.go` | ヘッダーから login/token を context に注入する認証ミドルウェア |
| `internal/watch/manager.go` | バックグラウンドウォッチの goroutine 管理、トークンスナップショット、`AUTH_EXPIRED` 検知 |
| `internal/tools/watch.go` | MCP ツールハンドラ、リクエストから login/token を取り出して Manager に渡す |
| `internal/tools/server.go` | `BuildStreamableHandler` — `InvalidateToken: nil` の設定箇所 (line 175) |
| `internal/github/client.go` | `invalidatingTransport`、`IsAuthError`、`NewClient` |
| `cmd/server/main.go` | サーバー起動、auth middleware の適用 |

---

**結論**: フォローアップ実装 issue として以下の2つを推奨します。
- `copilot-review-mcp` の起動時 DB クリーンアップ (STALE 移行)
- mcp-gateway が token invalidation hook を提供する場合の `InvalidateToken` 活性化

オプションA (GitHub API 呼び出しをリクエストスコープのみに制限) はウォッチ機能の根幹を壊すため非推奨です。
