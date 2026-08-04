# Synthetic OpenAI OAuth history

This maintenance command creates one deterministic batch of 80-100 synthetic
OpenAI OAuth accounts and moves historical usage from OpenAI `apikey`
accounts to that batch. It is a dry run unless `--execute` is supplied.

The task cutoff is exclusive and must use an explicit `+08:00` offset. The
following value includes all rows before August 4 and excludes August 4 itself:

```text
2026-08-04T00:00:00+08:00 == 2026-08-03T16:00:00Z
```

## Behavior

- Source rows are `usage_logs` whose current account has
  `platform='openai'` and `type='apikey'`.
- The generated account batch defaults to 90 accounts, split between `plus`
  and `team`, with reserved `.invalid` emails and fixture-prefixed tokens.
- Account names, identifiers, plan ordering, and usage assignment are random
  but repeatable for the same `--seed`.
- Generated accounts inherit the source account's proxy, concurrency,
  priority, rate multiplier, and group bindings.
- Every generated account receives at least one historical request. Only
  `usage_logs.account_id` changes; token, cost, billing, user, key, team, and
  timestamp fields remain unchanged.
- Five-hour and seven-day used percentages are derived from the assigned
  historical account cost in the windows ending at the cutoff.
- The final account state is `status='error'`, `schedulable=false`, with:

```text
Token revoked (401): Encountered invalidated oauth token for user, failing request
```

Each account records its batch ID, original source account ID, and allocation
slot in `extra`, which makes the operation reversible without a side table.
History changes run in 20,000-row transactions by default, with periodic
vacuum passes so a large batch does not retain all obsolete row versions in one
transaction. `--chunk-size` may be lowered when disk headroom is constrained.

## Run

Run from `backend/` with the same database environment used by the service.
First create and verify a database backup, then inspect the dry-run output. It
prints every source account name and ID before any bulk write.

```sh
go run ./cmd/synthesize-openai-oauth-history \
  --before 2026-08-04T00:00:00+08:00 \
  --account-count 90 \
  --chunk-size 20000 \
  --seed 20260804 \
  --batch-id openai-oauth-history-20260804-20260804
```

Restrict the operation to an explicit source set when needed:

```sh
go run ./cmd/synthesize-openai-oauth-history \
  --before 2026-08-04T00:00:00+08:00 \
  --source-account-ids SOURCE_ID_1,SOURCE_ID_2
```

After checking the source IDs, eligible row count, plan split, and
`plan_digest`, repeat the same selection flags and pass that digest to
`--expected-plan-digest` with `--execute`:

When `--source-account-ids` is used, include the identical list in this second
command as well.

```sh
go run ./cmd/synthesize-openai-oauth-history \
  --before 2026-08-04T00:00:00+08:00 \
  --account-count 90 \
  --chunk-size 20000 \
  --seed 20260804 \
  --batch-id openai-oauth-history-20260804-20260804 \
  --expected-plan-digest PLAN_DIGEST \
  --execute
```

The write holds one advisory lock for the operation. Account creation and each
history chunk use short read-committed transactions. Generated accounts are revoked
and unschedulable from insertion onward. Finalization aborts unless the number
of reassigned rows matches the dry-run plan, no source rows remain before the
cutoff, and the generated accounts own zero rows at or after the cutoff.

## Rollback

Inspect the rollback first:

```sh
go run ./cmd/synthesize-openai-oauth-history \
  --rollback-batch openai-oauth-history-20260804-20260804 \
  --chunk-size 20000
```

Restore every assigned row to its recorded source account and remove the
generated accounts:

```sh
go run ./cmd/synthesize-openai-oauth-history \
  --rollback-batch openai-oauth-history-20260804-20260804 \
  --chunk-size 20000 \
  --execute
```
