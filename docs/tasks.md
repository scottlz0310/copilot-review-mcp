# review-raven 改善タスク一覧

## ドキュメント一般化計画（ISSUE#63 後続）

ISSUE#63 は PR#64 でクローズ済み。`docs/architecture.md` / `docs/architecture.ja.md` は更新完了。
以下のファイルに「Copilot をメインターゲットとする記述」が残っているため、reviewed-side MCP としての
再定義コンセプトに合わせて一般化する。

**変更しないもの**: ツール名（`request_copilot_review`、`get_copilot_review_status`、`start_copilot_review_watch` 等）は公開 API 互換維持のため変更しない（`docs/architecture.md#migration--compatibility` 参照）。

---

### 高優先度 — メインの誤ったフレーミング

#### `README.md` L3

```
変更前:
An MCP (Model Context Protocol) server that manages GitHub Copilot PR review cycles.
Provides review request, completion detection, staleness detection, and thread reply/resolve
through an **async watch + notification** model designed for LLM agents.

変更後（案）:
An MCP (Model Context Protocol) server for the **reviewed side** of a PR review workflow.
Reads review threads, replies, resolves, and requests re-review — regardless of whether the
review came from Copilot, a human reviewer, or a bot. Built on an **async watch + notification**
model designed for LLM agents.
```

#### `README.ja.md` L6

```
変更前:
GitHub Copilot の PR レビューサイクルを管理する MCP（Model Context Protocol）サーバー。
レビュー依頼・完了検知・staleness 判定・スレッド返信／解決までを LLM 向けの
async watch + notification モデルで提供する。

変更後（案）:
PR レビューを受けて直す側の MCP（Model Context Protocol）サーバー。
Copilot review・human reviewer・bot reviewer を問わず、review thread の読み取り・返信・
resolve・再レビュー依頼を LLM 向けの async watch + notification モデルで提供する。
```

---

### 中優先度 — スキルドキュメントの Copilot 固有表現

#### `docs/skills/pr-review-cycle.md`

| 箇所 | 変更前 | 変更後（案） |
|---|---|---|
| frontmatter `description` | "Waits for Copilot review completion via async watch polling" | "Waits for review completion via async watch polling" |
| L11 本文 | "wait for Copilot review completion via **async watch polling**" | "wait for review completion via **async watch polling**" |
| L29 セットアップ表 | "Copilot review watch & thread operations" | "Review watch & thread operations" |
| L99（1-C タイムアウト投稿文） | "Copilot review completion wait timed out after 15 minutes." | "Review completion wait timed out after 15 minutes." |
| L329 ツール対応表 | "Instant check of Copilot review state on GitHub" | "Instant check of review state on GitHub" |
| L330 ツール対応表 | "Request a Copilot review" | "Request a review" |

#### `docs/skills/pr-review-cycle.ja.md`

| 箇所 | 変更前 | 変更後（案） |
|---|---|---|
| frontmatter `description` | "Copilot レビュー完了を async watch ポーリング（...）で待機してから" | "レビュー完了を async watch ポーリング（...）で待機してから" |
| L10-11 本文 | "Copilot レビュー完了を **async watch ポーリング**で待機してから" | "レビュー完了を **async watch ポーリング**で待機してから" |
| L29 セットアップ表 | "Copilot レビュー watch・スレッド操作" | "レビュー watch・スレッド操作" |
| L103（1-C タイムアウト投稿文） | "Copilot レビュー完了待機がタイムアウトしました（15 分）。" | "レビュー完了待機がタイムアウトしました（15 分）。" |
| L341 ツール対応表 | "GitHub 上の Copilot レビュー状態を即時確認" | "GitHub 上のレビュー状態を即時確認" |
| L342 ツール対応表 | "Copilot レビューを依頼" | "レビューを依頼" |

---

### 低優先度 — 単語レベル

#### `docs/watch-tools.md` L39

```
変更前: The Copilot review has reached `COMPLETED` or `BLOCKED`.
変更後: The review has reached `COMPLETED` or `BLOCKED`.
```

#### `docs/watch-tools.ja.md` L39

```
変更前: Copilot review が `COMPLETED` または `BLOCKED` に到達した。
変更後: review が `COMPLETED` または `BLOCKED` に到達した。
```

#### `docs/usage.md` L225

```
変更前: 1. Check or request a Copilot review.
変更後: 1. Check or request a review.
```

#### `docs/usage.ja.md` L227

```
変更前: 1. Copilot review の状態確認または依頼
変更後: 1. レビューの状態確認または依頼
```


> 旧内容は `docs/archive/tasks-legacy-2026_06_08.md` に退避済み。
