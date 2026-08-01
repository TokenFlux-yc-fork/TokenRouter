# YC Fork Customizations

This file tracks behavior that the YC fork must preserve while following
upstream. It describes the current maintenance surface, not every historical
commit in the fork.

## 2026-08 运维与能力契约

- `/responses/input_tokens` 的本地 fallback 是带 `estimate` 语义的健康中性兼容路径，不参与 customer billing 或 settlement；成功 schema 仍为 Anthropic-compatible `{"input_tokens": N}`。
- `Retry-After` 使用有界解析和 cooldown deadline 合并，不能缩短已有 block；pre-semantic 才允许有限 retry/failover，post-semantic 或 delivery committed 后绝不透明 replay。
- `max_output_tokens` capability 以 account、canonical endpoint、effective/mapped model、credential/config generation 和 version 组成 exact identity。默认 `strict_output_limit=false` 可使用单次 bounded strip retry；strict 模式不得静默删除字段，所有候选不支持时返回稳定脱敏 capability error；transient/streaming/native-v2 结果不得污染普通 Responses capability。
- `upstream_attempt_attributions` 是 content-free、durable 的运营遥测 ledger。Ops timeline 只读查询该 ledger，支持 gateway/client request ID、稳定时间排序和有界结果；attempt telemetry 不得用于 billing/settlement，且不得保存 payload、Authorization 或原始 URL/header/body。
- Deploy candidate 只表示已验证但未发布的候选，不创建 tag/release，不连接生产，不修改 `.deployment/ACTIVE_RELEASE`。


- `local`: the behavior is maintained by this fork.
- `partial`: upstream covers part of the behavior; a local delta remains.
- `upstreamed`: upstream covers the behavior and the local delta can be removed.

The initial statuses below describe production `v0.1.227` at
`55015979bfaa522caf2f23e34647054cefd3dc47`, before the first `yc/main`
upstream merge.

## Upstream Review: v0.1.230

- Upstream target: `2ab21e0feb3c000909013650f8d9661ecceeb847`.
- Reviewed range: `c791f44c93d60821564b0ed6f87c49896739331f..2ab21e0feb3c000909013650f8d9661ecceeb847`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- Upstream channel-model routing is combined with the fork behavior: scheduler
  reporting uses the routed model, Responses Lite validation errors remain
  excluded from account health, and WebSocket HTTP-bridge retries carry the
  routed model into every retry.
- `go test ./internal/handler ./internal/service` passes on the combined tree.

## Upstream Review: v0.1.235

- Upstream target: `a6a66544f6acd7b79e40dcccaeee19f4e4478f2c` (the
  post-tag `VERSION=0.1.235` sync commit for `v0.1.235`).
- Reviewed range: `2ab21e0feb3c000909013650f8d9661ecceeb847..a6a66544f6acd7b79e40dcccaeee19f4e4478f2c`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- Group health and backup-pool refill remain local while sharing the upstream
  group duplicate-operation ID and reasoning-effort policy schema and APIs.
- OpenAI request-blocked responses and non-429 Grok inference errors remain
  request-scoped. Upstream first-output deadlines, model-scoped transient
  availability, WebSocket write observation, exact terminal-event detection,
  and Grok Free function-tool cache routing are combined with the fork's
  pre-output failover and terminal-tail buffering rules.
- HTTP and WebSocket preambles remain hidden until semantic output. Client
  cancel and overlap control frames are still accepted after an upstream
  preamble, and no retry occurs after semantic output is committed.
- Focused SSE, HTTP bridge, WebSocket failover, account-state, and lifecycle
  tests pass on the combined tree; the complete baseline gates below remain
  required before the merge is finalized.

## Upstream Review: v0.1.236

- Upstream target: `c5ed5a04ef7362da3c1b5c65f4a883728d92c401` (the
  post-tag `VERSION=0.1.236` sync commit for `v0.1.236`).
- Reviewed range: `a6a66544f6acd7b79e40dcccaeee19f4e4478f2c..c5ed5a04ef7362da3c1b5c65f4a883728d92c401`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- OpenAI Live and Ollama Cloud usage are combined with the fork's group health,
  backup-pool, OAuth capacity, and build-identity fields and routes.
- Upstream proxy disconnect tracking is combined with the fork's guarded first
  output, pre-output failover, native compact terminal failure, and hidden
  preamble semantics.
