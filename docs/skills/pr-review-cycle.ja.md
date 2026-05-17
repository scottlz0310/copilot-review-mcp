---
name: pr-review-cycle
description: Copilot レビュー完了を async watch ポーリング（mcp-resource-subscriber 不要）で待機してから PR レビュー対応サイクル（スレッド取得→分類・採否判断→修正→返信→サイクル評価→サマリ投稿）を自律実行する。PR 作成直後・Copilot レビュー依頼直後に呼び出す。マージは自律実行しない。
---

# pr-review-cycle スキル

[English](pr-review-cycle.md)

`copilot-review` サーバーの watch ツール群を使い、Copilot レビュー完了を
**async watch ポーリング**で待機してから PR レビュー対応サイクルを自律実行するスキル。

MCP ツールのみを使用し、`mcp-resource-subscriber` などのサブスクリプション系外部 CLI は不要。
`mcp-resource-subscriber` が利用できない環境向けのポーリングベース代替スキル（`pr-review-subscribe` の代替）。

> **このファイルについて**
> `docs/skills/pr-review-cycle.md` はリポジトリ共有用テンプレートです。
> 個人の AI エージェント設定（`~/.claude/skills/` 等）にコピーしてご利用ください。
> MCP サーバーキーはお使いの環境に合わせて読み替えてください。

---

## セットアップ

### 必要な MCP サーバー

| サーバー | 役割 | 参照 |
|---------|------|------|
| `copilot-review` | Copilot レビュー watch・スレッド操作 | [README.ja.md](../../README.ja.md) |
| `github` | Issue/PR コメント投稿 | [README.ja.md](../../README.ja.md) |

### プレースホルダーの読み替え

| プレースホルダー | 役割 | VS Code での例 |
|----------------|------|---------------|
| `{CRM}` | `copilot-review` サーバーツール | `mcp__copilot-review__*` |
| `{GH}` | `github` サーバーツール | `mcp__github__*` |

> ツール名プレフィックスはお使いの MCP クライアント設定によって異なります。IDE の MCP 設定で正確なプレフィックスを確認してください。

---

## 全体フロー

```
Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 → Phase 6
                ↑                                              ↓ WAIT / REQUEST_REREVIEW
                └──────────────────────────────────────────────┘
                                        ↓ READY_TO_MERGE
                              Phase 6.5 → Phase 6.6 → Phase 7 → Phase 8
                                        ↓ ESCALATE
                                      終了（ユーザーに報告）
```

---

## Phase 0: スナップショット確認

1. `owner`、`repo`、`pr`（PR番号）を確定する。
2. `{CRM}:get_copilot_review_status` で GitHub 上の現状を即時確認する。
3. `status = COMPLETED` または `BLOCKED`: → Phase 2 へ（watch 不要）。
4. `status = NOT_REQUESTED`: `{CRM}:request_copilot_review` でレビュー依頼後 → Phase 1 へ。
5. `status = PENDING` / `IN_PROGRESS`: → Phase 1 へ。

## Phase 1: async watch 開始＋完了待機

**待機開始時刻を記録する。** Phase 6 からループで戻ってきた場合も新たに記録し直す（タイムアウトは Phase 1 進入ごとに 15 分リセット）。

### 1-A: Watch 開始

`{CRM}:start_copilot_review_watch` を呼ぶ（即時 return）。

記録する項目:
- `watch_id`
- `resource_uri`（`copilot-review://watch/{watch_id}`）
- `next_poll_seconds`

同一 PR の active watch があれば再利用される。

### 1-B: 完了をポーリング

まず `start_copilot_review_watch` レスポンスの `recommended_next_action` を確認する。
すでに terminal action（例: `READ_REVIEW_THREADS`）であれば、待機せずに即座に対応アクションへ進む。

そうでない場合は `next_poll_seconds` 秒待ってから `{CRM}:get_copilot_review_watch_status` を呼ぶ
（最小 1 秒、サーバー既定は 90 秒）。`POLL_AFTER` 以外になるまで繰り返す。

`recommended_next_action` に従う:

| recommended_next_action | 対応 |
|------------------------|------|
| `READ_REVIEW_THREADS` | → Phase 2 へ |
| `POLL_AFTER` | 指定秒数後に再ポーリング |
| `CHECK_FAILURE` | エラー内容をユーザーに報告してサイクル中断 |
| `REAUTH_AND_START_NEW_WATCH` | ユーザーに再認証を促して終了 |
| `START_NEW_WATCH` | Phase 1-A へ戻る |

