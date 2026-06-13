---
name: thread-owl-review-cycle
description: "thread-owl レビュー用の reviewed-side cycle スキル。thread-owl のレビュースレッドを読み、分類・修正・返信・resolve を行い、再レビューが必要な場合は @thread-owl re-review requested コメントを投稿して cycle を完了する。thread-owl がレビューを投稿した後（PR に unresolved スレッドが存在する状態）で呼び出す。"
---

# thread-owl-review-cycle スキル

[English](thread-owl-review-cycle.md)

> **スコープ: thread-owl レビュー専用。**
> このスキルは reviewer が **thread-owl** の場合の reviewed-side cycle を担当する。
> **Copilot** review（async watch ポーリング）には [`pr-review-cycle`](pr-review-cycle.ja.md) を使うこと。

thread-owl がレビュアーの場合に reviewed-side cycle を実行するスキル。Copilot watch ループはない。エントリーは thread-owl が新しいレビューを投稿した後（PR に unresolved な thread-owl スレッドが存在する状態）に行う。thread-owl review の通知を受け取ったらこのスキルを起動すること。

再レビュー依頼は `@thread-owl re-review requested` PR コメントとして投稿する。reviewed-side cycle はそこで完了する。thread-owl は自身の `issue_comment.created` webhook でこのコメントを検知し、`re-review-requested` candidate を enqueue して `queue://review/re-review-requests` の subscriber（reviewer-side）に通知する。次の reviewer-side cycle はこの通知で起動する。

> **このファイルについて**
> `docs/skills/thread-owl-review-cycle.ja.md` はリポジトリ共有用テンプレートです。
> 個人の AI エージェント設定（`~/.gemini/antigravity-cli/skills/` や `~/.claude/skills/` 等）にコピーしてご利用ください。
> MCP サーバーキーはお使いの環境に合わせて読み替えてください。
> 
> **インストール済みSkillの更新手順**
> すでに個人の AI エージェント設定に本スキルをインストールしている場合、この最新のテンプレートファイルの内容でインストール先の `SKILL.md` を上書きして更新を反映してください。

---

## セットアップ

### 必要な MCP サーバー

| サーバー | 役割 | 参照 |
|---------|------|------|
| `github` | PR コメント投稿・Issue 作成 | [README.ja.md](../../README.ja.md) |
| `review-raven` | PR レビュースレッドの取得・返信・解決 | [README.ja.md](../../README.ja.md) |

> このスキルでは、第一選択として `review-raven` MCP ツールを使用してスレッドの取得・返信・解決を行います。MCP ツールが利用不可能な場合のフォールバックとして `gh` CLI（GraphQL/REST API）を使用します。

### プレースホルダーの読み替え

| プレースホルダー | 役割 | 例 |
|----------------|------|-----|
| `{GH}` | `github` サーバーツール | `mcp__github__*` |
| `{RAVEN}` | `review-raven` サーバーツール | `mcp__review-raven__*` |

---

## 全体フロー

```
Phase 0（エントリー・cycles_done 復元）
  |
  v
Phase U2: スレッド取得 → Phase 3: 分類 → Phase 4: 修正 → Phase U5: 返信/resolve
                                                                    |
                                                        Phase U6: サイクル評価
                                                                    |
                                    ┌───────────────────────────────┘
                                    ↓ READY_TO_MERGE（再レビュー不要）
                          Phase 6.5 → Phase 6.6 → Phase 7 → Phase 8
                                    ↓ ESCALATE（最大サイクル超過）
                          Phase 6.5 → Phase 7 → Phase 8
                                    ↓ REQUEST_REREVIEW（cycles_done < max_cycles）
                          @thread-owl コメント投稿 → reviewed-side cycle 完了
```

---

## Phase 0: エントリー・サイクルカウント復元

