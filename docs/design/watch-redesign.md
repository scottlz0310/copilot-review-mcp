# copilot-review-mcp 非同期 Watch 再設計

**作成日**: 2026-04-22  
**ステータス**: Draft  
**対象**: `services/copilot-review-mcp`

---

## 1. 背景

現行の `wait_for_copilot_review` は、GitHub API を定期ポーリングしながら
1 回の MCP tool call を長時間ブロックする構造になっている。

この方式には、実装面と運用面の両方で限界がある。

- MCP tool call が最長 30 分近く拘束される
- `Stateless: true` のため、tool call 完了後に server 側から後追い通知しにくい
- LLM から見ると「長時間待つだけのツール」に見え、利用が避けられやすい
- キャンセルや transport 切断で待機結果が失われやすい
- 途中経過や完了通知の扱いが弱い

理想の UX は次の通り。

1. LLM は最初に `get_copilot_review_status` で現在状態を確認する
2. 未完了なら「待機開始」を即時実行する
3. 以降は他の作業を進める
4. 完了時は通知を受ける
5. host が通知を十分扱えない場合でも cheap status read で追跡できる

---

## 2. 目標

### 2.1 主目標

- LLM が使いやすい非同期 wait モデルへ移行する
- blocking wait を主経路から外す
- 完了通知を MCP の標準的な仕組みで扱えるようにする
- 通知が弱い host でも状態確認で破綻しない設計にする
- PR を段階的に分けられる構造へ分解する

### 2.2 成功条件

- `start_*` 系ツールは即時 return する
- 同一 user / 同一 PR に対する `start_*` は active watch の間 idempotent に振る舞う
- watch 状態は server 内部で継続監視される
- watch ごとの状態を cheap read で取得できる
- 完了/失敗/状態遷移時に resource update 通知を送れる
- 現行 `wait_for_copilot_review` は互換のために残せても、LLM の主経路でなくなる

---

## 3. 非目標

- 初期段階での「サーバ再起動後も watch を完全復元する」こと
- webhook ベースへの全面移行
- すべての MCP host で同一の通知 UX を保証すること
- 既存 issue #55, #56, #57, #58 をこの redesign の中で同時に解決すること

> ただし本 redesign によって、`wait_for_copilot_review` まわりの一部課題は
> 実質的に吸収または優先度低下する可能性がある。

---

## 4. 現行構成の制約

### 4.1 Streamable HTTP が stateless

現行実装は `mcp.NewStreamableHTTPHandler(..., { Stateless: true })` を使っており、
リクエストごとに一時 session を生成して閉じている。

このため、tool call 完了後に長生きする session に紐づいた通知や subscription を
安定して扱えない。

### 4.2 リクエストごとに新しい `mcp.Server` を生成している

server/session/resource subscription の状態がリクエスト間で維持されないため、
MCP resource 更新通知と相性が悪い。

### 4.3 認証トークンが request context 依存

GitHub access token は middleware が request context に載せているだけであり、
バックグラウンド watch worker が request の外で GitHub API を叩く仕組みがない。

### 4.4 host 側の通知サポート差

MCP server が notification を送れても、host がそれを LLM 実行再開に結びつけるとは限らない。
したがって「通知のみ」を前提にせず、fallback status read が必要。

---

## 5. 方針

### 5.1 blocking wait から watch モデルへ移行する

`wait_for_copilot_review` を「その場で待つ」ツールから、
「バックグラウンド監視を開始し、その状態を別経路で追える」設計へ置き換える。

推奨ツール構成は以下。

- `get_copilot_review_status`
  現在の GitHub 上の即時 snapshot を返す
- `start_copilot_review_watch`
  watch を開始または既存 watch を再利用し、即時 return する
- `get_copilot_review_watch_status`
  ローカル watch state を返す cheap read。
  `watch_id` 指定だけでなく `(owner, repo, pr)` からの lookup も許容する