### 1-C: タイムアウト処理（経過時間 ≥ 15 分）

1. `{CRM}:cancel_copilot_review_watch` で watch をキャンセル。
2. `{GH}:add_issue_comment` で以下を投稿:
   `Copilot レビュー完了待機がタイムアウトしました（15 分）。手動で再開してください。`
3. ユーザーに手動再開方法を案内して終了。

## Phase 2: スレッド取得

`{CRM}:get_review_threads` を呼ぶ。

**未解決スレッドが 0 件の場合のルーティング**（両ケースとも → 下記デフォルト設定後に Phase 6.5 へ）:
- `cycles_done = 0` かつ 未解決 = 0: 初回レビューで Copilot が「問題なし」と判断した。
- `cycles_done ≥ 1` かつ 未解決 = 0: 再レビュー完了・新規問題なし。前サイクルの修正が承認された。

Phase 3〜6 をスキップする場合、Phase 7/8 向けに以下のデフォルト値を使用する:
- `termination_status = READY_TO_MERGE`
- `override_applied = no`
- `final_cycle_fix_types`: blocking × 0, non-blocking × 0, suggestion × 0, trivial × 0

未解決スレッドが 1 件以上の場合は Phase 3 へ進む。

## Phase 3: 分類・採否判断（自律）

各未解決コメントを以下の基準で分類し、`accept` / `reject` を自律的に決定する:

| 分類 | 基準 |
|------|------|
| `blocking` | 実行時エラー・データ整合性の破壊・セキュリティリスク・破壊的変更・公開記録の不整合 |
| `non-blocking` | テスト追加・ログ改善・プライバシー・一貫性の改善など対応推奨だが必須ではないもの |
| `suggestion` | 設計・命名・構造・保守性の改善提案 |

`reject` の場合は具体的な理由を明記する: スコープ外・対応済み・前提が誤り・意図的な先送り等。

**Reject 制約 — スコープ外・先送りはトラッキング Issue 必須。**
`out-of-scope`、`deferred`、`follow-up` を理由とする reject は、
フォローアップ Issue にトレース可能になるまで完了とみなさない。
`Follow-up issue` 列に必ず記入すること。

フォローアップ Issue が不要な reject 理由:
- `already-handled` — コミット / PR / Issue を引用する。
- `invalid-premise` — 誤解の内容を説明する。
- `wont-fix` — 明示的な不対応決定。「後で対応」と書いてはならない。

修正前に以下のテーブルを提示する:

```
| # | スレッド ID | 分類 | 採否 | 概要 | reject 理由 | フォローアップ Issue |
|---|------------|------|------|------|------------|---------------------|
```

Phase 6 で使用する `fix_type` を決定する:

| fix_type | 該当ケース |
|----------|-----------|
| `logic` | コードの動作またはテストのみの変更 |
| `spec_change` | 公開ドキュメント・API・ワークフロー・互換性記録のセマンティクス変更 |
| `trivial` | typo・フォーマット・文言のみの修正 |
| `none` | 修正なし（全 reject） |

## Phase 4: 修正＋コミット

1. `git status --short --branch` を実行する。
2. `accept` した項目のみ修正する。
3. **修正粒度**: 1 スレッド = 1 論理変更単位（atomic）。共通編集が明らかに整理される場合を除く。
4. 全修正完了後にビルド・テストを再実行。失敗したら修正してリトライ。解消不能な場合はサイクル中断してユーザーに報告。
5. Phase 4 完了後に**まとめて 1 コミット**する（Conventional Commits 形式）。
6. ユーザーが明示的に求めない限り force push しない。

関係のないユーザー変更を revert しない。

## Phase 5: スレッド返信＋resolve

全スレッドに対して `{CRM}:reply_and_resolve_review_thread` を呼ぶ。

- **修正済み**: コミットと具体的な修正内容を言及する。
- **reject**: 理由を明確に述べる。ステップ 4 の例外を除き常に `resolve=true` を設定する。

### Reject 返信ルール

スコープ外の reject はフォローアップ Issue にトレース可能になるまで完了ではない。

#### 1. 既存 Issue のリンク

`Tracked by #xxx` または `Follow-up: #xxx` を返信に含める。
リンクする Issue が実際にその reject 内容をカバーしていることを確認する
— 関係ない目的で開かれた Issue を流用しない。

