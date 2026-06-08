# ドキュメント一般化計画（完了済み）

> **Completed 2026-06-08** — PR#65 でマージ済み。

ISSUE#63 の方針に基づき、README・スキル・watch-tools・usage の各ドキュメントに
残っていた Copilot メインターゲット記述を一般化した。
ツール名（request_copilot_review 等）は公開 API 互換のため変更しない。

---

## 変更対象ファイルと実施内容

### 高優先度 — メインの誤ったフレーミング（完了）

#### `README.md`

```
変更前:
An MCP (Model Context Protocol) server that manages GitHub Copilot PR review cycles.

変更後:
An MCP (Model Context Protocol) server for the **reviewed side** of a PR review workflow.
Reads, replies to, and resolves review threads regardless of review provider (Copilot,
human reviewer, or bot). Re-review requests are routed through the configured acquisition provider.
```

#### `README.ja.md`

```
変更前:
GitHub Copilot の PR レビューサイクルを管理する MCP（Model Context Protocol）サーバー。

変更後:
PR レビューを受けて直す側の MCP（Model Context Protocol）サーバー。
review thread の読み取り・返信・resolve は reviewer provider を問わず対応する。
再レビュー依頼は設定された acquisition provider 経由で行う。
```

### 中優先度 — スキルドキュメントの Copilot 固有表現（完了）

- `docs/skills/pr-review-cycle.md` — frontmatter description、本文4箇所、ツール対応表2行
- `docs/skills/pr-review-cycle.ja.md` — 同等箇所

### 低優先度 — 単語レベル（完了）

- `docs/watch-tools.md` — `READ_REVIEW_THREADS` 説明の「Copilot review が」を除去
- `docs/watch-tools.ja.md` — 同上
- `docs/usage.md` — quick start 手順の「Copilot review」を除去
- `docs/usage.ja.md` — 同上