- `list_copilot_review_watches`
  active / recent watch の一覧を返し、LLM や人手が watch を見失っても追跡しやすくする
- `cancel_copilot_review_watch`
  任意の watch を停止する
- `wait_for_copilot_review`
  互換維持用の blocking fallback。主経路では非推奨にする

### 5.2 MCP resources を watch 状態の公開面として使う

watch ごとに resource URI を持たせる。

例:

```text
copilot-review://watch/{watch_id}
```

この resource は `resources/read` で watch 状態を返し、状態変化時には
`notifications/resources/updated` を送る。

### 5.3 通知と polling fallback を両立する

通知が扱える host では subscription を利用する。
通知が弱い host では `get_copilot_review_watch_status` を呼べば同じ状態に到達できるようにする。

### 5.4 watch 状態は GitHub API ではなくローカル state を優先参照する

watch status 読み出しのたびに GitHub API を叩くのではなく、
バックグラウンド worker が更新したローカル state を返す。

これにより、

- GitHub API 使用量を抑える
- LLM の再確認コストを下げる
- host 側の retry に強くする

### 5.5 active watch は 1 PR あたり 1 本に制約し、`start_*` は idempotent にする

`start_copilot_review_watch` の振る舞いは明示的に idempotent とする。

- 同一 `(github_login, owner, repo, pr)` に active watch がある場合:
  既存 watch を返す
- active watch が無い場合:
  新規 watch を作成する

これにより、LLM は「既に watch があるかもしれない」状況でも
安全に `start_*` を再実行できる。

---

## 6. 提案アーキテクチャ

### 6.1 段階導入: まず stateless-compatible watch、その後 stateful session

stateful session 化は最終形として妥当だが、現行構成との差分が大きく、
セッション管理・メモリ管理・複数 client の扱いを含めて実装コストが高い。

そのため導入順は次のようにする。

- 先行段階:
  現行 transport のままでも成立する watch manager + cheap status read を導入する
- 後続段階:
  resource subscription / update notification を成立させるために stateful session を導入する

つまり、async watch 自体の成立は stateful session の完了を待たずに進める。

### 6.2 watch manager

プロセス内 singleton として watch manager を持つ。

責務:

- watch の作成/再利用
- worker の起動/停止
- watch 状態の更新
- terminal 状態遷移の管理
- worker 健全性監視
- resource 更新通知の発火

watch の dedupe 単位は少なくとも以下を含む。

- GitHub login
- owner
- repo
- pr

### 6.3 active watch の一意性

同一 user / 同一 PR について active watch は常に 1 本だけとする。

この制約により、

- `start_*` を idempotent にできる
- GitHub polling の重複を避けられる
- LLM が watch ID を見失っても `(owner, repo, pr)` から回復しやすい

### 6.4 watch state

段階的に次の 2 層へ発展させる。

- 初期段階:
  実行中 worker 管理と status read をメモリで持つ
- 次段階:
  状態参照用スナップショットを SQLite に保存する

これにより、まずは小さく async watch を成立させ、
その後に再開性と可観測性を強化できる。

### 6.5 GitHub token の扱い

watch 開始時に request context から token を取得し、watch lifetime 中のみ worker 側で保持する。

初期段階では以下を許容する。

- token の永続化はしない
- サーバ再起動時に watch は失効してよい
- watch 完了/キャンセル時に token を破棄する

token が失効 / revoke / scope 不足になった場合は、
watch 自体を `FAILED` とし、`failure_reason=AUTH_EXPIRED` を記録する。

初期段階では自動回復は行わず、
ユーザーまたは LLM が再認証後に `start_*` を再実行する方式を採る。

---

## 7. 状態モデル

### 7.1 review status

既存と同様:

- `NOT_REQUESTED`
- `PENDING`
- `IN_PROGRESS`
- `COMPLETED`
- `BLOCKED`

### 7.2 watch lifecycle status