#### 2. 新規フォローアップ Issue の作成

カバーする既存 Issue がない場合:
1. `{GH}:create_issue` で PR・スレッドを参照した説明的なタイトル・本文で Issue を作成する。
2. 新しい Issue 番号を記録する。
3. `Follow-up: #<番号>` を返信に含める。
4. Phase 3 の決定テーブルと Phase 7 サマリに番号を記録する。

#### 3. 明示的な `Won't fix`

`Won't fix` と具体的な理由を返信に書く。
「後で対応」「別 Issue に先送り」「フォローアップ予定」という表現は禁止
— それらはステップ 1 または 2 が必要であることを意味する。

#### 4. Issue 作成・リンクが不可能な場合

- スレッドを resolve しない（`resolve=false`）。
- Phase 7 に `untracked — needs follow-up issue` として記録する。

## Phase 6: サイクル評価

`{CRM}:get_pr_review_cycle_status` を以下の引数で呼ぶ:

```json
{
  "owner": "<owner>",
  "repo": "<repo>",
  "pr": 42,
  "cycles_done": 0,
  "max_cycles": 0,
  "fix_type": "<Phase 3 で決定した fix_type>"
}
```

> `max_cycles: 0` でサーバー側デフォルト（環境変数 `MAX_REVIEW_CYCLES`、未設定時 3）を使用する。
> `cycles_done` は 0 始まりの整数。Phase 1 進入ごとにインクリメントする（初回: `0`、2 サイクル目: `1`、…）。

`recommended_action` に従う:

| recommended_action | 次のアクション |
|-------------------|---------------|
| `WAIT` | `cycles_done` をインクリメントして Phase 1 へ戻る |
| `REPLY_RESOLVE` | Phase 2 へ戻る（未解決スレッドが残っている） |
| `REQUEST_REREVIEW` | 下記オーバーライドルール参照; 通常は `{CRM}:request_copilot_review` → `cycles_done` インクリメント → Phase 1 |
| `READY_TO_MERGE` | Phase 6.5 へ |
| `ESCALATE` | 分類・報告（下記参照）してサイクル終了 |

**`REQUEST_REREVIEW` オーバーライド**: `recommended_action = REQUEST_REREVIEW` かつ
`cycles_done ≥ 1` かつ 今サイクルの Phase 2 での未解決スレッド数 = 0 の場合、
再レビューを依頼しない。`READY_TO_MERGE` として扱い Phase 6.5 へ進む。

**終了分類**:

| 分類 | 条件 | マージへの影響 |
|------|------|----------------|
| ✅ `READY_TO_MERGE` | `recommended_action = READY_TO_MERGE`、またはオーバーライード適用で `unresolved = 0` | 安全 — 通常のマージゲート |
| 🟡 `ESCALATE — Clean` | `ESCALATE` かつ最終サイクルの accept に `blocking` なし | おそらく安全 — 未検証の旨を注記 |
| 🔴 `ESCALATE — Unverified Fix` | `ESCALATE` かつ最終サイクルで `blocking` fix を 1 件以上 accept したが Copilot が再レビューしていない | 危険 — マージ前に人間レビュー推奨 |

Phase 7 用に記録する:
- `termination_status`
- `final_cycle_fix_types`: `blocking` / `non-blocking` / `suggestion` / `trivial` の accept 件数
- `override_applied`: `yes` または `no`
- `unverified_blocking_commits`: `ESCALATE — Unverified Fix` 時のコミット SHA リスト

`ESCALATE — Unverified Fix` の場合も Phase 6.5 / 6.6 / 7 へ進むが、
Phase 8 では CI 結果に関わらずマージ準備完了を格下げする。

## Phase 6.5: CI 確認

1. `gh pr checks <PR番号>` で CI 状態を確認する。
2. 全ジョブ SUCCESS → Phase 6.6 へ。
3. 失敗ジョブあり: `gh run view <run-id> --log-failed` でログを取得し原因分析。
   - **修正可能**（コード不備・明確な失敗原因）: 採否テーブルに追加して Phase 4 へ戻る。
   - **修正困難**（環境要因・flaky test・原因不明）: ユーザーに報告して指示を仰ぐ。

`gh` が利用不可または PR のチェックにアクセスできない場合は `{GH}` / GitHub MCP サーバー経由で確認する。
どちらのルートでも CI を確認できない場合は `CI: unknown` を報告して Phase 7 の前に停止する。

