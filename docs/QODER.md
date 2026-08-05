# Qoder Native Upstream

TokenRouter supports Qoder native upstream accounts through the Qoder COSY gateway path. Public request-side aliases map to Qoder route keys, and raw route keys remain valid direct request models for compatibility and operations.

## Account Types

- `cosy` accounts can use a PAT bootstrap or device OAuth credentials on either the international (`global`) or China (`cn`) site.
- `credentials.site` selects the site. Missing values remain compatible with existing accounts and resolve to `global`.
- `credentials.refresh_mode` records the token source. Missing values resolve to `cosy`; China standard OAuth uses `qodercn20`.
- Manual import accepts either `pat` by itself, or an existing COSY token set.
- Existing COSY token credentials include `security_oauth_token`, `refresh_token`, `machine_id`, `machine_token`, `machine_type`, `uid` or `aid`, and optional organization metadata.
- International OAuth and manual COSY credentials refresh through the international Center flow. China standard OAuth refreshes its OpenAPI token and then completes `userinfo -> status`; China manual COSY credentials use the Gateway legacy refresh path. PAT sessions are rebuilt from the original PAT.
- The China integration covers the standard QODER_PAT and QoderCN20 login. Enterprise dedicated-domain `PERSONAL_TOKEN`, organization selection, AK/SK, and region discovery are not supported.

## Model Aliases and Mapping

International public aliases are:

- `claude-opus-4-6`
- `auto`
- `performance`
- `efficient`
- `lite`
- `qwen3.8-max`
- `qwen3.7-max`
- `qwen3.7-plus`
- `kimi-k3`
- `kimi-k2.7-code`
- `glm-5.2`
- `deepseek-v4-pro`
- `deepseek-v4-flash`
- `minimax-m3`

China public aliases are:

- `auto`
- `qwen3.8-max`
- `qwen3.7-max`
- `qwen3.7-plus`
- `qwen3.6-flash`
- `deepseek-v4-pro`
- `deepseek-v4-flash`
- `glm-5.2`
- `kimi-k2.7-code`
- `minimax-m2.7`

Account model lists and default alias resolution follow `credentials.site`. Lists without account context use a stable union with international models first. A mixed-site group exposes the union supported by its schedulable accounts, while site-specific aliases and route keys are not scheduled to an incompatible account. Explicit account mapping remains the override mechanism, and unknown raw route keys continue to pass through.

On both sites, `qwen3.8-max` maps to the formal `qmodel_38max` route. The removed `qwen3.8-max-preview` alias and `qmodel_preview` route are not silently redirected. Accounts that still need the old request name must configure an explicit `model_mapping`, for example `qwen3.8-max-preview -> qmodel_38max`.

Qoder account `model_mapping` follows the same rewrite-rule semantics as other platforms:

- key: model name accepted at that routing layer;
- value: final Qoder route/upstream model name;
- the mapping itself does not restrict the request model space.

Use `model_whitelist` when an account must be limited to specific final route/upstream models. The gateway applies mapping first and then checks the whitelist. If no whitelist is configured, the account remains unrestricted. Channel-level mapping is also a one-step rewrite; do not configure alias chains such as `custom -> public alias -> route key`. Configure `model -> upstream route key` directly.

## Site Thinking Controls

The site capability snapshots were verified against Qoder international 1.21.2 and Qoder CN 1.10.0. These versions are propagated through the OpenAPI User-Agent, `Cosy-Version`, the signed `cosyVersion` payload, and inference `business.version`.

Capability lookup runs after account-level model mapping and public alias resolution, so a custom request model mapped directly to a known route key receives the same handling. Shared international and China route keys use the same Thinking capabilities. Unknown route keys and international-only models without a verified capability are not modified.

| Site | Public model | Route key | Thinking capability | Downstream mapping |
| --- | --- | --- | --- | --- |
| International | `qwen3.8-max` | `qmodel_38max` | Toggle only | Any valid effort, enabled/adaptive switch, or positive budget enables Thinking; no level is sent |
| International | `qwen3.7-max` | `qmodel_latest` | Toggle only | Same as Qwen3.8-Max |
| International | `qwen3.7-plus` | `qmodel` | Toggle only | Same as Qwen3.8-Max |
| International | `deepseek-v4-pro` | `dmodel` | High / Max | Minimal, Low, and Medium become High; High, Very High, and Max become Max; any positive budget becomes Max |
| International | `deepseek-v4-flash` | `dfmodel` | High / Max | Same as DeepSeek-V4-Pro |
| International | `glm-5.2` | `gm51model` | High / Max | Same as DeepSeek-V4-Pro |
| China | `auto` | `auto` | No user-editable control | No override |
| China | `qwen3.8-max` | `qmodel_38max` | Toggle only | Same as international Qwen3.8-Max |
| China | `qwen3.7-max` | `qmodel_latest` | Toggle only | Same as Qwen3.8-Max |
| China | `qwen3.7-plus` | `qmodel` | Toggle only | Same as Qwen3.8-Max |
| China | `qwen3.6-flash` | `q36fmodel` | No user-editable control | No override |
| China | `deepseek-v4-pro` | `dmodel` | High / Max | Minimal, Low, and Medium become High; High, Very High, and Max become Max; any positive budget becomes Max |
| China | `deepseek-v4-flash` | `dfmodel` | High / Max | Same as DeepSeek-V4-Pro |
| China | `glm-5.2` | `gm51model` | High / Max | Same as DeepSeek-V4-Pro |
| China | `kimi-k2.7-code` | `kmodel` | No user-editable control | No override |
| China | `minimax-m2.7` | `mmodel` | No user-editable control | No override |