- Grok 402 and non-429 5xx responses remain request-scoped in accordance with
  `grok-inference-error-classification`; only 429 persists scheduling state.
- The complete baseline gates below remain required before this merge is
  finalized.

## Upstream Review: v0.1.238

- Upstream target: `003dd01a24c41ace1c36eb399c692a0401ae9c7d` (the
  post-tag `VERSION=0.1.238` sync commit for `v0.1.238`).
- Reviewed range: `96edeacb2b348efa78fbd909d9a853d5a7a0084a..003dd01a24c41ace1c36eb399c692a0401ae9c7d`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- Upstream team and batch-image billing attribution is combined with the
  fork's group health, backup-pool, OAuth capacity, and build-identity fields.
  Native compact, Responses routing, and pre-output failover behavior remain
  local and unchanged by this upstream range.
- The team attribution migration is locally hardened for the production usage
  history: legacy rows remain nullable, existing-table indexes are concurrent,
  and a migration-only process supports blue-green rollout without workers.
- The only merge conflict was generated Ent runtime field indexing. Ent was
  regenerated from the combined schema after resolving the field order.
- The complete baseline gates, repository integration tests, Compose parsing,
  handler race tests, and serial service race tests pass. The default parallel
  service race test still reports concurrent Gin `SetMode` calls; the same
  four-test reproducer fails on the canonical upstream target.

## Upstream Review: v0.1.241

- Upstream target: `23ca2fff357e35b9fe2e10b129160f92a0030e1c` (the
  post-tag `VERSION=0.1.241` sync commit for `v0.1.241`).
- Reviewed range: `003dd01a24c41ace1c36eb399c692a0401ae9c7d..23ca2fff357e35b9fe2e10b129160f92a0030e1c`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- Upstream composite API-key routing is combined with the fork's group health,
  backup-pool, OpenAI capacity/failover, and Team online-migration behavior.
  Batch-image reads keep the fork's legacy billing-user fallback while adding
  the upstream composite group and requested-model fields.
- Upstream group-creation sort locking is combined with the fork's persisted
  health defaults. The five textual conflicts were resolved by retaining both
  behaviors and both sets of regression tests rather than selecting either
  side wholesale.
- Upstream content-moderation Team attribution migration `225` and composite
  API-key migration `226` are locally hardened to preserve the fork's online
  rollout rules: bounded lock waits, nullable historical attribution, no
  startup-time large-table backfill, `NOT VALID` historical constraints, and
  retry-safe concurrent indexes in `225a`/`226a`. Migration-only execution
  remains required before application startup.
- Upstream Codex WebSocket configuration and risk-control Team scoping do not
  replace the fork's OpenAI native compact, Responses validation, pre-output
  failover, scheduled-runner isolation, or build-identity deltas.
- Focused repository, migration, service, and API-contract tests pass on the
  combined tree; the complete baseline gates below remain required before the
  merge is finalized.

## Upstream Review: v0.1.242

- Upstream target: `fa7ed9c5fabef508f3c8cae9e4d1a413ddba34b7` (the
  post-tag `VERSION=0.1.242` sync commit for `v0.1.242`).
- Reviewed range: `23ca2fff357e35b9fe2e10b129160f92a0030e1c..fa7ed9c5fabef508f3c8cae9e4d1a413ddba34b7`.
- No active customization is fully upstreamed in this range; all rows remain
  `local`.
- Upstream unified pool-mode error-policy decisions and mapped-model media
  handling are combined with the fork's request-scoped Grok classifications,
  opaque-provider neutral failover, canonical stream-terminal handling, and
  pre-semantic versus delivery-committed replay boundary.
- OpenAI HTTP, SSE, WebSocket, and HTTP-bridge paths retain response headers in
  typed failover errors and preserve continuation/native-compaction staging
  while using the upstream `UpstreamErrorDecision` policy contract.
- Focused Grok, pool-policy, OpenAI terminal, and WebSocket tests plus the
  complete baseline gates remain required before this merge is finalized.

## Active Customizations

