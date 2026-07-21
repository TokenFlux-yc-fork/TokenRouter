# Qoder CN Native Upstream

TokenRouter treats the `qoder` platform as Qoder CN only and follows the `qoderclicn` COSY gateway protocol. The platform key remains `qoder`; there is no separate CN platform key or international-site mode.

## Account Types

- New `cosy` accounts use `credentials.site=cn` and can use a CN PAT bootstrap or CN device OAuth credentials.
- CN device OAuth credentials use `credentials.refresh_mode=qodercn20`. PAT-backed sessions retain the PAT so TokenRouter can rebuild the COSY session.
- Manual import accepts either a CN `pat`, or an existing CN COSY token set.
- Existing COSY token credentials include `security_oauth_token`, `refresh_token`, `machine_id`, `machine_token`, `machine_type`, `uid` or `aid`, and optional organization metadata.
- QoderCN20 refreshes through the CN OpenAPI `/api/v1/deviceToken/refresh` endpoint and requires both the rotated access token and refresh token. Legacy `refresh_mode=cosy` credentials are rejected. PAT sessions are rebuilt from the original CN PAT through `/api/v1/jobToken/exchange` and userinfo.
- `machine_token` is normalized to the same stable value as `machine_id`, and `machine_type` is normalized to `5`.
- Accounts with `credentials.site=global` or no `site` are preserved as legacy records but are not schedulable and cannot query models, quota, or inference. Metadata edits preserve that state. Changing only `site` is rejected; migration requires newly submitted CN OAuth or PAT credentials.
- The integration covers standard CN PAT and QoderCN20 login. Enterprise dedicated-domain tokens, organization selection, AK/SK, and region discovery are not supported.

## Model Aliases and Mapping

The Qoder CN public aliases are:

- `auto`
- `qwen3.8-max-preview`
- `qwen3.7-max`
- `qwen3.7-plus`
- `qwen3.6-flash`
- `deepseek-v4-pro`
- `deepseek-v4-flash`
- `glm-5.2`
- `kimi-k2.7-code`
- `minimax-m2.7`

The aliases map to the route keys `auto`, `qmodel_preview`, `qmodel_latest`, `qmodel`, `q36fmodel`, `dmodel`, `dfmodel`, `gm51model`, `kmodel`, and `mmodel`, respectively. Legacy international-only aliases and route keys are rejected for CN accounts. Unknown raw route keys continue to pass through for operational compatibility.

Qoder account `model_mapping` follows the same rewrite-rule semantics as other platforms:

- key: model name accepted at that routing layer;
- value: final Qoder route/upstream model name;
- the mapping itself does not restrict the request model space.

Use `model_whitelist` when an account must be limited to specific final route/upstream models. The gateway applies mapping first and then checks the whitelist. If no whitelist is configured, the account remains unrestricted. Channel-level mapping is also a one-step rewrite; do not configure alias chains such as `custom -> public alias -> route key`. Configure `model -> upstream route key` directly.

## Billing Scope

Qoder built-in public aliases and their route keys are manual-pricing-only. They do not fall back to LiteLLM, Claude Opus, or any model-file price when no effective channel price is configured.

- Effective channel price means at least one price pointer or valid interval is configured.
- `nil` price fields mean unconfigured.
- A pointer value of `0` means explicitly free and is treated as an effective manual price.
- Empty Qoder channel pricing rows are treated as unconfigured for billing and do not mask an alias-level manual price.
- Non-Qoder requested model names, such as a custom `gpt-5.4` entry mapped to a Qoder route key, continue to use the normal TokenRouter requested-model pricing when no effective Qoder manual price is configured, even if the channel's billing model source is `upstream`.

Pricing precedence for Qoder billing is:

1. requested public/custom alias manual channel price;
2. channel-mapped route key manual channel price;
3. upstream model manual channel price;
4. unpriced / zero-cost usage record.

Unpriced Qoder models are shown as unknown/unpriced in marketplace and admin pricing surfaces. Successful zero-cost Qoder requests still write full usage logs and continue through the normal subscription/balance billing pipeline with a zero billable amount. Qoder remains part of user × platform USD quota accounting when a positive balance-billed amount exists.

## Upstream Account Usage

Qoder has its own upstream monthly credits quota. TokenRouter treats this as account usage/capacity information only; it is separate from TokenRouter user balance, subscription, and user × platform USD quotas.

The account usage view queries the CN OpenAPI `/api/v2/quota/usage` endpoint with the account bearer token and stores a last-known snapshot in `account.extra.qoder_quota_snapshot`. If the live query fails, the admin UI can show the cached snapshot together with a degraded usage error. The complete upstream monthly credit balance is the sum of `userQuota`, `addOnQuota`, and `orgResourcePackage` / `sharedQuota`, matching `qoderclicn`. For non-personal zero-quota accounts, `isQuotaExceeded=true` or a depleted positive combined quota applies the normal account `rate_limited_until` scheduling signal until Qoder's `expiresAt`; remaining add-on or organization credits prevent or clear a stale quota lock. The observed `personal_standard` shape with `total=0`, `remaining=0`, and an extremely distant `expiresAt` is display-only until real request errors confirm it. Request-time signals such as code `115`, `agentLimitResetTime`, or HTTP 429 still use the normal account rate-limit cooldown path.

## Operations

Qoder participates in scheduler snapshots, error passthrough rules, failover, and admin user platform usage views under the `qoder` platform key. For retryable upstream failures (Qoder entitlement denial code `112`, agent limit / 429 / 5xx), the gateway can fail over to another account before any stream chunk has been written; after streaming starts, it returns a stream-aware error instead of switching accounts. Code `112` is treated as model/account entitlement denial, not an auth-token failure, so it does not trigger token refresh.

The inference profile uses the CN Gateway, `session_type=qoderclicn`, and the pinned CN client version. The wire request includes a top-level `system` string; `chat_context.text` and `chat_context.extra.originalContent` are strings. Inference requests use the authenticated COSY session signature and trace context, not the removed login-time `Appcode` / `Date` / `Signature` scheme. After login, TokenRouter reads `/api/v2/config/getDataPolicy`: `AGREE` and `NO_RECORD` map to `agree`, while `DISAGREE` maps to `disagree`. If that best-effort lookup is unavailable, the encrypted runtime identity omits `data_policy_agreed` and the `cosy-data-policy` Gateway header defaults to `disagree`; only an explicit positive result sends `agree`.

Admin account data export/import preserves `qoder` / `cosy` accounts and their credentials for backup migration.
