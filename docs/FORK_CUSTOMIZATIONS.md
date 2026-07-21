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

## Active Customizations

| ID | Behavior | Source commits | Main paths | Status | Verification |
|---|---|---|---|---|---|
| `frontend-pnpm-overrides` | Keep workspace-level dependency overrides used by the fork frontend. | `63e2c9ef4` | `frontend/pnpm-workspace.yaml` | `local` | Frozen install, lint, typecheck, frontend tests and production build. |
| `group-health-observability` | Persist group health state and expose health controls and status in the admin UI. | `f05fab8d1`, `0ac502cc8`, `b7a892ad6` | group schema/repository/service, admin groups UI | `local` | Group health service tests, repository contract tests and Groups UI tests. |
| `group-backup-pool-refill` | Refill the Codex backup pool from configured group mappings. | `c4f42b433` | group schema/service, admin groups UI | `local` | Backup-pool service, mapper, repository and Groups UI tests. |
| `openai-oauth-schedulability` | Keep externally managed OpenAI OAuth accounts schedulable without allowing runtime failures to corrupt their ownership state. | `055ec2369`, `55015979b` | account scheduler, token refresh, runtime block and rate-limit services | `local` | Scheduler, token refresh, runtime block and rate-limit tests. |
| `scheduled-test-circuit-breaker` | Apply passive account circuit breaking only after bounded retries and keep request-level OpenAI blocked responses out of account health state. | `ef26ee67e`, `d30849206` | scheduled-test handler/repository/service, OpenAI upstream error classification, gateway failover | `local` | Scheduled-test, request-blocked classification, passive-breaker and failover tests. |
| `openai-capacity-failure-semantics` | Keep capacity, retry and terminal stream failures inside the gateway until failover is exhausted. | `93980cc41`, `863a94711`, `317678716` | OpenAI gateway HTTP/SSE/WebSocket paths | `local` | Gateway failure, SSE, WebSocket, retry and terminal-status tests. |
| `openai-http2-body-fallback` | Retry eligible HTTP/2 internal stream/body failures through the bounded upstream fallback path. | `87a2387ec` | `backend/internal/repository/http_upstream*` | `local` | HTTP upstream repository tests. |
| `openai-native-compact-liveness` | Preserve native compact stream liveness, writer state and completion semantics. | `ac83503f1` | compact bridge, native compact liveness, SSE writer | `local` | Native compact, stream bridge, SSE writer and ops logger tests. |
| `openai-http-continuation-wsv2` | Bridge OpenAI HTTP Responses continuations carrying `previous_response_id` to eligible upstream Responses WebSocket v2 accounts while preserving ordinary HTTP and non-OpenAI behavior. | `877c26365` | OpenAI Responses handler, transport selection and WSv2 forwarder | `local` | Handler validation, transport decision and HTTP-to-WSv2 continuation bridge tests. |
| `cloudra-tool-call-compat` | Recover late tool-call IDs and preserve Codex tool event shapes for Cloudra compatibility. | `bea049769`, `ce6f03ee6` | `backend/internal/pkg/apicompat` | `local` | API compatibility Codex event tests and lint. |
| `openai-oauth-pool-capacity` | Estimate and expose aggregate OpenAI OAuth pool capacity. | `4cdb5f0e9` | admin capacity service/API and capacity UI | `local` | Admin capacity service/API, locale, router and UI tests. |
| `openai-oauth-group-capacity` | Break OpenAI OAuth capacity down by group and expose group management controls. | `daedbb5eb` | admin capacity service/API and capacity UI | `local` | Group-capacity service/API and UI tests. |
| `fork-build-identity` | Publish upstream version and stable `yc-fork` identity as separate build metadata and UI values. | `65b2f15a7` | Docker/build files, settings API, version UI | `local` | Build identity backend/frontend tests plus binary and browser identity probes. |
| `responses-lite-validation` | Reject residual unsupported Responses Lite fields and preserve event/tool validation semantics. | `6a73e12df` | OpenAI gateway handler/forwarder, Responses Lite tools | `local` | Responses Lite unit tests and production protocol probes. |
| `grok-inference-error-classification` | Persist Grok inference scheduling state only for 429; other inference errors fail over for the current request only. | `6f31ce627` | `openai_gateway_grok*` | `local` | Grok gateway tests, refresh race tests and account-state production probe. |
| `frontend-build-heap` | Give the containerized frontend build enough heap for the fork UI. | `fe599ead3` | `deploy/Dockerfile` | `local` | Container image build. |

## Qualification-Only Commits

These commits improve evidence for the customizations above but do not define
separate production behavior:

| Commit | Purpose |
|---|---|
| `5c8297b64` | Distinguish API-key server failover in gateway tests. |
| `51b1ef617` | Configure the active-retry qualification fixture. |

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