| ID | Behavior | Source commits | Main paths | Status | Verification |
|---|---|---|---|---|---|
| `frontend-pnpm-overrides` | Keep workspace-level dependency overrides used by the fork frontend. | `63e2c9ef4` | `frontend/pnpm-workspace.yaml` | `local` | Frozen install, lint, typecheck, frontend tests and production build. |
| `group-health-observability` | Persist group health state and expose health controls and status in the admin UI. | `f05fab8d1`, `0ac502cc8`, `b7a892ad6` | group schema/repository/service, admin groups UI | `local` | Group health service tests, repository contract tests and Groups UI tests. |
| `group-backup-pool-refill` | Refill the Codex backup pool from configured group mappings. | `c4f42b433` | group schema/service, admin groups UI | `local` | Backup-pool service, mapper, repository and Groups UI tests. |
| `openai-oauth-schedulability` | Keep externally managed OpenAI OAuth accounts schedulable without allowing runtime failures to corrupt their ownership state. | `055ec2369`, `55015979b` | account scheduler, token refresh, runtime block and rate-limit services | `local` | Scheduler, token refresh, runtime block and rate-limit tests. |
| `scheduled-test-circuit-breaker` | Apply passive account circuit breaking only after bounded retries and keep request-level OpenAI blocked responses out of account health state. | `ef26ee67e`, `d30849206` | scheduled-test handler/repository/service, OpenAI upstream error classification, gateway failover | `local` | Scheduled-test, request-blocked classification, passive-breaker and failover tests. |
| `scheduled-test-runner-isolation` | Keep the scheduled-test runner enabled by default, allow temporary blue-green instances to disable it without mutating shared schedules, and require enabled instances to hold a renewable fail-closed Redis lease with a handoff fencing token. | `d01eb92f4`, task `scheduled-runner-fenced-lease` | config loading, scheduled-test runner, leader-lease cache and startup wiring | `local` | Config switch tests; lease acquire/renew/expiry/release, stale-token, Redis-failure and two-instance handoff tests; blue-green release rehearsal. |
| `parallel-startup-concurrency-safety` | Preserve live account/user concurrency slots and wait counters across parallel blue-green startup; reclaim them only through Redis TTL and active-index expiry rather than process-prefix mismatch or a startup-wide wait-key sweep. | task `parallel-startup-concurrency-safety` | concurrency cache, active indexes and startup wiring | `local` | Repository integration tests for two live prefixes, indexed TTL expiry and unindexed wait-counter preservation; rolling-deployment rehearsal. |
| `openai-capacity-failure-semantics` | Keep capacity, retry and terminal stream failures inside the gateway until failover is exhausted. | `93980cc41`, `863a94711`, `317678716` | OpenAI gateway HTTP/SSE/WebSocket paths | `local` | Gateway failure, SSE, WebSocket, retry and terminal-status tests. |
| `openai-http2-body-fallback` | Retry eligible HTTP/2 internal stream/body failures through the bounded upstream fallback path. | `87a2387ec` | `backend/internal/repository/http_upstream*` | `local` | HTTP upstream repository tests. |
| `openai-native-compact-liveness` | Preserve native compact stream liveness, writer state and completion semantics, and keep native v2 on Responses-capable accounts. | `ac83503f1`, `a41329a2a` | compact bridge, native compact liveness, SSE writer, account capability routing | `local` | Native compact, Responses capability/fallback, scheduler, stream bridge, SSE writer and ops logger tests. |
| `cloudra-tool-call-compat` | Recover late tool-call IDs and preserve Codex tool event shapes for Cloudra compatibility. | `bea049769`, `ce6f03ee6` | `backend/internal/pkg/apicompat` | `local` | API compatibility Codex event tests and lint. |
| `openai-oauth-pool-capacity` | Estimate and expose aggregate OpenAI OAuth pool capacity. | `4cdb5f0e9` | admin capacity service/API and capacity UI | `local` | Admin capacity service/API, locale, router and UI tests. |
| `openai-oauth-group-capacity` | Break OpenAI OAuth capacity down by group and expose group management controls. | `daedbb5eb` | admin capacity service/API and capacity UI | `local` | Group-capacity service/API and UI tests. |
| `fork-build-identity` | Publish upstream version and stable `yc-fork` identity as separate build metadata and UI values. | `65b2f15a7` | Docker/build files, settings API, version UI | `local` | Build identity backend/frontend tests plus binary and browser identity probes. |
| `team-online-migration` | Keep the upstream team attribution schema backward-compatible with historical NULL rows and apply it independently from application workers. | `6f2bbe4c6` | team migrations, migration runner, usage/batch repositories, server CLI | `local` | Team migration assertions, non-transactional retry tests, isolated-schema PostgreSQL rehearsal, full backend tests, lint and build. |
| `responses-lite-validation` | Reject residual unsupported Responses Lite fields and preserve event/tool validation semantics. | `6a73e12df` | OpenAI gateway handler/forwarder, Responses Lite tools | `local` | Responses Lite unit tests and production protocol probes. |
| `grok-inference-error-classification` | Persist Grok inference scheduling state only for 429; other inference errors fail over for the current request only. | `6f31ce627` | `openai_gateway_grok*` | `local` | Grok gateway tests, refresh race tests and account-state production probe. |
| `grok-responses-transient-404` | Retry the exact xAI Responses `bad_response_status_code` 404 once on the same account, then fail over without entering another same-account pool retry; unrelated 404 responses keep their existing behavior. | `5f7439b85` | `grok_upstream_errors*`, `openai_gateway_grok*` | `local` | Exact classifier, API-key same-account recovery, retry exhaustion/failover and non-overmatch Grok tests. |
| `openai-remote-compaction-v2-contract` | Require native Remote Compaction v2 to use an exact, current capability domain and to validate and stage the complete upstream attempt before committing exactly one valid result; before semantic commit, unavailable native capability or exhausted native attempts fall back to the legacy compact bridge; final candidate qualification remains release-gated. | task `#102` (pending commit) | OpenAI HTTP/SSE/WebSocket gateway, capability/probe/attribution repositories, migrations `227`-`231` | `local` | Strict classifier and validator fixtures; bounded staging, disconnect, native-to-legacy fallback and failover tests; capability, probe-fencing, migration, attribution, settlement and HTTP/WebSocket parity gates below. |
| `frontend-build-heap` | Give the containerized frontend build enough heap for the fork UI. | `fe599ead3` | `deploy/Dockerfile` | `local` | Container image build. |

