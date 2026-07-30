//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("227_openai_native_compaction_v2_capability.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	overrideMigrationSQL, err := dbmigrations.FS.ReadFile("231_harden_openai_native_compaction_manual_overrides.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(overrideMigrationSQL))
	require.NoError(t, err)

	prefix := "capability-integration-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = integrationDB.ExecContext(cleanupCtx, `
DELETE FROM scheduler_outbox
WHERE account_id IN (SELECT id FROM accounts WHERE name LIKE $1)
`, prefix+"%")
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM accounts WHERE name LIKE $1`, prefix+"%")
		_, _ = integrationDB.ExecContext(cleanupCtx, `DELETE FROM proxies WHERE name LIKE $1`, prefix+"%")
	})
	return ctx, prefix
}

func createOpenAINativeCompactionIntegrationAccount(t *testing.T, ctx context.Context, prefix string, withProxy bool) int64 {
	t.Helper()
	var proxyID any
	if withProxy {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO proxies (name, protocol, host, port, status)
VALUES ($1, 'http', '127.0.0.1', 8080, 'active')
RETURNING id
`, prefix+"-proxy").Scan(&id))
		proxyID = id
	}

	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, proxy_id, credentials, extra)
VALUES ($1, 'openai', 'apikey', $2, '{}'::jsonb, '{}'::jsonb)
RETURNING id
`, prefix+"-account", proxyID).Scan(&accountID))
	return accountID
}

func openAINativeCompactionIntegrationKey(accountID int64, prefix string) service.OpenAINativeCompactionCapabilityKey {
	return service.OpenAINativeCompactionCapabilityKey{
		AccountID:           accountID,
		UpstreamFingerprint: service.OpenAIUpstreamFingerprint("upstream_v1_" + prefix),
		EffectiveModel:      "gpt-integration-" + prefix,
		ContractVersion:     service.OpenAINativeCompactionContractVersion,
	}
}