1. `owner`、`repo`、`pr` を確定する。
2. `max_cycles = 3` を設定する（必要に応じて調整）。
3. `cycles_done` と `handled_comments`（処理済みの非スレッドコメントID）を PR コメント履歴から復元する:
   - PR の issue comment を検索し、最新の `<!-- review-raven: cycles_done=N, handled_comments=ID1,ID2,... -->`（または `cycles_done=N` 単体）を見つける。
   - `cycles_done`: 見つかった場合 `N + 1`、見つからない場合 `0`。
   - `handled_comments`: アノテーション内の `handled_comments` にリストされているID群を記録してセット（既処理リスト）を作成する。見つからない場合は空。
4. Phase U2 へ進む。

## Phase U2: レビュー指摘の収集

以下の3つの手段を用いて、PRに投稿されたすべての指摘を収集します。

### 1. インラインレビュースレッドの取得
**第一選択 (review-raven MCP)**: `{RAVEN}:get_review_threads` を実行して全レビュースレッドを取得します:
- `owner`: `<owner>`
- `repo`: `<repo>`
- `pr`: `<pr>`

**フォールバック (gh CLI)**: MCP ツールが使用できない場合は、GraphQL を用いて `gh` CLI で全レビュースレッドを取得します。
```bash
gh api graphql -f query='
  query($owner: String!, $repo: String!, $pr: Int!, $cursor: String) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $pr) {
        reviewThreads(first: 100, after: $cursor) {
          pageInfo { hasNextPage endCursor }
          nodes {
            id
            isResolved
            comments(first: 10) {
              nodes {
                databaseId
                body
                path
                line
                author { login }
                createdAt
              }
            }
          }
        }
      }
    }
  }
' -f owner=<owner> -f repo=<repo> -F pr=<pr>
```
`pageInfo.hasNextPage` が `true` の場合、`-f cursor=<endCursor>` で繰り返します。

---

`isResolved = false` のすべてのスレッド（inline thread）を収集します。author（`thread-owl`、`thread-owl[bot]`、repository owner、GitHub App、Copilot、human reviewer等）にかかわらず、未解決の指摘はすべて対象とします。各スレッドの `id`（PRRT ノード ID — resolve 用）を記録します。また、`gh` CLI によるフォールバック取得時はルートコメントの `databaseId`（返信用）も記録します。

### 2. レビュー本文（review body）の取得
スレッド化されていないレビューの全体コメント（review body）を取得します。
```bash
gh api repos/<owner>/<repo>/pulls/<pr>/reviews --paginate --jq '.[] | select(.body != "") | {id: .id, body: .body, author: .user.login, state: .state}'
```
取得したレビュー本文の中から、具体的な修正や対応を求めている `actionable` な指摘を抽出します。
**既処理チェック**: 抽出したコメントの `id` が Phase 0 で復元した `handled_comments` に含まれている場合は、処理済み（Resolved）としてスキップします。未対応のもののみ、コメントの `id`、`author`、`body` を記録します。

### 3. PRコメント（issue comment）の取得
スレッド形式になっていないPR全体のコメントを取得します。
```bash
gh api repos/<owner>/<repo>/issues/<pr>/comments --paginate --jq '.[] | {id: .id, body: .body, author: .user.login}'
```
取得したコメントの中から、`actionable` な指摘を抽出します。
**既処理チェック**: 抽出したコメントの `id` が Phase 0 で復元した `handled_comments` に含まれている場合は、処理済みとしてスキップします。未対応のもののみ、コメントの `id`、`author`、`body` を記録します。

---

未解決の指摘（未解決のインラインスレッド、および返信や対応が行われていない review body / PRコメントの指摘）が 0 件の場合は、`termination_status = READY_TO_MERGE` と判定して **Phase 6.5** へ進みます。1件以上の未解決指摘がある場合は **Phase 3** へ進みます。

## Phase 3: 分類・採否判断（自律）

各未解決コメントを以下の基準で分類し、`accept` / `reject` を自律的に決定する:

| 分類 | 基準 |
|------|------|
| `blocking` | 実行時エラー・データ整合性の破壊・セキュリティリスク・破壊的変更・公開記録の不整合 |
| `non-blocking` | テスト追加・ログ改善・プライバシー・一貫性の改善など対応推奨だが必須ではないもの |
| `suggestion` | 設計・命名・構造・保守性の改善提案 |

