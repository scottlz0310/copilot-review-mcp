# Spike Investigation: Frequent REAUTH_REQUIRED on Request-Scoped (Non-Watch) Tool Calls

[日本語](spike-request-scoped-reauth.ja.md)

> Target Issue: https://github.com/scottlz0310/review-raven/issues/87
> Investigated repositories: `scottlz0310/review-raven`, `scottlz0310/mcp-gateway`, `scottlz0310/Mcp-Docker`
> Investigation date: 2026-07-02

---

## Conclusion (up front)

**The root cause lies in mcp-gateway builtin mode.** In builtin mode's token exchange, the GitHub provider access token is discarded after identity resolution, and only the gateway-issued RS256 JWT remains in the token store (subject index). As a result:

- `EnsureFreshAccessTokenForSubject`, called by `upstream_provider_token=true` routes, returns the **gateway JWT as the "provider token"**, and it is injected into review-raven as `Authorization: Bearer`
- review-raven passes that token straight to the GitHub API → GitHub returns HTTP 401 → classified as `REAUTH_REQUIRED`

In other words, **GitHub-side authentication never expired; a GitHub token never reaches review-raven in the first place**. The failure is deterministic on every call, matching the observations "fails immediately on the first call" and "has been frequent for a while". `gh api graphql` succeeded at the same time because `gh` uses its own credentials, unrelated to the gateway.

**Fix attribution: mcp-gateway.** review-raven's classification logic (`internal/github/classify.go`) correctly classifies the fact that "GitHub returned 401"; no review-raven code change is required.

---

## 1. Where REAUTH_REQUIRED is produced (investigation request 1)

`REAUTH_REQUIRED` is generated in `internal/github/classify.go`, not in `internal/middleware/auth.go`.

| Location | Trigger condition |
|---|---|
| `classify.go:60` | Gateway sentinel `ErrGatewaySubjectGone` (Phase B whoami path only) |
| `classify.go:96` | REST API error `*github.ErrorResponse` with HTTP 401 |
| `classify.go:115` | GraphQL (shurcooL/githubv4) plain error containing `"401 Unauthorized"` |

`get_review_threads` goes through the GraphQL path (`GetReviewThreads` in `client.go` → `c.v4.Query`), so the observed error corresponds to `classify.go:115` (or `:96` for REST-based tools). **The trigger is always "the GitHub API itself returned 401"; there is no code path where review-raven validates the token on its own and judges it invalid** (hypothesis 2 rejected).

`internal/middleware/auth.go` only checks header presence (headers are stored into the context without validation; only when missing does it return the 401 JSON `missing_proxy_identity` / `missing_token`). That middleware 401 is not in the `AuthError` JSON format, so it is not the source of the observed error.

## 2. Complete token flow on the non-watch path (investigation request 2)

```
[Client (Claude Code)]
    │  Authorization: Bearer <gateway JWT>   ← in builtin mode the client receives a
    │                                           gateway-signed RS256 JWT, not a GitHub token
    ▼
[mcp-gateway / route: ROUTE_REVIEW_RAVEN (upstream_provider_token=true)]
    │
    ├─ middleware.Auth: verifies the gateway JWT → subject (GitHub login) into context
    │
    ├─ NewProviderTokenMiddleware (proxy/handler.go:61)
    │     └─ EnsureFreshAccessTokenForSubject(subject)  (auth/handler.go:1439)
    │           └─ store.LatestBySubject(subject)
    │                 └─ subject index contains only the gateway JWT ★root cause
    │           └─ no rotation metadata → lenient branch → returns the JWT as-is
    │
    ├─ ReverseProxy.Rewrite (proxy/handler.go:210-214)
    │     └─ injects Authorization: Bearer <gateway JWT>   ← not a GitHub token
    │     └─ injects X-Authenticated-User: <login>
    ▼
[review-raven / internal/middleware/auth.go]
    │  stores headers into the request context without validation
    ▼
[tools/auth_request.go: newGitHubClientProvider]
    │  tokenFromToolRequest → obtains the gateway JWT
    │  ghclient.NewClient(ctx, <gateway JWT>, ...)   ← per-request static-token client
    ▼
[GitHub API (GraphQL / REST)]
    │  Bearer is not a GitHub token → HTTP 401
    ▼
[internal/github/classify.go:96/115]
    └─ autherr.NewReauthRequired() → {"error_type":"REAUTH_REQUIRED", ...}
```

Difference from the watch path: no token snapshot or `manager.ctx` is involved in this path (as the Issue assumed). However, **the injected token itself is invalid against the GitHub API from the start**, so the failure occurs before token freshness even matters.

## 3. Generation and refresh timing of the token the gateway injects (investigation request 3)

### 3.1 Builtin-mode token exchange discards the GitHub token

`mcp-gateway/internal/auth/handler.go`:

- **auth-code flow** (`tokenAuthCode`, builtin branch): per the comment "GitHub token was used only for identity resolution; it must not reach the client", a gateway JWT is generated and **only the JWT** is cached via `CacheToken(gatewayToken, subject, ...)`. The GitHub token and its refresh metadata are **stored nowhere** (there is no `persistProviderRefresh` call at all)
- **device flow** (`tokenDeviceGrant`, builtin): after `CacheToken(gatewayJWT, ...)`, it calls `persistProviderRefresh(completed.AccessToken /* GitHub token */, ...)`, but `RecordProviderRefresh` (`session.go:713`) is documented as an intentional no-op when keyed by a token that was never cached — the GitHub token was not `CacheToken`'d, so **the metadata is silently dropped**