The gateway reads the protocol-native controls from each inbound endpoint:

- Chat Completions: `reasoning_effort`, with `reasoning.effort` accepted as a compatibility fallback.
- Responses: `reasoning.effort`, with `reasoning_effort` accepted as a compatibility fallback.
- Anthropic Messages: `output_config.effort`, `thinking.type`, and `thinking.budget_tokens`.

Explicit `thinking.type=disabled` or effort `none` always wins. Otherwise an explicit valid effort wins over a positive budget, followed by `enabled` / `adaptive`; missing or invalid controls remain disabled. Toggle-capable models use Qoder's `reasoning_effort=none` override when disabled so the request cannot fall back to an upstream default. This preserves TokenRouter's explicit-control contract even though Qoder marks Qwen3.8-Max Thinking as enabled by default. Unknown effort strings are ignored instead of rejecting the request.

## Context Windows

Context lookup runs after account-level model mapping and public alias resolution. Each request therefore selects the highest context verified for the final route and the selected account's site. A failover attempt that selects an account from another site recalculates the capability before rebuilding the Qoder payload.

| Site | Maximum input tokens | Route keys |
| --- | ---: | --- |
| International | 1,000,000 | `ultimate`, `performance`, `qmodel_38max`, `qmodel_latest`, `qmodel`, `kmodel_latest`, `gm51model`, `dmodel`, `dfmodel`, `mmodel` |
| International | 256,000 | `kmodel` |
| International | 180,000 | `auto`, `efficient`, `lite` |
| China | 1,000,000 | `qmodel_38max`, `qmodel_latest`, `qmodel`, `q36fmodel`, `dmodel`, `dfmodel`, `gm51model` |
| China | 256,000 | `kmodel` |
| China | 200,000 | `mmodel` |
| China | 180,000 | `auto` |

Routes with an official runtime `contextConfig` send the selected maximum in `model_config.max_input_tokens`, `chat_context.extra.ideModelConfigOverride.max_input_tokens`, and `parameters.context_length`. Routes with a fixed maximum send only `model_config.max_input_tokens`. Unknown, hidden, and removed raw route keys remain pass-through models, use a conservative 200,000-token fallback, and do not receive a fabricated runtime context selection.

TokenRouter does not read a client-declared context limit. Chat Completions, Responses, and Anthropic Messages output-token fields continue to control output only. Clients retain their own model catalogs, compaction thresholds, and truncation behavior; `/v1/models` and `/models` do not expose non-standard context metadata.

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

The account usage view queries the selected site's COSY-signed Gateway quota endpoint and stores a last-known snapshot in `account.extra.qoder_quota_snapshot`. China requests include cached `orgId` and an optional `quota_key` credential when present. If the live query fails, the admin UI can show the cached snapshot together with a degraded usage error. The complete upstream monthly credit balance is the sum of `userQuota`, `addOnQuota`, and `orgResourcePackage` / `sharedQuota`, matching qodercli's usage view. For non-personal zero-quota accounts, `isQuotaExceeded=true` or a depleted positive combined quota applies the normal account `rate_limited_until` scheduling signal until Qoder's `expiresAt`; remaining add-on or organization credits prevent or clear a stale quota lock. The observed `personal_standard` shape with `total=0`, `remaining=0`, and an extremely distant `expiresAt` is display-only until real request errors confirm it. Request-time signals such as code `115`, `agentLimitResetTime`, or HTTP 429 still use the normal account rate-limit cooldown path.

## Operations

Qoder participates in scheduler snapshots, error passthrough rules, failover, and admin user platform usage views under the `qoder` platform key. For retryable upstream failures (Qoder entitlement denial code `112`, agent limit / 429 / 5xx), the gateway can fail over to another account before any stream chunk has been written; after streaming starts, it returns a stream-aware error instead of switching accounts. Code `112` is treated as model/account entitlement denial, not an auth-token failure, so it does not trigger token refresh.

Admin account data export/import preserves `qoder` / `cosy` accounts and their credentials for backup migration.