func countOpenAINativeCompactionAccountOutbox(t *testing.T, ctx context.Context, accountID int64) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM scheduler_outbox
WHERE event_type = $1 AND account_id = $2
`, service.SchedulerOutboxEventAccountChanged, accountID).Scan(&count))
	return count
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationTrustedOfficialContract(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	officialFingerprint, err := service.OfficialOpenAINativeCompactionFingerprint()
	require.NoError(t, err)

	apiKeyID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix+"-apikey", false)
	apiKeyKey := openAINativeCompactionIntegrationKey(apiKeyID, prefix+"-apikey")
	apiKeyKey.UpstreamFingerprint = officialFingerprint
	updated, err := repo.UpsertTrustedOfficial(ctx, &service.Account{ID: apiKeyID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}, apiKeyKey)
	require.ErrorContains(t, err, "exact official OpenAI OAuth identity")
	require.False(t, updated)

	var oauthID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, credentials, extra)
		VALUES ($1, 'openai', 'oauth', '{}'::jsonb, '{}'::jsonb)
		RETURNING id
	`, prefix+"-oauth-account").Scan(&oauthID))
	oauthKey := openAINativeCompactionIntegrationKey(oauthID, prefix+"-oauth")
	oauthKey.UpstreamFingerprint = officialFingerprint
	updated, err = repo.UpsertTrustedOfficial(ctx, &service.Account{ID: oauthID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, oauthKey)
	require.NoError(t, err)
	require.True(t, updated)
	record, err := repo.GetExact(ctx, oauthKey)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.True(t, record.Supported)
	require.Equal(t, service.OpenAINativeCompactionCapabilitySourceTrustedOfficial, record.Source)

	customKey := oauthKey
	customKey.EffectiveModel = "custom-model"
	customKey.UpstreamFingerprint = "upstream_v1_custom_oauth"
	updated, err = repo.UpsertTrustedOfficial(ctx, &service.Account{ID: oauthID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, customKey)
	require.ErrorContains(t, err, "exact official OpenAI OAuth identity")
	require.False(t, updated)
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationManualOverrideAuditLifecycle(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, false)
	key := openAINativeCompactionIntegrationKey(accountID, prefix)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := createdAt.Add(time.Hour)
	revokedAt := createdAt.Add(time.Minute)
	nextProbeAt := revokedAt.Add(time.Second)

	require.NoError(t, repo.UpsertManualOverride(ctx, service.OpenAINativeCompactionManualOverride{
		Key: key, Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
		Actor: "integration-operator", Reason: "bounded incident mitigation",
		CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}))
	updated, err := repo.RevokeManualOverride(ctx, key, revokedAt, nextProbeAt)
	require.NoError(t, err)
	require.True(t, updated)

	rows, err := integrationDB.QueryContext(ctx, `
		SELECT action, mode, override_actor, override_reason,
			override_created_at, override_expires_at, override_revoked_at
		FROM openai_native_compaction_override_audits
		WHERE account_id = $1
		  AND upstream_fingerprint = $2
		  AND effective_model = $3
		  AND contract_version = $4
		ORDER BY id
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type auditRow struct {
		action, mode, actor, reason string
		createdAt, expiresAt        time.Time
		revokedAt                   *time.Time
	}
	var audits []auditRow
	for rows.Next() {
		var row auditRow
		var revoked sql.NullTime
		require.NoError(t, rows.Scan(&row.action, &row.mode, &row.actor, &row.reason, &row.createdAt, &row.expiresAt, &revoked))
		if revoked.Valid {
			value := revoked.Time
			row.revokedAt = &value
		}
		audits = append(audits, row)
	}
	require.NoError(t, rows.Err())
	require.Len(t, audits, 2)
	require.Equal(t, "set", audits[0].action)
	require.Nil(t, audits[0].revokedAt)
	require.Equal(t, "revoke", audits[1].action)
	require.NotNil(t, audits[1].revokedAt)
	require.WithinDuration(t, revokedAt, *audits[1].revokedAt, time.Microsecond)
	for _, audit := range audits {
		require.Equal(t, service.OpenAINativeCompactionCapabilityModeForceOn, audit.mode)
		require.Equal(t, "integration-operator", audit.actor)
		require.Equal(t, "bounded incident mitigation", audit.reason)
		require.WithinDuration(t, createdAt, audit.createdAt, time.Microsecond)
		require.WithinDuration(t, expiresAt, audit.expiresAt, time.Microsecond)
	}

	record, err := repo.GetExact(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, service.OpenAINativeCompactionCapabilityModeAuto, record.Mode)
	require.Equal(t, service.OpenAINativeCompactionCapabilitySourceProbe, record.Source)
	require.Empty(t, record.OverrideActor)
	require.Nil(t, record.OverrideExpiresAt)
	require.WithinDuration(t, nextProbeAt, *record.NextProbeAt, time.Microsecond)
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationClaimDueConcurrentExpiredOverride(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, false)
	key := openAINativeCompactionIntegrationKey(accountID, prefix)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(-time.Second)
	require.NoError(t, repo.UpsertManualOverride(ctx, service.OpenAINativeCompactionManualOverride{
		Key: key, Mode: service.OpenAINativeCompactionCapabilityModeForceOff,
		Actor: "expiry-test", Reason: "natural expiry", CreatedAt: now.Add(-time.Hour), ExpiresAt: &expiresAt,
	}))
	_, err := NewSchedulerOutboxRepository(integrationDB).ListAfterAndReleaseDedup(ctx, 0, 1000)
	require.NoError(t, err)
	outboxBefore := countOpenAINativeCompactionAccountOutbox(t, ctx, accountID)

	type claimResult struct {
		claims []service.OpenAINativeCompactionProbeClaim
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, worker := range []string{"expiry-worker-a", "expiry-worker-b"} {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims, claimErr := repo.ClaimDue(ctx, now, now.Add(time.Minute), worker, 1)
			results <- claimResult{claims: claims, err: claimErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	claimed := 0
	for result := range results {
		require.NoError(t, result.err)
		claimed += len(result.claims)
	}
	require.Equal(t, 1, claimed)
	record, err := repo.GetExact(ctx, key)
	require.NoError(t, err)
	require.Equal(t, service.OpenAINativeCompactionCapabilityModeAuto, record.Mode)
	require.Equal(t, service.OpenAINativeCompactionCapabilitySourceProbe, record.Source)
	require.Empty(t, record.OverrideActor)
	require.Empty(t, record.OverrideReason)
	require.Nil(t, record.OverrideCreatedAt)
	require.Nil(t, record.OverrideExpiresAt)
	require.Nil(t, record.OverrideRevokedAt)
	require.NotEmpty(t, record.ProbeClaimedBy)
	require.Equal(t, outboxBefore+1, countOpenAINativeCompactionAccountOutbox(t, ctx, accountID), "concurrent reconciliation emits one account_changed")
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationClaimDueConcurrentExactKey(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, false)
	key := openAINativeCompactionIntegrationKey(accountID, prefix)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	now := time.Now().UTC()
	created, err := repo.EnsureAutoCandidate(ctx, key, now.Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, created)

	type claimResult struct {
		claims []service.OpenAINativeCompactionProbeClaim
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, worker := range []string{"worker-a", "worker-b"} {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims, claimErr := repo.ClaimDue(ctx, now, now.Add(time.Minute), worker, 1)
			results <- claimResult{claims: claims, err: claimErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	claimed := 0
	claimTokens := map[string]struct{}{}
	for result := range results {
		require.NoError(t, result.err)
		claimed += len(result.claims)
		for _, claim := range result.claims {
			require.Equal(t, key, claim.Capability.Key)
			claimTokens[claim.ClaimToken] = struct{}{}
		}
	}
	require.Equal(t, 1, claimed, "the exact key must be claimed by only one repository call")
	require.Len(t, claimTokens, 1)
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationStaleIdentityFences(t *testing.T) {
	for _, test := range []struct {
		name       string
		withProxy  bool
		changeXmin func(context.Context, int64) error
	}{
		{
			name: "account xmin changed",
			changeXmin: func(ctx context.Context, accountID int64) error {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET notes = $1 WHERE id = $2`, "xmin-fence", accountID)
				return err
			},
		},
		{
			name:      "proxy xmin changed",
			withProxy: true,
			changeXmin: func(ctx context.Context, accountID int64) error {
				_, err := integrationDB.ExecContext(ctx, `
UPDATE proxies SET updated_at = updated_at + INTERVAL '1 microsecond'
WHERE id = (SELECT proxy_id FROM accounts WHERE id = $1)
`, accountID)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
			accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, test.withProxy)
			key := openAINativeCompactionIntegrationKey(accountID, prefix)
			repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
			now := time.Now().UTC()
			created, err := repo.EnsureAutoCandidate(ctx, key, now.Add(-time.Minute))
			require.NoError(t, err)
			require.True(t, created)
			claims, err := repo.ClaimDue(ctx, now, now.Add(time.Minute), "stale-worker", 1)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.NoError(t, test.changeXmin(ctx, accountID))
			outboxBefore := countOpenAINativeCompactionAccountOutbox(t, ctx, accountID)

			supported := true
			write, err := repo.UpsertProbeResult(ctx, service.OpenAINativeCompactionProbeResult{
				Key:             key,
				ClaimToken:      claims[0].ClaimToken,
				AccountRevision: claims[0].AccountRevision,
				Supported:       &supported,
				SemanticOutcome: service.OpenAINativeCompactionValid,
				CheckedAt:       now,
			})
			require.NoError(t, err)
			require.True(t, write.StaleIdentity)
			require.False(t, write.CanonicalUpdated)

			record, err := repo.GetExact(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, record)
			require.False(t, record.Supported)
			require.Equal(t, claims[0].ClaimToken, record.ProbeClaimedBy)
			require.Equal(t, outboxBefore, countOpenAINativeCompactionAccountOutbox(t, ctx, accountID))
			audits, err := repo.ListAudit(ctx, key, 10)
			require.NoError(t, err)
			require.Len(t, audits, 1)
			require.True(t, audits[0].StaleIdentity)
		})
	}
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationExpiredAtWriteIsStaleOnly(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, false)
	key := openAINativeCompactionIntegrationKey(accountID, prefix)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	now := time.Now().UTC()
	created, err := repo.EnsureAutoCandidate(ctx, key, now.Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, created)
	claims, err := repo.ClaimDue(ctx, now, now.Add(time.Minute), "expiry-worker", 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	_, err = integrationDB.ExecContext(ctx, `
UPDATE openai_native_compaction_capabilities
SET probe_claimed_until = NOW() - INTERVAL '1 second'
WHERE id = $1
`, claims[0].Capability.ID)
	require.NoError(t, err)
	outboxBefore := countOpenAINativeCompactionAccountOutbox(t, ctx, accountID)
	supported := true
	write, err := repo.UpsertProbeResult(ctx, service.OpenAINativeCompactionProbeResult{
		Key:             key,
		ClaimToken:      claims[0].ClaimToken,
		AccountRevision: claims[0].AccountRevision,
		Supported:       &supported,
		SemanticOutcome: service.OpenAINativeCompactionValid,
		CheckedAt:       now, // Probe finished before the original lease deadline; DB lease is expired at persistence time.
	})
	require.NoError(t, err)
	require.True(t, write.StaleIdentity)
	require.False(t, write.CanonicalUpdated)

	record, err := repo.GetExact(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.False(t, record.Supported)
	require.Equal(t, claims[0].ClaimToken, record.ProbeClaimedBy)
	require.Equal(t, outboxBefore, countOpenAINativeCompactionAccountOutbox(t, ctx, accountID))
	audits, err := repo.ListAudit(ctx, key, 10)
	require.NoError(t, err)
	require.Len(t, audits, 1)
	require.True(t, audits[0].StaleIdentity)
}

func TestOpenAINativeCompactionCapabilityRepositoryIntegrationLiveResultAtomicAndInconclusivePreservesSupport(t *testing.T) {
	ctx, prefix := prepareOpenAINativeCompactionCapabilityRepositoryIntegration(t)
	accountID := createOpenAINativeCompactionIntegrationAccount(t, ctx, prefix, false)
	key := openAINativeCompactionIntegrationKey(accountID, prefix)
	repo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	now := time.Now().UTC()
	created, err := repo.EnsureAutoCandidate(ctx, key, now.Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, created)
	claims, err := repo.ClaimDue(ctx, now, now.Add(time.Minute), "live-worker", 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	outboxBefore := countOpenAINativeCompactionAccountOutbox(t, ctx, accountID)
	_, err = NewSchedulerOutboxRepository(integrationDB).ListAfterAndReleaseDedup(ctx, 0, 1000)
	require.NoError(t, err)

	supported := true
	write, err := repo.UpsertProbeResult(ctx, service.OpenAINativeCompactionProbeResult{
		Key:             key,
		ClaimToken:      claims[0].ClaimToken,
		AccountRevision: claims[0].AccountRevision,
		Supported:       &supported,
		SemanticOutcome: service.OpenAINativeCompactionValid,
		CheckedAt:       now,
		NextProbeAt:     openAINativeCompactionIntegrationTimePointer(now.Add(time.Hour)),
	})
	require.NoError(t, err)
	require.False(t, write.StaleIdentity)
	require.True(t, write.CanonicalUpdated)
	record, err := repo.GetExact(ctx, key)
	require.NoError(t, err)
	require.True(t, record.Supported)
	require.Empty(t, record.ProbeClaimedBy)
	require.Nil(t, record.ProbeClaimedUntil)
	require.Equal(t, outboxBefore+1, countOpenAINativeCompactionAccountOutbox(t, ctx, accountID), "canonical update and account_changed must become visible together")

	secondNow := now.Add(2 * time.Hour)
	claims, err = repo.ClaimDue(ctx, secondNow, secondNow.Add(time.Minute), "inconclusive-worker", 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	write, err = repo.UpsertProbeResult(ctx, service.OpenAINativeCompactionProbeResult{
		Key:                 key,
		ClaimToken:          claims[0].ClaimToken,
		AccountRevision:     claims[0].AccountRevision,
		Supported:           nil,
		SemanticOutcome:     service.OpenAINativeCompactionTransportFailure,
		CheckedAt:           secondNow,
		LastSemanticFailure: "transport_failure",
	})
	require.NoError(t, err)
	require.True(t, write.CanonicalUpdated)
	record, err = repo.GetExact(ctx, key)
	require.NoError(t, err)
	require.True(t, record.Supported, "nil Supported must preserve the prior supported bit")
	require.NotNil(t, record.CheckedAt)
	require.WithinDuration(t, now, *record.CheckedAt, time.Second, "an inconclusive attempt must not refresh the last conclusive capability check")
}

func openAINativeCompactionIntegrationTimePointer(value time.Time) *time.Time {
	return &value
}
