# Spike 調査結果: リクエストスコープの通常 tool call で REAUTH_REQUIRED が頻発する原因

[English](spike-request-scoped-reauth.md)

> 対象 Issue: https://github.com/scottlz0310/review-raven/issues/87
> 調査リポジトリ: `scottlz0310/review-raven`, `scottlz0310/mcp-gateway`, `scottlz0310/Mcp-Docker`
> 調査日: 2026-07-02

---

## 結論（先出し）

**根本原因は mcp-gateway builtin mode にある。** builtin mode の token 交換では GitHub provider アクセストークンが identity 解決後に破棄され、トークンストア（subject index）には gateway 発行の RS256 JWT しか残らない。そのため:

- `upstream_provider_token=true` ルートが呼ぶ `EnsureFreshAccessTokenForSubject` は「provider トークン」として **gateway JWT を返し**、それが review-raven に `Authorization: Bearer` として注入される
- review-raven はそのトークンをそのまま GitHub API に渡す → GitHub が HTTP 401 → `REAUTH_REQUIRED` に分類

つまり **GitHub 側の認証は一度も失効しておらず、そもそも GitHub トークンが review-raven に届いていない**。決定論的に毎回失敗するため「初回呼び出しで即座に発生」「以前から頻発」という観測と一致する。同時刻に `gh api graphql` が成功したのは、`gh` が自前のクレデンシャル（gateway と無関係）を使うためである。

**修正の帰属先: mcp-gateway**。review-raven 側の判定ロジック（`internal/github/classify.go`）は「GitHub が 401 を返した」という事実を正しく分類しており、コード修正は不要。

---

## 1. REAUTH_REQUIRED を返す判定ロジック（調査依頼 1）

`REAUTH_REQUIRED` の生成箇所は `internal/middleware/auth.go` ではなく `internal/github/classify.go` である。

| 場所 | トリガー条件 |
|---|---|
| `classify.go:60` | gateway sentinel `ErrGatewaySubjectGone`（Phase B whoami 経路のみ） |
| `classify.go:96` | REST API エラー `*github.ErrorResponse` で HTTP 401 |
| `classify.go:115` | GraphQL (shurcooL/githubv4) の plain error に `"401 Unauthorized"` を含む |

`get_review_threads` は GraphQL 経路（`client.go` の `GetReviewThreads` → `c.v4.Query`）なので、今回の観測は `classify.go:115`（または REST 系ツールなら `:96`）に該当する。**トリガーは常に「GitHub API 本体が 401 を返した」ことであり、review-raven が独自にトークンを検証して invalid と判定する経路は存在しない**（仮説 2 は棄却）。

`internal/middleware/auth.go` はヘッダーの有無しか見ない（存在すれば無検証で context に格納、欠落時のみ 401 JSON `missing_proxy_identity` / `missing_token` を返す）。この middleware の 401 は `AuthError` JSON 形式ではないため、観測されたエラーの発生源ではない。

## 2. 非 watch 経路の token の完全な流れ（調査依頼 2）

```
[クライアント (Claude Code)]
    │  Authorization: Bearer <gateway JWT>   ← builtin mode でクライアントに発行されるのは
    │                                           GitHub トークンではなく gateway 署名 RS256 JWT
    ▼
[mcp-gateway / ルート: ROUTE_REVIEW_RAVEN (upstream_provider_token=true)]
    │
    ├─ middleware.Auth: gateway JWT を検証 → subject (GitHub login) を context へ
    │
    ├─ NewProviderTokenMiddleware (proxy/handler.go:61)
    │     └─ EnsureFreshAccessTokenForSubject(subject)  (auth/handler.go:1439)
    │           └─ store.LatestBySubject(subject)
    │                 └─ subject index には gateway JWT しか無い ★根本原因
    │           └─ rotation メタデータ無し → lenient branch → JWT をそのまま返す
    │
    ├─ ReverseProxy.Rewrite (proxy/handler.go:210-214)
    │     └─ Authorization: Bearer <gateway JWT> を注入   ← GitHub トークンではない
    │     └─ X-Authenticated-User: <login> を注入
    ▼
[review-raven / internal/middleware/auth.go]
    │  ヘッダーを無検証で request context に格納
    ▼
[tools/auth_request.go: newGitHubClientProvider]
    │  tokenFromToolRequest → gateway JWT を取得
    │  ghclient.NewClient(ctx, <gateway JWT>, ...)   ← per-request の static token client
    ▼
[GitHub API (GraphQL / REST)]
    │  Bearer が GitHub トークンではない → HTTP 401
    ▼
[internal/github/classify.go:96/115]
    └─ autherr.NewReauthRequired() → {"error_type":"REAUTH_REQUIRED", ...}
```

watch 経路との違い: この経路にトークンスナップショットや `manager.ctx` は関与しない（Issue の想定通り）。ただし**注入されるトークン自体が最初から GitHub API に対して無効**なため、鮮度の問題以前に失敗する。

## 3. gateway が注入するトークンの生成・refresh タイミング（調査依頼 3）

### 3.1 builtin mode の token 交換で GitHub トークンが破棄される

`mcp-gateway/internal/auth/handler.go`:

- **auth-code flow** (`tokenAuthCode`, builtin 分岐): 「GitHub token was used only for identity resolution; it must not reach the client」として gateway JWT を生成し、`CacheToken(gatewayToken, subject, ...)` で **JWT のみ**をキャッシュ。GitHub トークンと refresh メタデータは**どこにも保存されない**（`persistProviderRefresh` の呼び出し自体がない）
- **device flow** (`tokenDeviceGrant`, builtin 時): `CacheToken(gatewayJWT, ...)` の後に `persistProviderRefresh(completed.AccessToken /* GitHub トークン */, ...)` を呼ぶが、`RecordProviderRefresh` (`session.go:713`) は「キャッシュ未登録のトークンをキーにした場合は意図的に no-op」と文書化されており、GitHub トークンは `CacheToken` されていないため**メタデータは黙って捨てられる**