## Scheduled Test Runner Lease

Enabled scheduled-test runners compete for the Redis lease
`leader:fenced:{scheduled-test-runner}:lease`. The lease has a 45-second TTL,
renews every 10 seconds, and allocates a monotonically increasing token from
`leader:fenced:{scheduled-test-runner}:token` on each acquisition. Renew and
release compare both the process owner ID and fencing token. Lease acquisition,
renewal, or a pre-side-effect fence check failing cancels in-flight work and
prevents result, circuit-breaker, and schedule updates. A missing lease backend
keeps the runner stopped rather than reverting to multi-instance execution.

## Parallel Startup Concurrency State

The historical startup cleanup entry point now reconciles only expired active
index candidates. A request ID prefix identifies a process but does not prove
that another prefix is stale while blue and green instances overlap. Startup
therefore never removes another prefix's live account/user slots and never
scans or bulk-deletes wait counters. Slot scores, key TTLs, wait-counter TTLs,
and the existing active-index reconciliation worker remain the expiry authority.

## Remote Compaction v2 Contract

The `openai-remote-compaction-v2-contract` customization is fail-closed and
preserves these boundaries:

- **Strict request classifier.** On a bare Responses route, a request is native
  Remote Compaction v2 only when `stream` is the JSON boolean `true`, an
  `x-codex-beta-features` comma-delimited token is exactly and case-sensitively
  `remote_compaction_v2`, `input` is a non-empty array, and its final element is
  an object whose string `type` is exactly `compaction_trigger`. A trigger only
  earlier in the input, a substring or case-folded feature match, a non-boolean
  stream value, a Responses subresource, or the legacy compact route must not
  enter this contract. The classifier requires a final trigger; it does not
  claim that the request contains only one trigger.
- **Transport-independent exactly-one validation.** HTTP SSE and WebSocket feed
  the same Responses event validator. Success requires exactly one well-formed
  compaction item from `response.output_item.done`, exactly one successful
  terminal event, and no invalid, duplicate-terminal, or post-terminal frame.
  The accepted item types are exactly `compaction` and `compaction_summary`,
  with a non-empty bounded string content field. Zero, multiple, malformed,
  failed-terminal, incomplete, or resource-limited attempts fail closed;
  terminal framing alone is not a successful Responses terminal. Legacy
  reconstruction from item-added events or a terminal response body is not
  evidence for this contract.