**Reject 制約 — スコープ外・先送りはトラッキング Issue 必須。**
`out-of-scope`、`deferred`、`follow-up` を理由とする reject は、フォローアップ Issue にトレース可能になるまで完了とみなさない。

フォローアップ Issue が不要な reject 理由:
- `already-handled` — コミット / PR / Issue を引用する。
- `invalid-premise` — 誤解の内容を説明する。
- `wont-fix` — 明示的な不対応決定。「後で対応」と書いてはならない。

修正前に以下のテーブルを提示する:

```
| # | スレッド ID | 分類 | 採否 | 概要 | reject 理由 | フォローアップ Issue |
|---|------------|------|------|------|------------|---------------------|
```

`fix_type` を決定する:

| fix_type | 該当ケース |
|----------|-----------|
| `logic` | コードの動作またはテストのみの変更 |
| `spec_change` | 公開ドキュメント・API・ワークフロー・互換性記録のセマンティクス変更 |
| `trivial` | typo・フォーマット・文言のみの修正 |
| `none` | 修正なし（全 reject） |

## Phase 4: 修正＋コミット

1. `git status --short --branch` を実行する。
2. `accept` した項目のみ修正する。
3. 修正粒度: 1 スレッド = 1 論理変更単位（atomic）。
4. 全修正完了後にビルド・テストを再実行する。
5. Phase 4 完了後に**まとめて 1 コミット**する（Conventional Commits 形式）。
6. ユーザーが明示的に求めない限り force push しない。

**返信＋resolve・処理済み記録**:

### 1. インラインレビュースレッドへの返信と解決
**第一選択 (review-raven MCP)**: `{RAVEN}:reply_and_resolve_review_thread` を使用して、返信と解決（resolve）を順次実行します：
- `threadId`: Phase U2 で取得したスレッド ID（PRRT_xxx）
- `body`: 返信内容（修正内容の報告、または reject 時の理由）
- `resolve`: `true`（解決する場合）、`false`（解決しない場合）

※返信のみを行う場合は `{RAVEN}:reply_to_review_thread` を、解決のみを行う場合は `{RAVEN}:resolve_review_thread` を個別に使用してもよい。

**フォールバック (gh CLI)**: MCP ツールが使用できない場合は、以下を実行します。
- **返信**: `{GH}:add_reply_to_pull_request_comment` を使用します。
  - `owner`, `repo`, `pull_number`: Phase 0 で確定した値
  - `comment_id`: Phase U2 で取得したルートコメントの `databaseId`
  - `body`: 返信内容
- **解決 (resolve)**: GraphQL mutation で実行します。
```bash
gh api graphql -f query='
  mutation($threadId: ID!) {
    resolveReviewThread(input: {threadId: $threadId}) {
      thread { id isResolved }
    }
  }
' -f threadId=<PRRT_node_id>
```

Issue 作成・リンクが不可能な場合を除き常に resolve します。

### 2. レビュー本文・PRコメントへの返信と処理済み記録
レビュー本文やPRコメントは「解決（resolve）」ボタンがないため、返信コメントの投稿とコミットの適用に加え、アノテーションへの記録をもって「処理済み」として永続化します。
- **返信**: `{GH}:add_issue_comment`（または `gh pr comment`）を呼び出し、該当のコメントを引用しつつ、対応結果または reject の理由を返信します。
- **記録**: 新たに解決した非スレッドのコメント ID を、今回サイクルで蓄積した `handled_comments` リストに追加します。これらは Phase 7 のサマリや再レビュー依頼コメントのアノテーションに記録されます。

### Reject 返信ルール

#### 1. 既存 Issue のリンク
`Tracked by #xxx` または `Follow-up: #xxx` を含める。Issue が実際にその内容をカバーしていることを確認する。

#### 2. 新規フォローアップ Issue の作成
`{GH}:create_issue` で Issue を作成する。`Follow-up: #<番号>` を返信に含め、Phase 3 テーブルと Phase 7 サマリに番号を記録する。

#### 3. 明示的な `Won't fix`
`Won't fix` と具体的な理由を書く。「後で対応」「フォローアップ予定」という表現は禁止。