### 3.2 EnsureFreshAccessTokenForSubject は JWT を「provider トークン」として返す

`EnsureFreshAccessTokenForSubject` (`auth/handler.go:1439`) は Phase B（#76, 2026-05-15）で実装された。**当時はクライアントトークン = GitHub トークンだったため `LatestBySubject` の結果をそのまま provider トークンとして返す前提が成立していた**。2026-06-17 の builtin JWT 化（#127）でこの前提が崩れたが、関数側は追従していない。subject index に JWT しか無いため:

1. `LatestBySubject` → gateway JWT を返す
2. JWT の `TokenRecord` に `ProviderRefreshToken` / `ProviderAccessExpiry` が無い → rotation 不適用
3. lenient branch で **JWT をそのまま `AccessToken` として返す**（エラーにならないため、proxy 側の warning ログも出ない）

なお `TokenRecord` には provider アクセストークンを保持するフィールド自体が存在しない（`Subject` / `Audiences` / `ExpiresAt` / `ProviderRefreshToken` / `ProviderAccessExpiry` / `RotationPermanentlyFailed` / `Nonce` のみ）。

### 3.3 タイムライン（ローカル docker ログ・git 履歴で裏取り済み）

| 日時 (UTC) | 事象 |
|---|---|
| 2026-05-15 | mcp-gateway #76: Phase B `EnsureFreshAccessTokenForSubject` 実装（クライアントトークン = GitHub トークン前提） |
| 2026-06-17 | mcp-gateway #127: builtin mode で gateway JWT 発行に変更。**前提が崩れた起点 =「以前から頻発」の起点** |
| 2026-06-27 | Mcp-Docker #192: review-raven ルートに gateway OAuth 適用。`upstream_provider_token` 未設定のため gateway クライアント JWT がそのまま素通しで注入され、全 tool call が REAUTH_REQUIRED に |
| 2026-06-30 13:19 | mcp-gateway #187: `upstream_provider_token` 実装（本問題の修正を意図） |
| 2026-06-30 22:18 | Mcp-Docker #198 適用でコンテナ再作成。gateway 起動ログで `upstream_provider_token: true` を確認。**しかし 3.1/3.2 の通り builtin mode では JWT が返るため効果なし** |
| 2026-07-01 01:23 | ユーザーが device flow で GitHub 再認証成功（gateway audit ログ） |
| 2026-07-01 01:24-25 | squirrel-notifier PR#112 検証セッションで `get_review_threads` が即 REAUTH_REQUIRED（**再認証直後でも失敗** = トークン鮮度の問題ではない決定的証拠）。gateway ログに provider token 系 warning なし = `EnsureFreshAccessTokenForSubject` は「成功」して JWT を返していた |

### 3.4 再現条件

**mcp-gateway が builtin mode で稼働し、review-raven ルートを経由する tool call を行うこと。** それだけで 100% 再現する（レアな edge case ではない）。`upstream_provider_token=true` の有無は結果を変えない（無し = クライアント JWT 素通し、有り = ストアから取り出した同じ JWT）。

## 4. 修正の帰属判断（調査依頼 4）

**mcp-gateway。** 修正の方向性:

1. builtin mode の token 交換（auth-code / device / refresh の全 grant）で GitHub provider アクセストークンと refresh メタデータを gateway JWT の `TokenRecord` に紐付けて保持する（例: `TokenRecord.ProviderAccessToken` フィールド追加 + SQLite ストアの migration）
2. `EnsureFreshAccessTokenForSubject` は record の provider アクセストークンを返し、rotation も provider トークンに対して行う
3. Phase B `/internal/v1/whoami`（watch 経路）も同関数を使うため、**同修正で watch 側の delegated access も正しく GitHub トークンを返すようになる**（現状は同じ欠陥の影響下にある）

review-raven 側:

- `classify.go` の分類は正しい（GitHub の 401 を REAUTH_REQUIRED にマップ）。修正不要
- 任意の改善候補（本 spike のスコープ外・未実施）: JWT 形状（`eyJ...` 3 セグメント）のトークンを検知して「gateway 設定不備」を示す明示的エラーを返せば、`GitHub authentication has expired` という誤解を招くメッセージを避けられる。root cause 修正後は発生しない経路のため必須ではない

## 5. 除外した仮説

| 仮説 | 判定 |
|---|---|
| review-raven の auth middleware が有効なヘッダーを invalid と判定 | **棄却**。middleware はヘッダー有無しか見ない。REAUTH_REQUIRED は GitHub 本体の 401 由来 |
| gateway 注入トークンが「注入時点で既に失効」 | **部分的に正しいが不正確**。失効した GitHub トークンではなく、**最初から GitHub トークンですらない**（gateway JWT） |
| watch トークンスナップショットの陳腐化（mcp-gateway#70） | 無関係（Issue の想定通り）。ただし whoami 経路も本欠陥の影響下にあり、#70 で扱った「ローテーション陳腐化」以前の問題が存在する |

## 6. Follow-up

- mcp-gateway に修正 Issue を起票済み: [mcp-gateway#188](https://github.com/scottlz0310/mcp-gateway/issues/188) — builtin mode で provider アクセストークンを保持し、`EnsureFreshAccessTokenForSubject` が GitHub トークンを返すようにする
- 修正がデプロイされるまで、review-raven ルート経由の GitHub 操作は `gh` CLI / `github` MCP サーバーへのフォールバックが引き続き必要