- **Bounded whole-attempt staging.** Every candidate attempt is retained until
  semantic validation succeeds, under per-attempt byte/event/duration limits,
  a request-cumulative byte limit, and a process-wide staged-byte budget; large
  stages may spill to a private temporary file. Before commit, any transport,
  semantic, or staging failure discards the entire attempt and may fail over.
  Commit is one-way: after bytes or WebSocket frames begin reaching the client,
  the attempt is not safe to fail over, and a commit/write failure must not
  replay another account into the same customer response. Native HTTP requests
  remain linked to client cancellation. Responses WebSocket ingress uses one
  session-scoped client reader across account failover; a peer close cancels an
  active native `ctx_pool`, `passthrough`, or `http_bridge` upstream attempt and
  releases staging without changing the ordinary-stream usage-drain policy.
- **Pre-commit availability fallback.** A missing compatibility-domain binding,
  an empty exact native-capability pool, or exhaustion of staged native attempts
  demotes the HTTP request once to the existing `/responses/compact` bridge only
  while no semantic byte has been committed. The demotion removes only the
  `remote_compaction_v2` feature token, normalizes the compact body, preserves
  session/previous-response stickiness, resets request-local native exclusions,
  and keeps SSE liveness active. It does not clear shared bindings, infer native
  capability from legacy support, or replay after semantic delivery begins.
- **Compatibility and capability domain.** Reusable state is scoped by provider,
  opaque canonical-upstream fingerprint, effective mapped model, and contract
  version `remote_compaction_v2`; transport is deliberately excluded so HTTP
  and WebSocket share evidence. Successful response/session bindings persist
  this payload-free tuple in Redis as well as the local hot cache, so a
  continuation remains verifiable across instances and blue-green handoff;
  a missing, expired, malformed, or mismatched distributed binding fails
  closed. The exact persisted capability key is
  `(account_id, upstream_fingerprint, effective_model, contract_version)` and
  cannot be inferred from generic Responses or legacy compact support. A
  `trusted_official` admission additionally requires an OpenAI OAuth account,
  the canonical official Responses identity, its matching fingerprint, and
  Responses endpoint capability. Probe evidence expires with its freshness
  window; `force_on`/`force_off` overrides require explicit manual-override
  metadata, cease to apply when revoked or expired, and never bypass semantic
  validation. An active exact-key semantic quarantine denies admission,
  including trusted-official and force-on candidates.
- **Schema ownership.** Migration `227` adds exact-key capability, override,
  quarantine, probe-lease, and payload-free probe-result state. Migration `228`
  adds a content-free, per-real-upstream-attempt attribution ledger that is
  telemetry rather than customer billing. Migration `229` adds the independent
  UTC daily operator-cost reservation/commit ledger for probes. Migration `230`
  hardens dispatch with reservation expiry and irreversible authorization-
  sentinel identity fields plus an expired-reservation recovery index. Migration
  `231` requires finite, nonblank manual-override metadata and adds immutable
  exact-key set/revoke audit rows while the canonical row returns to `auto` on
  revoke. These migrations are additive, retry-safe, and must pass isolated
  PostgreSQL rehearsal before rollout.
- **Probe isolation and recovery.** Autonomous probing is disabled by default.
  When explicitly enabled, it requires an isolated active group, a dedicated
  non-composite API-key authorization sentinel, an active sentinel owner, an
  allowlisted model, positive output/cost bounds, and database-leased claims.
  Explicitly grouped API-key/custom HTTPS Responses endpoints are eligible and
  remain subject to the global upstream URL policy; they are not restricted to
  the public OpenAI hostname.
  The sentinel is a kill switch, never the candidate's upstream credential.
  Dispatch revalidates identity and uses claim/account-revision fencing; stale
  workers or changed identities may record only an inconclusive, payload-free
  result. Cost is reserved before dispatch, dispatched work is committed, and
  work known not to have been sent is released. The probe runner invokes the
  budget repository's conservative expired-reservation reaper before claiming
  new work; definitely dispatched reservations remain charged and are never
  reclaimed from elapsed time alone.
