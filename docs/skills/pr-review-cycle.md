---
name: pr-review-cycle
description: Copilot レビュー完了を async watch で待機してから PR レビュー対応サイクル（スレッド取得→分類・採否判断→修正→返信→サイクル評価→サマリ投稿）を自律実行する。PR 作成直後・Copilot レビュー依頼直後に呼び出す。マージは自律実行しない。
---

# pr-review-cycle スキル

copilot-review-mcp の watch ツール群を使い、Copilot レビュー完了を **async watch** で待機してから
PR レビュー対応サイクルを自律実行するスキル。

> **このファイルについて**  
> `docs/skills/pr-review-cycle.md` はリポジトリ共有用テンプレートです。  
> 個人の AI エージェント設定（`~/.claude/skills/` 等）にコピーしてご利用ください。  
> MCP サーバーキーはお使いの環境に合わせて読み替えてください。

---

## セットアップ

### 必要な MCP サーバー

| サーバー | 役割 | 参照 |
|---------|------|------|
| `copilot-review-mcp` | Copilot レビュー watch・スレッド操作 | [services/copilot-review-mcp](../../services/copilot-review-mcp/) |
| GitHub MCP サーバー | Issue/PR コメント投稿 | [README.md](../../README.md) |

### プレースホルダーの読み替え

本スキルでは以下のプレースホルダーを使用する:

| プレースホルダー | 役割 | VS Code での例 |
|----------------|------|---------------|
| `{CRM}` | copilot-review-mcp ツール | `mcp_copilot-review-mcp_*` |
| `{GH}` | GitHub MCP ツール | `mcp_github-mcp-server-docker_*` |

> IDE ごとのツール名プレフィックスは `make gen-config-crm` で生成した設定ファイルで確認できます。

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

1. `{CRM}:get_copilot_review_status` で GitHub 上の現状を即時確認する。
2. `status = COMPLETED` または `BLOCKED`: → Phase 2 へ（watch 不要）。
3. `status = NOT_REQUESTED`: `{CRM}:request_copilot_review` でレビュー依頼後 → Phase 1 へ。
4. `status = PENDING` / `IN_PROGRESS`: → Phase 1 へ。

## Phase 1: async watch 開始＋完了待機

**待機開始時刻を記録する。** Phase 6 からループで戻ってきた場合も新たに記録し直す（タイムアウトは Phase 1 進入ごとにリセット）。

### 1-A: watch 開始

1. `{CRM}:start_copilot_review_watch` を呼ぶ（即時 return）。
   - 同一 PR の active watch があれば再利用される。
   - レスポンスの `resource_uri`（`copilot-review://watch/{watch_id}`）を記録する。

### 1-B: 完了待機

**通知サポートあり（推奨）:**
- `resource_uri` を MCP resource として subscribe し、`notifications/resources/updated` 通知を待つ。
- 通知受信後: `{CRM}:get_copilot_review_watch_status` で最終ステータスを確認。

**ポーリング fallback:**
- レスポンスの `next_poll_seconds` 秒後に `{CRM}:get_copilot_review_watch_status` を呼ぶ（最小 1 秒、サーバー既定は 90 秒）。
- `recommended_next_action` に従う:

| recommended_next_action | 対応 |
|------------------------|------|
| `READ_REVIEW_THREADS` | → Phase 2 へ |
| `POLL_AFTER` | 指定秒数後に再ポーリング |
| `CHECK_FAILURE` | エラー内容をユーザーに報告してサイクル中断 |
| `REAUTH_AND_START_NEW_WATCH` | ユーザーに再認証を促して終了 |
| `START_NEW_WATCH` | watch を再開始（1-A へ） |

### 1-C: タイムアウト処理（経過時間 ≥ 15 分）

1. `{CRM}:cancel_copilot_review_watch` で watch をキャンセル。
2. `{GH}:add_issue_comment` で以下を投稿:  
   `Copilot レビュー完了待機がタイムアウトしました（15 分）。手動で再開してください。`
3. ユーザーに手動再開方法を案内して終了。

## Phase 2: スレッド取得

- `{CRM}:get_review_threads` で全コメントスレッドを取得。
- スレッドが 0 件: Copilot が「問題なし」と判断した可能性 → Phase 7 へ。

## Phase 3: 分類・採否判断（自律）

各コメントを以下の基準で分類し、`accept` / `reject` を自律的に決定する:

| 分類 | 基準 |
|------|------|
| `blocking` | 実行時エラー・データ整合性の破壊・セキュリティリスク・破壊的変更 |
| `non-blocking` | テスト追加・ログ改善など対応推奨だが必須ではないもの |
| `suggestion` | 設計・命名・抽象化の改善提案 |

`reject` の場合は理由を明記（スコープ外・別 PR で対応予定・設計方針との相違等）。

判定結果を以下のテーブルでユーザーに提示してから Phase 4 へ進む（承認待ちはしない）:

```
| # | スレッド ID | 分類 | 採否 | 概要 | 理由（reject のみ） |
|---|------------|------|------|------|---------------------|
```

また、今回の修正がどの `fix_type` に該当するかを決定する（Phase 6 で使用）:

| fix_type | 該当ケース |
|----------|-----------|
| `logic` | 実装ロジック・テストのみの変更 |
| `spec_change` | 仕様・API・インターフェースの変更 |
| `trivial` | typo・コメント・フォーマットのみ |
| `none` | 修正なし（全 reject） |

## Phase 4: 修正＋コミット