## Phase 6.6: カバレッジ確認

Codecov 等のカバレッジ PR コメントを確認する（存在しない場合はスキップ → Phase 7 へ）。

- テストで解消できるカバレッジのギャップが導入されている場合: Phase 4 へ戻る（`fix_type = logic`）。
- 関連するカバレッジシグナルがない、または問題がない場合: Phase 7 へ進む。

## Phase 7: サマリコメント投稿

`{GH}:add_issue_comment` で以下を PR に投稿する:

```markdown
## レビュー対応サマリ

### 修正内容
- （概要を箇条書き）

### 採否判断
- accept: N 件
- reject: M 件
  - Thread <threadId> (PRRT_xxx): （理由）

### 先送り・スコープ外項目
- なし | <リスト: Thread <threadId> (PRRT_xxx) — 概要>

### 検証
- CI: ...
- 未解決スレッド: ...
- サイクルステータス: <termination_status>
  - `ESCALATE — Unverified Fix` の場合: 理由・未検証コミット SHA・「マージ前に人間レビュー推奨」を明記
- 最終サイクル修正タイプ: blocking × N, non-blocking × N, suggestion × N, trivial × N
- オーバーライード適用（0 件再レビュー）: yes | no
```

**`先送り・スコープ外項目` ルール:**

- `out-of-scope` / `deferred` / `follow-up` を理由とする全 reject を、フォローアップ Issue 番号付きでリストしなければならない。
- `- なし` は、それらの理由で reject した件数が 0 件、かつ Phase 5 ステップ 4 で未解決のまま残ったスレッドがない場合のみ許容。
- 未トラッキングの項目は `Thread <threadId> (PRRT_xxx) — untracked — needs follow-up issue (Phase 5 step 4)` として明示する。
- `Won't fix` の reject はこのセクションに含めない。

## Phase 8: マージ判断

**自律的にマージしない。** ユーザーからの明示的な指示を待つ。

マージ条件（ユーザー指示時に満たすこと）:
- CI 全ジョブ SUCCESS
- 未解決レビュースレッド = 0 件
- 全スレッドに返信済み
- 未解決の `blocking` 項目なし
- `termination_status` が `READY_TO_MERGE` または `ESCALATE — Clean`

`termination_status = ESCALATE — Unverified Fix` の場合:
1. CI グリーン・未解決 0 件でも **マージ準備完了とは報告しない**。
2. 未検証コミット SHA を付けて警告を明確に提示する。
3. ユーザーがそれでもマージを要求する場合は、未検証 blocking 修正を手動レビュー済みであることを明示的に確認してから進める。

条件が満たされない場合は欠けている項目を報告して指示を仰ぐ。

---

## 注意事項

- ポーリング間隔: レスポンスの `next_poll_seconds` に従う（最小 1 秒、サーバー既定 90 秒）
- タイムアウト: Phase 1 進入ごとに 15 分リセット
- 再レビュー上限: サーバー側 `MAX_REVIEW_CYCLES`（デフォルト 3）。`ESCALATE` 以降は人間判断
- 修正粒度: スレッド単位 atomic（1 スレッド = 1 論理変更単位）
- コミット戦略: Phase 4 完了後まとめて 1 コミット（Conventional Commits 形式）
- Phase 3 の採否判断は自律だが結果テーブルは必ず提示（監査性のため）
- Phase 8 は明示指示待ち（操作安全基準）

---

## ツール対応表

| ツール名 | 用途 |
|---------|------|
| `{CRM}:get_copilot_review_status` | GitHub 上の Copilot レビュー状態を即時確認 |
| `{CRM}:request_copilot_review` | Copilot レビューを依頼 |
| `{CRM}:start_copilot_review_watch` | async watch 開始（即時 return） |
| `{CRM}:get_copilot_review_watch_status` | watch の現在状態をポーリング |
| `{CRM}:cancel_copilot_review_watch` | watch をキャンセル |
| `{CRM}:get_review_threads` | レビュースレッドを一覧取得 |
| `{CRM}:reply_and_resolve_review_thread` | スレッドに返信して resolve |
| `{CRM}:get_pr_review_cycle_status` | サイクル評価・次アクション判定 |
| `{GH}:add_issue_comment` | PR にコメント投稿 |
| `{GH}:create_issue` | フォローアップトラッキング Issue を作成 |