### 3.2 EnsureFreshAccessTokenForSubject returns the JWT as the "provider token"

`EnsureFreshAccessTokenForSubject` (`auth/handler.go:1439`) was implemented in Phase B (#76, 2026-05-15). **At that time the client token WAS the GitHub token, so returning the result of `LatestBySubject` as the provider token was a valid assumption.** The builtin JWT migration (#127, 2026-06-17) broke that assumption, but the function was never updated. Since the subject index contains only JWTs:

1. `LatestBySubject` → returns the gateway JWT
2. The JWT's `TokenRecord` has no `ProviderRefreshToken` / `ProviderAccessExpiry` → rotation is not applicable
3. The lenient branch **returns the JWT as-is as `AccessToken`** (no error is raised, so no warning appears in the proxy logs either)

Note that `TokenRecord` has no field to hold a provider access token at all (only `Subject` / `Audiences` / `ExpiresAt` / `ProviderRefreshToken` / `ProviderAccessExpiry` / `RotationPermanentlyFailed` / `Nonce`).

### 3.3 Timeline (corroborated with local docker logs and git history)

| Date/time (UTC) | Event |
|---|---|
| 2026-05-15 | mcp-gateway #76: Phase B `EnsureFreshAccessTokenForSubject` implemented (assumes client token = GitHub token) |
| 2026-06-17 | mcp-gateway #127: builtin mode switches to issuing gateway JWTs. **The point where the assumption broke = origin of "frequent for a while"** |
| 2026-06-27 | Mcp-Docker #192: gateway OAuth applied to the review-raven route. With `upstream_provider_token` unset, the gateway client JWT is forwarded as-is, and every tool call becomes REAUTH_REQUIRED |
| 2026-06-30 13:19 | mcp-gateway #187: `upstream_provider_token` implemented (intended to fix this problem) |
| 2026-06-30 22:18 | Container recreated with Mcp-Docker #198. Gateway startup log confirms `upstream_provider_token: true`. **However, per 3.1/3.2 the builtin mode still returns the JWT, so it has no effect** |
| 2026-07-01 01:23 | User completes GitHub re-authentication via device flow (gateway audit log) |
| 2026-07-01 01:24-25 | In the squirrel-notifier PR#112 verification session, `get_review_threads` fails immediately with REAUTH_REQUIRED (**failing right after re-authentication = decisive evidence this is not a token-freshness problem**). No provider-token warnings in the gateway log = `EnsureFreshAccessTokenForSubject` "succeeded" and returned the JWT |

### 3.4 Reproduction conditions

**Run mcp-gateway in builtin mode and make any tool call through the review-raven route.** That alone reproduces the failure 100% of the time (it is not a rare edge case). Whether `upstream_provider_token=true` is set does not change the outcome (unset = the client JWT is forwarded as-is; set = the same JWT is fetched from the store).

## 4. Fix attribution (investigation request 4)

**mcp-gateway.** Direction of the fix:

1. In builtin mode's token exchange (all grants: auth-code / device / refresh), retain the GitHub provider access token and refresh metadata tied to the gateway JWT's `TokenRecord` (e.g., add a `TokenRecord.ProviderAccessToken` field + SQLite store migration)
2. `EnsureFreshAccessTokenForSubject` returns the record's provider access token, and rotation also operates on the provider token
3. Phase B `/internal/v1/whoami` (watch path) uses the same function, so **the same fix makes the watch-side delegated access return proper GitHub tokens as well** (it is currently affected by the same defect)

On the review-raven side:

- The classification in `classify.go` is correct (maps GitHub's 401 to REAUTH_REQUIRED). No fix needed
- Optional improvement candidate (out of scope for this spike, not implemented): detecting JWT-shaped tokens (`eyJ...`, 3 segments) and returning an explicit error indicating "gateway misconfiguration" would avoid the misleading `GitHub authentication has expired` message. Not essential, since this path disappears once the root cause is fixed

## 5. Rejected hypotheses

| Hypothesis | Verdict |
|---|---|
| review-raven's auth middleware misjudges valid headers as invalid | **Rejected.** The middleware only checks header presence. REAUTH_REQUIRED comes from GitHub's own 401 |
| The token the gateway injects is "already expired at injection time" | **Partially right but imprecise.** It is not an expired GitHub token — it is **not a GitHub token at all** (a gateway JWT) |
| Staleness of the watch token snapshot (mcp-gateway#70) | Unrelated (as the Issue assumed). However, the whoami path is also affected by this defect, which precedes the "rotation staleness" problem handled in #70 |

## 6. Follow-up

- Fix issue filed against mcp-gateway: [mcp-gateway#188](https://github.com/scottlz0310/mcp-gateway/issues/188) — retain the provider access token in builtin mode so `EnsureFreshAccessTokenForSubject` returns the GitHub token
- Until the fix is deployed, GitHub operations via the review-raven route still require falling back to the `gh` CLI / github (MCP)