1. `accept` した項目のみ修正する。
2. **修正粒度**: 1 スレッド = 1 論理変更単位（atomic）。別スレッドへの波及がある場合は採否テーブルを再評価してから進める。
3. 全修正完了後にビルド・テストを再実行。失敗したら修正してリトライ。解消不能な場合はサイクル中断してユーザーに報告。
4. Phase 4 完了後に**まとめて 1 コミット**する（Conventional Commits 形式）。

## Phase 5: スレッド返信＋resolve

**全スレッドに必ず返信し、かつ必ず resolve する**（漏れはマージブロックの原因）:

| 状況 | ツール |
|------|--------|
| 修正済みスレッド | `{CRM}:reply_and_resolve_review_thread` |
| reject / 対応不要 | `{CRM}:reply_and_resolve_review_thread` |

reject の返信文には必ず理由を明記すること（スコープ外・別 Issue/PR で追跡・設計方針との相違 等）。

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

> `pr` は PR 番号（数値）。`max_cycles: 0` でサーバー側デフォルト（環境変数 `MAX_REVIEW_CYCLES`、未設定時 3）を使用する。  
> `cycles_done` は 0 始まりの整数（数値型）。Phase 1 進入ごとにインクリメントする（初回呼び出し: `0`、2 サイクル目: `1`、…）。

`recommended_action` に従う:

| recommended_action | 次のアクション |
|-------------------|---------------|
| `WAIT` | Phase 1 へ戻る（`cycles_done` をインクリメント） |
| `REPLY_RESOLVE` | Phase 2 へ戻る（未解決スレッドが残っている） |
| `REQUEST_REREVIEW` | `{CRM}:request_copilot_review` → Phase 1 へ戻る（`cycles_done` インクリメント） |
| `READY_TO_MERGE` | Phase 6.5 へ |
| `ESCALATE` | 現状をユーザーに報告してサイクル終了（人間判断に委ねる） |

## Phase 6.5: CI 確認

1. `gh pr checks <PR番号>` で CI 状態を確認。
2. 全ジョブ SUCCESS → Phase 6.6 へ。
3. 失敗ジョブあり: `gh run view <run-id> --log-failed` でログを取得し原因分析。
   - **修正可能**（コード不備・明確な失敗原因）: 採否テーブルに追加して Phase 4 へ戻る。
   - **修正困難**（環境要因・flaky test・原因不明）: ユーザーに報告して指示を仰ぐ。

## Phase 6.6: カバレッジ確認

1. Codecov 等のカバレッジ PR コメントを確認（存在しない場合はスキップ → Phase 7 へ）。
2. パッチカバレッジ・全体カバレッジの変動・未カバー新規ロジックを評価。
3. 判定:
   - **テスト追加で解消できる**: Phase 4 へ戻る（`fix_type = logic`）。
   - **設計変更が必要 / モック困難**: ユーザーに報告して判断待ち。
   - **問題なし**: Phase 7 へ。

## Phase 7: サマリコメント投稿

`{GH}:add_issue_comment` で以下を PR に投稿する:

```markdown
## レビュー対応サマリ

### 修正内容
- （概要を箇条書き）

### 採否判断
- accept: N 件
- reject: M 件
  - #<thread>: （理由）

### 未対応項目
- （あれば理由付きで）
```

## Phase 8: マージ判断

**自律的にマージしない。** ユーザーからの明示的な指示を待つ。

マージ条件（ユーザー指示時に満たすこと）:
- CI 全ジョブ SUCCESS
- `blocking` コメント 0 件
- 未解決レビュースレッド 0 件
- 全コメントに返信済み

条件を満たさない場合は欠けている項目をユーザーに報告して指示を仰ぐ。

---

## 注意事項

- ポーリング間隔: レスポンスの `next_poll_seconds` に従う（最小 1 秒、サーバー既定 90 秒）
- タイムアウト: Phase 1 進入ごとに 15 分リセット
- 再レビュー上限: サーバー側 `MAX_REVIEW_CYCLES`（デフォルト 3）。`ESCALATE` 以降は人間判断
- 修正粒度: スレッド単位 atomic（1 スレッド = 1 論理変更単位）
- コミット戦略: Phase 4 完了後まとめて 1 コミット（Conventional Commits 形式）
- Phase 3 の採否判断は自律だが結果テーブルは必ず提示（監査性のため）
- Phase 4 でテスト失敗が解消できない場合はサイクル中断
- Phase 8 は明示指示待ち（操作安全基準）

---

## ツール対応表

| ツール名 | 用途 |
|---------|------|
| `{CRM}:get_copilot_review_status` | GitHub 上の Copilot レビュー状態を即時確認 |
| `{CRM}:request_copilot_review` | Copilot レビューを依頼 |
| `{CRM}:start_copilot_review_watch` | async watch 開始（推奨経路） |
| `{CRM}:get_copilot_review_watch_status` | watch の現在状態をポーリング |
| `{CRM}:cancel_copilot_review_watch` | watch をキャンセル |
| `{CRM}:get_review_threads` | レビュースレッドを一覧取得 |
| `{CRM}:reply_and_resolve_review_thread` | スレッドに返信して resolve |
| `{CRM}:get_pr_review_cycle_status` | サイクル評価・次アクション判定 |
| `{GH}:add_issue_comment` | PR にコメント投稿 |
| `{CRM}:wait_for_copilot_review` | ※ legacy。blocking wait。代わりに watch を使うこと |

---

## MCP Resource サブスクリプション（参考）

`start_copilot_review_watch` のレスポンスに含まれる `resource_uri` を購読すると、
watch ステータス変化時に `resources/updated` 通知を受け取れます。

```
URI: copilot-review://watch/{watch_id}
MIME: application/json
```

通知受信後は `{CRM}:get_copilot_review_watch_status` で最新状態を読み取ってください。