watch 自体の状態は review status と分ける。

- `WATCHING`
- `COMPLETED`
- `BLOCKED`
- `TIMEOUT`
- `RATE_LIMITED`
- `FAILED`
- `STALE`
- `CANCELLED`

`COMPLETED` / `BLOCKED` は terminal。
`TIMEOUT` / `FAILED` / `STALE` / `CANCELLED` も terminal。

`STALE` は次を意味する。

- worker が失われた
- サーバ再起動で watch 実行文脈が消えた
- token 非永続運用のため継続監視できない

つまり「GitHub 上の review はまだ終わっていないかもしれないが、
この watch はもう進行していない」状態である。

### 7.3 failure reason

`watch_status=FAILED` の場合は補助的に `failure_reason` を持つ。

初期候補:

- `AUTH_EXPIRED`
- `GITHUB_ERROR`
- `INTERNAL_ERROR`

### 7.4 返却例

```json
{
  "watch_id": "cw_01H...",
  "watch_status": "WATCHING",
  "review_status": "PENDING",
  "terminal": false,
  "resource_uri": "copilot-review://watch/cw_01H...",
  "recommended_next_action": "POLL_AFTER",
  "next_poll_seconds": 90
}
```

---

## 8. 期待フロー

### 8.1 推奨フロー

```text
1. get_copilot_review_status(owner, repo, pr)
2. 未完了なら start_copilot_review_watch(owner, repo, pr)
3. active watch が既にあればそれを再利用し、なければ新規作成する
4. 即時 return を受けて他の作業へ進む
5. host が通知を扱えれば resource updated を受信
6. 必要なら get_copilot_review_watch_status(watch_id) または `(owner, repo, pr)` lookup で再確認
7. terminal 状態になったら後続アクションへ進む
```

### 8.2 watch_id を見失った場合

```text
1. get_copilot_review_watch_status(owner, repo, pr)
2. active watch が存在すればそれを返す
3. 存在しなければ start_copilot_review_watch(owner, repo, pr) を再実行
```

### 8.3 host が通知を活かせない場合

```text
1. start_copilot_review_watch(...)
2. watch_id または `(owner, repo, pr)` を保持
3. 次の判断点で get_copilot_review_watch_status(...)
4. `recommended_next_action=POLL_AFTER` と `next_poll_seconds` を尊重して再確認する
```

### 8.4 デバッグ / 人手運用

```text
1. list_copilot_review_watches(...)
2. active watch / stale watch / failed watch を一覧で確認
3. 必要な watch だけ詳細 status を読む
```

---

## 9. データモデル案

SQLite に `review_watch` テーブルを追加する想定。

例:

```sql
CREATE TABLE review_watch (
    id                  TEXT PRIMARY KEY,
    github_login        TEXT NOT NULL,
    owner               TEXT NOT NULL,
    repo                TEXT NOT NULL,
    pr                  INTEGER NOT NULL,
    trigger_log_id      INTEGER,
    resource_uri        TEXT,
    watch_status        TEXT NOT NULL,
    review_status       TEXT NOT NULL,
    failure_reason      TEXT,
    is_active           INTEGER NOT NULL DEFAULT 1,
    started_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    completed_at        INTEGER,
    stale_at            INTEGER,
    last_error          TEXT,
    rate_limit_reset_at INTEGER
);

CREATE UNIQUE INDEX idx_review_watch_active_per_pr
ON review_watch(github_login, owner, repo, pr)
WHERE is_active = 1;
```

`is_active=1` は「この watch が現在の主 watch」であることを意味する。
terminal 状態または `STALE` へ遷移した時点で `is_active=0` に落とす。

> `resource_uri` は notification 導入前段階では NULL を許容してよい。

---

## 10. PR 分割方針

より小さく再開しやすい単位に分割する。

### Phase 1a: memory-only watch manager

