# YC Fork Customizations

This file tracks behavior that the YC fork must preserve while following
upstream. It describes the current maintenance surface, not every historical
commit in the fork.

## Status Values

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

## Active Customizations

| ID | Behavior | Source commits | Main paths | Status | Verification |
|---|---|---|---|---|---|
| `frontend-pnpm-overrides` | Keep workspace-level dependency overrides used by the fork frontend. | `63e2c9ef4` | `frontend/pnpm-workspace.yaml` | `local` | Frozen install, lint, typecheck, frontend tests and production build. |
| `group-health-observability` | Persist group health state and expose health controls and status in the admin UI. | `f05fab8d1`, `0ac502cc8`, `b7a892ad6` | group schema/repository/service, admin groups UI | `local` | Group health service tests, repository contract tests and Groups UI tests. |
| `group-backup-pool-refill` | Refill the Codex backup pool from configured group mappings. | `c4f42b433` | group schema/service, admin groups UI | `local` | Backup-pool service, mapper, repository and Groups UI tests. |
| `openai-oauth-schedulability` | Keep externally managed OpenAI OAuth accounts schedulable without allowing runtime failures to corrupt their ownership state. | `055ec2369`, `55015979b` | account scheduler, token refresh, runtime block and rate-limit services | `local` | Scheduler, token refresh, runtime block and rate-limit tests. |
| `scheduled-test-circuit-breaker` | Apply passive account circuit breaking only after bounded retries and keep request-level OpenAI blocked responses out of account health state. | `ef26ee67e`, `d30849206` | scheduled-test handler/repository/service, OpenAI upstream error classification, gateway failover | `local` | Scheduled-test, request-blocked classification, passive-breaker and failover tests. |
| `scheduled-test-runner-isolation` | Keep the scheduled-test runner enabled by default while allowing temporary blue-green instances to disable it without mutating shared plan schedules. | `d01eb92f4` | config loading and scheduled-test runner startup | `local` | Config default/environment tests, environment reachability guard, runner startup test and blue-green release rehearsal. |
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
| `frontend-build-heap` | Give the containerized frontend build enough heap for the fork UI. | `fe599ead3` | `deploy/Dockerfile` | `local` | Container image build. |

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
