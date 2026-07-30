//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func prepareUpstreamAttemptAttributionIntegration(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("228_upstream_attempt_attribution.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `TRUNCATE upstream_attempt_attributions`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `TRUNCATE upstream_attempt_attributions`)
	})
	return ctx
}

func TestUpstreamAttemptAttributionMigrationReapplyIsIdempotent(t *testing.T) {
	ctx := prepareUpstreamAttemptAttributionIntegration(t)
	migrationSQL, err := dbmigrations.FS.ReadFile("228_upstream_attempt_attribution.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var tableName string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT to_regclass('public.upstream_attempt_attributions')::text`).Scan(&tableName))
	require.Equal(t, "upstream_attempt_attributions", tableName)
}

func TestUpstreamAttemptAttributionRepositoryIdempotentCASAndTerminalFence(t *testing.T) {
	ctx := prepareUpstreamAttemptAttributionIntegration(t)
	repo := NewUpstreamAttemptAttributionRepository(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	attempt := integrationUpstreamAttempt(now)

	created, err := repo.Upsert(ctx, attempt)
	require.NoError(t, err)
	replayed, err := repo.Upsert(ctx, attempt)
	require.NoError(t, err)
	require.Equal(t, created.AttemptID, replayed.AttemptID)
	require.Equal(t, int64(1), replayed.StateVersion)

	stale := attempt
	stale.SemanticObserved = true
	stale.Semantic.Outcome = "stale-must-not-win"
	stale.ObservedAt = now.Add(time.Second)
	stored, err := repo.Upsert(ctx, stale)
	require.NoError(t, err)
	require.Empty(t, stored.Semantic.Outcome)

	completed := now.Add(2 * time.Second)
	terminal := attempt
	terminal.State = service.UpstreamAttemptStateTerminal
	terminal.StateVersion = 2
	terminal.CompletedAt = &completed
	terminal.ObservedAt = completed
	terminal.SemanticObserved = true
	terminal.Semantic = service.UpstreamAttemptSemantic{Outcome: "valid", TerminalEvent: "response.completed", TerminalCount: 1, SuccessfulTerminal: true}
	terminal.DeliveryObserved = true
	terminal.DeliveryCommitted = true
	stored, err = repo.Upsert(ctx, terminal)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamAttemptStateTerminal, stored.State)

	newerButOldState := attempt
	newerButOldState.StateVersion = 3
	newerButOldState.ObservedAt = completed.Add(time.Second)
	stored, err = repo.Upsert(ctx, newerButOldState)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamAttemptStateTerminal, stored.State)
	require.Equal(t, int64(2), stored.StateVersion)
}

func TestUpstreamAttemptAttributionRepositoryRejectsAttemptIdentityReuse(t *testing.T) {
	ctx := prepareUpstreamAttemptAttributionIntegration(t)
	repo := NewUpstreamAttemptAttributionRepository(integrationDB)
	attempt := integrationUpstreamAttempt(time.Now().UTC().Truncate(time.Microsecond))
	_, err := repo.Upsert(ctx, attempt)
	require.NoError(t, err)

	conflict := attempt
	conflict.AccountID++
	_, err = repo.Upsert(ctx, conflict)
	require.ErrorIs(t, err, service.ErrUpstreamAttemptConflict)
}

func TestUpstreamAttemptAttributionRepositoryPreservesUnknownUsage(t *testing.T) {
	ctx := prepareUpstreamAttemptAttributionIntegration(t)
	repo := NewUpstreamAttemptAttributionRepository(integrationDB)
	attempt := integrationUpstreamAttempt(time.Now().UTC().Truncate(time.Microsecond))
	stored, err := repo.Upsert(ctx, attempt)
	require.NoError(t, err)
	require.False(t, stored.Usage.Observed)
	require.Nil(t, stored.Usage.InputTokens)
	require.Nil(t, stored.Usage.CostUSD)
}

func integrationUpstreamAttempt(now time.Time) service.UpstreamAttemptAttribution {
	suffix := uuid.NewString()
	return service.UpstreamAttemptAttribution{
		AttemptID:        service.AttemptID("attempt_" + suffix),
		ClientRequestID:  service.ClientRequestID("client_" + suffix),
		GatewayRequestID: service.GatewayRequestID("gateway_" + suffix),
		AccountID:        987654321,
		Transport:        service.UpstreamAttemptTransportHTTP,
		Domain: service.OpenAICompatibilityDomain{
			Provider:            "openai",
			UpstreamFingerprint: service.OpenAIUpstreamFingerprint("upstream_v1_" + suffix),
			EffectiveModel:      "gpt-integration",
			ContractVersion:     "v2",
		},
		State:        service.UpstreamAttemptStateStarted,
		StateVersion: 1,
		StartedAt:    now,
		ObservedAt:   now,
	}
}