#### 4. Issue 作成・リンクが不可能な場合
スレッドを resolve しない。Phase 7 に `untracked — needs follow-up issue` として記録する。

## Phase U6: サイクル評価・再レビュー判断

**ステップ 1**: 未解決指摘を再取得（Phase U2 の手順を再実行）。
- インラインスレッドの未解決（`isResolved = false`）が 0 件であること。
- 抽出したすべての review body / PR コメントの actionable 指摘に対して、対応する返信・処理が完了していること。
- 未解決の指摘が 1 件以上残っている場合: 想定外。報告して `needs user decision` で停止する。

**ステップ 2**: `need_re_review` を判断（未解決 = 0 の場合のみ）:

| fix_type | blocking accept あり? | need_re_review |
|----------|----------------------|----------------|
| `none` | — | **no** |
| `trivial` | — | **no** |
| `logic` または `spec_change` | いずれの場合も | **yes** |
| いずれも | 1 件以上 | **yes** |

**ステップ 3**: ルーティング

- `need_re_review = no` → **Phase 6.5**（`termination_status = READY_TO_MERGE`）
- `need_re_review = yes` かつ `cycles_done ≥ max_cycles` → 終了分類して **Phase 6.5**
- `need_re_review = yes` かつ `cycles_done < max_cycles` → `@thread-owl` コメント投稿（下記フォーマット参照）→ **reviewed-side cycle 完了**

### 終了分類

| 分類 | 条件 | マージへの影響 |
|------|------|----------------|
| ✅ `READY_TO_MERGE` | 未解決 = 0、再レビュー不要 | 安全 — 通常のマージゲート |
| 🟡 `ESCALATE — Clean` | 最大サイクル超過 かつ 最終サイクルの accept に `blocking` なし | おそらく安全 — 未検証の旨を注記 |
| 🔴 `ESCALATE — Unverified Fix` | 最大サイクル超過 かつ 最終サイクルで `blocking` fix を 1 件以上 accept したが再レビューなし | 危険 — マージ前に人間レビュー推奨 |

Phase 7 用に記録する: `termination_status`、`final_cycle_fix_types`、`unverified_blocking_commits`。

### 再レビュー依頼コメントフォーマット

`{GH}:add_issue_comment` で以下を投稿する:

```markdown
@thread-owl re-review requested

修正対応が完了しました。再レビューをお願いします。

<!-- review-raven: cycles_done=N, handled_comments=ID1,ID2,... -->
```

`N` には現在の `cycles_done` の値、`handled_comments` にはこれまでに処理を完了した（本サイクルで処理したものを含む）すべての非スレッドコメント ID のリストをカンマ区切りで記入します。これにより、次回のサイクル開始時（Phase 0）に正しく処理済み状態が復元され、重複対応を防ぎます。

**reviewed-side cycle はここで完了する。Phase U2 には戻らない。**
次の reviewer-side cycle は thread-owl の `issue_comment.created` webhook → queue → mcp-resource-subscriber 通知によって起動される。

---

## Phase 6.5: CI 確認

1. `gh pr checks <PR番号>` を実行する。
2. 全ジョブ SUCCESS → Phase 6.6 へ。
3. 失敗ジョブあり: `gh run view <run-id> --log-failed` でログを確認する。
   - 修正可能 → Phase 4 へ戻る。
   - 修正困難 → ユーザーに報告して停止。

`gh` が利用不可な場合は `{GH}` / GitHub MCP server で確認する。どちらでも確認できない場合は `CI: unknown` を報告して停止する。

## Phase 6.6: カバレッジ確認

Codecov 等のカバレッジ PR コメントを確認する（存在しない場合はスキップ → Phase 7 へ）。

- テストで解消できるカバレッジのギャップがある場合: Phase 4 へ戻る（`fix_type = logic`）。
- 問題がない場合: Phase 7 へ進む。

## Phase 7: サマリコメント投稿

`{GH}:add_issue_comment` で以下を PR に投稿する:

```markdown
## レビュー対応サマリ（thread-owl）

### 修正内容
- （概要を箇条書き）

### 採否判断
- accept: N 件
- reject: M 件
  - Thread <threadId> (PRRT_xxx): （理由）

### 先送り・スコープ外項目
- なし | <リスト: Thread <threadId> — Follow-up: #N>

### 検証
- CI: ...
- 未解決指摘数: 0
- サイクルステータス: <termination_status>
  - `ESCALATE — Unverified Fix` の場合: 理由・未検証コミット SHA・「マージ前に人間レビュー推奨」を明記
- 最終サイクル修正タイプ: blocking × N, non-blocking × N, suggestion × N, trivial × N
- cycles_done: N
- 再レビュー: @thread-owl コメント投稿済み | 不要 | ESCALATE（最大サイクル超過）

<!-- review-raven: cycles_done=N, handled_comments=ID1,ID2,... -->
```

**`先送り・スコープ外項目` ルール**: `out-of-scope` / `deferred` / `follow-up` を理由とする全 reject をフォローアップ Issue 番号付きでリストしなければならない。「なし」は該当 reject が 0 件かつ Phase U5 ステップ 4 で未解決スレッドがない場合のみ許容。

## Phase 8: マージ判断

**自律的にマージしない。** ユーザーからの明示的な指示を待つ。

マージ条件（ユーザー指示時に満たすこと）:
- CI 全ジョブ SUCCESS
- 未解決の review 指摘 = 0 件
- 全スレッドに返信済み
- 未解決の `blocking` 項目なし
- `termination_status` が `READY_TO_MERGE` または `ESCALATE — Clean`

`termination_status = ESCALATE — Unverified Fix` の場合:
1. CI グリーン・未解決 0 件でも **マージ準備完了とは報告しない**。
2. 未検証コミット SHA を付けて警告を明確に提示する。
3. ユーザーがそれでもマージを要求する場合は、未検証 blocking 修正を手動レビュー済みであることを明示的に確認してから進める。

`termination_status = WAITING_FOR_REVIEW(thread-owl)`（再レビューコメント投稿済み）の場合:
1. マージ準備完了とは報告しない。
2. 「thread-owl への再レビュー依頼済み。次の review cycle 待機中。」と報告する。

---

## 注意事項

- `max_cycles` デフォルトは 3。Phase 0 で必要に応じて調整する。
- `cycles_done` はサーバー状態ではなく `<!-- review-raven: cycles_done=N -->` PR コメントアノテーションから復元する。
- 再レビュー依頼は `@thread-owl` PR コメント経由。`request_copilot_review` は使用しない。
- このスキルは Copilot watch を開始せず、`get_pr_review_cycle_status` を呼ばない。
- 修正粒度: スレッド単位 atomic（1 スレッド = 1 論理変更単位）。
- コミット戦略: Phase 4 完了後まとめて 1 コミット（Conventional Commits 形式）。
- Phase 8 は明示指示待ち（操作安全基準）。

---

## ツール対応表

| ツール/コマンド | 役割 | 優先順位 |
|----------------|------|----------|
| `{RAVEN}:get_review_threads` | 全レビュースレッドの取得 | **第一選択** |
| `gh api graphql` (query) | 全レビュースレッドの取得 | **フォールバック** |
| `{RAVEN}:reply_and_resolve_review_thread` | スレッドへの返信と解決 | **第一選択** |
| `{GH}:add_reply_to_pull_request_comment` + `gh api graphql` (mutation) | スレッドへの返信と解決 | **フォールバック** |
| `{RAVEN}:reply_to_review_thread` | レビュースレッドに返信 | **第一選択** |
| `{RAVEN}:resolve_review_thread` | レビュースレッドを解決済みにする | **第一選択** |
| `{GH}:add_issue_comment` | PR サマリ・再レビュー依頼コメント投稿 | 共通 |
| `{GH}:create_issue` | フォローアップトラッキング Issue を作成 | 共通 |
| `gh pr checks` | CI確認 | 共通 |

---

## 関連スキル

- [`pr-review-cycle`](pr-review-cycle.ja.md) — Copilot review 専用。再レビューは `request_copilot_review` + async watch ループ（`@thread-owl` コメントは使用しない）。