- 現行 transport のまま background watch manager を導入
- active watch の一意性と `start_*` の idempotency を先に成立させる
- token 失効時の `FAILED/AUTH_EXPIRED` を定義する

### Phase 1b: SQLite persistence

- `review_watch` テーブル追加
- watch state を SQLite に保存
- 再開性と可観測性を強化

### Phase 2: watch tool surface

- `start_* / get_* / cancel_* / list_*` を追加
- `watch_id` lookup と `(owner, repo, pr)` lookup を定義
- `recommended_next_action` / `next_poll_seconds` を返す

### Phase 3: stateful session foundation

- stateless 廃止
- server lifecycle 見直し
- stateful session の安全な導入

### Phase 4: resource 公開と通知

- watch resource 導入
- `resources/read`
- `resources/subscribe`
- `ResourceUpdated(...)`

### Phase 5: legacy wait 整理

- `wait_for_copilot_review` を fallback/legacy 化
- README と運用フロー更新
- 旧経路の説明整理

---

## 11. リスク

### 11.1 host 差異

resource subscription を host がどこまで UX として扱うかは一定でない。
よって fallback cheap read を必須にする。

### 11.2 active watch の衝突

一意性制約と idempotent start を曖昧にすると、
同一 PR への重複 polling や watch の乱立が起きる。

### 11.3 stateful session 化の影響

現行の request-scoped server/client 構造と異なり、
server/session の長寿命化に伴う状態管理の複雑性が増える。
そのため段階導入し、watch 自体は先に stateless-compatible に成立させる。

### 11.4 token 取り扱い

バックグラウンド worker が token を保持するため、
保持期間・破棄タイミング・ログ出力回避を厳格に扱う必要がある。
また token 失効時に `AUTH_EXPIRED` を返せないと原因不明の失敗に見える。

### 11.5 partial migration 期間

新旧ツールがしばらく共存するため、LLM への推奨経路を
description と README で明示しないと旧 blocking path に流れやすい。

---

## 12. 実装順の推奨

1. memory-only watch manager と idempotent start を導入する
2. watch state の SQLite 保存を入れる
3. 新しい watch 系ツールを公開する
4. stateful transport と server lifecycle を固める
5. resource 読み出しと更新通知を載せる
6. `wait_for_copilot_review` を互換 fallback に下げる

---

## 13. 補足

この redesign は「長時間ブロックする wait ツールを改善する」というより、
`copilot-review-mcp` を LLM 向けの async watch サービスへ再定義する変更である。

したがって、局所修正ではなく、

- transport
- session
- auth/token lifetime
- local state
- tool UX
- docs

を一体で扱う前提で issue と PR を分解する。

---

## 14. Issue 分解

- Epic: [#63](https://github.com/scottlz0310/Mcp-Docker/issues/63) `epic(copilot-review-mcp): async watch + notification ベースへ再設計し blocking wait を主経路から外す`
- Phase 1a / memory-only watch: [#68](https://github.com/scottlz0310/Mcp-Docker/issues/68) `feat(copilot-review-mcp): memory-only watch manager を先行導入し active watch を idempotent に扱う`
- Phase 1b / persistence: [#65](https://github.com/scottlz0310/Mcp-Docker/issues/65) `feat(copilot-review-mcp): SQLite 永続化で review_watch state を追加する`
- Phase 2 / tool UX + migration: [#67](https://github.com/scottlz0310/Mcp-Docker/issues/67) `feat(copilot-review-mcp): watch 系ツールを追加し \`wait_for_copilot_review\` を legacy 化する`
- Phase 3 / stateful foundation: [#64](https://github.com/scottlz0310/Mcp-Docker/issues/64) `refactor(copilot-review-mcp): Streamable HTTP を stateful session 化し async notification の基盤を作る`
- Phase 4 / resources: [#66](https://github.com/scottlz0310/Mcp-Docker/issues/66) `feat(copilot-review-mcp): watch resource と resources/updated 通知を追加する`