- **Attribution, settlement, and telemetry gates.** The required design gives
  each actual HTTP, WebSocket, or probe attempt independent
  request/connection/turn and exact-domain attribution. Unknown usage remains
  unknown rather than observed zero. Native customer settlement and scheduler
  success require a validator outcome of `valid`, successful delivery commit,
  and no client disconnect; failed, discarded, uncommitted, or merely observed
  upstream attempts must not charge the customer. Operations telemetry and the
  attribution ledger are bounded metadata only and must not contain
  request/response bodies, secrets, sensitive upstream addresses, or compaction
  content. Production HTTP and WebSocket paths populate the durable attempt
  coordinator, project its terminal state into the forward result, gate customer
  settlement on valid committed delivery plus persisted attribution, and feed
  the bounded attempt projection into the existing compact outcome logger.

### Remote Compaction v2 Release Gates

The customer path remains guarded by exact capability admission. Autonomous
probing remains off unless operators deliberately configure and enable it. The
HTTP and WebSocket implementations are required to have parity for classifier,
validator, whole-attempt staging, commit/failover, attribution, settlement, and
redaction semantics; implementation presence alone is not release evidence.

Before enabling this customization in a release, all of the following must be
recorded as passing on the final combined tree:

1. Strict positive/negative classifier fixtures and transport-independent
   exactly-one validator fixtures, including malformed, duplicate-terminal,
   post-terminal, incomplete-stream, and stage-limit cases.
2. HTTP/SSE end-to-end tests for pre-commit failover, successful atomic commit,
   post-commit no-retry, client cancellation, usage settlement, attribution,
   semantic quarantine, and bounded memory/disk cleanup.
3. Equivalent native WebSocket tests over `ctx_pool`, `passthrough`, and
   `http_bridge`, including text/binary frame preservation, disconnects,
   reconnect/turn identity, pre-commit failover, and post-commit no-retry.
4. Capability and scheduler tests for exact-key mismatch, mapped models,
   trusted-official identity, stale probe evidence, override expiry/revocation,
   quarantine, and HTTP/WebSocket reuse of the same compatibility domain.
5. Probe tests for sentinel revocation, claim and account-revision fencing,
   concurrent workers, reservation exhaustion, dispatch settlement, stale
   results, crash recovery, and payload-free/redacted persistence.
6. Isolated PostgreSQL apply/reapply and repository integration tests for
   migrations `227`-`231`, including constraints, leases, stale writes,
   reservation recovery, immutable override audits, and attribution state
   transitions.
7. Customer-settlement and operations-telemetry hard-gate tests proving that
   every real upstream attempt is attributed while only a valid committed
   customer delivery can settle usage, and that unknown usage is not zero.
8. The complete baseline gates below, race tests for the affected handler and
   service packages, Compose validation, and controlled non-production HTTP and
   WebSocket protocol probes.

At task `#102` implementation time, production classifier, exact capability,
probe and reaper, shared validator/staging, semantic quarantine, attempt
attribution, settlement, bounded telemetry, and all enabled HTTP/WebSocket mode
wiring are present with focused regression coverage. Controlled protocol probes,
isolated migration rehearsals, and the complete final candidate baseline remain
release evidence rather than implementation claims. Remote Compaction v2
therefore remains release-gated; autonomous probing remains disabled unless its
explicit isolation and budget configuration is supplied.

## Qualification-Only Commits

These commits improve evidence for the customizations above but do not define
separate production behavior:

| Commit | Purpose |
|---|---|
| `5c8297b64` | Distinguish API-key server failover in gateway tests. |
| `51b1ef617` | Configure the active-retry qualification fixture. |
| `7b00e435d` | Synchronize the usage-cleanup recompute test stub for the serial service race gate. |

Other test and lint adjustments are listed with their owning customization in
the active table.

## Baseline Gates

Every upstream merge and release candidate must run:

```bash
cd backend
go mod verify
go test ./...
go test -tags=unit ./...
go vet ./...

cd ../frontend
pnpm install --frozen-lockfile
pnpm lint:check
pnpm typecheck
pnpm test:run
pnpm build
```

Also run the focused tests and protocol probes named in the active table,
Compose configuration validation, migration rehearsal, fork identity checks,
and desktop/mobile browser probes.

## Update Rules

1. Add or change a row in the same merge that changes fork behavior.
2. During an upstream merge, review every row and update its status.
3. Mark a row `upstreamed` only after the local delta is removed and its
   regression coverage passes against the upstream implementation.
4. Do not remove historical source commits from this table merely because a
   feature branch or worktree was deleted.
