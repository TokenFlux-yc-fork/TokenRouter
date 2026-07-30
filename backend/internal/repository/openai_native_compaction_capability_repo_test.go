package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func nativeCompactionTestKey() service.OpenAINativeCompactionCapabilityKey {
	return service.OpenAINativeCompactionCapabilityKey{
		AccountID:           17,
		UpstreamFingerprint: "upstream_v1_test",
		EffectiveModel:      "gpt-5",
		ContractVersion:     service.OpenAINativeCompactionContractVersion,
	}
}

func TestOpenAINativeCompactionCapabilityRepositoryGetExactUsesFullKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)FROM openai_native_compaction_capabilities.*account_id = \$1.*upstream_fingerprint = \$2.*effective_model = \$3.*contract_version = \$4`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "upstream_fingerprint", "effective_model", "contract_version",
			"supported", "mode", "source", "checked_at", "last_status", "last_semantic_failure",
			"quarantined_until", "override_actor", "override_reason", "override_created_at",
			"override_expires_at", "override_revoked_at", "next_probe_at", "probe_claimed_until",
			"probe_claimed_by", "created_at", "updated_at",
		}).AddRow(9, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			true, "auto", "probe", now, 200, "", nil, "", "", nil, nil, nil, now, nil, "", now, now))

	record, err := NewOpenAINativeCompactionCapabilityRepository(db).GetExact(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, key, record.Key)
	require.True(t, record.Supported)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionUpsertTrustedOfficialRejectsUntrustedAccountsBeforeSQL(t *testing.T) {
	officialFingerprint, err := service.OfficialOpenAINativeCompactionFingerprint()
	require.NoError(t, err)
	key := nativeCompactionTestKey()
	key.UpstreamFingerprint = officialFingerprint

	tests := []struct {
		name    string
		account *service.Account
		key     service.OpenAINativeCompactionCapabilityKey
	}{
		{name: "API key", account: &service.Account{ID: key.AccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}, key: key},
		{name: "custom OAuth fingerprint", account: &service.Account{ID: key.AccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}, key: func() service.OpenAINativeCompactionCapabilityKey {
			custom := key
			custom.UpstreamFingerprint = "upstream_v1_custom"
			return custom
		}()},
		{name: "other provider OAuth", account: &service.Account{ID: key.AccountID, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}, key: key},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			updated, err := NewOpenAINativeCompactionCapabilityRepository(db).UpsertTrustedOfficial(context.Background(), test.account, test.key)
			require.ErrorContains(t, err, "exact official OpenAI OAuth identity")
			require.False(t, updated)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAINativeCompactionUpsertTrustedOfficialAllowsExactOfficialOAuth(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	officialFingerprint, err := service.OfficialOpenAINativeCompactionFingerprint()
	require.NoError(t, err)
	key := nativeCompactionTestKey()
	key.UpstreamFingerprint = officialFingerprint
	account := &service.Account{ID: key.AccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)INSERT INTO openai_native_compaction_capabilities.*FROM accounts a.*a\.platform = \$7.*a\.type = \$8.*source <> \$9`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceTrustedOfficial,
			service.PlatformOpenAI, service.AccountTypeOAuth,
			service.OpenAINativeCompactionCapabilitySourceManualOverride).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	updated, err := NewOpenAINativeCompactionCapabilityRepository(db).UpsertTrustedOfficial(context.Background(), account, key)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionEnsureAutoCandidateCreatesAndPublishesAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	dueAt := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO openai_native_compaction_capabilities.*supported, mode, source, checked_at, next_probe_at.*ON CONFLICT \(account_id, upstream_fingerprint, effective_model, contract_version\).*DO UPDATE SET.*RETURNING id`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe, dueAt,
			service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(91))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	created, err := NewOpenAINativeCompactionCapabilityRepository(db).EnsureAutoCandidate(context.Background(), key, dueAt)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionEnsureAutoCandidatePreservesExistingExactKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	dueAt := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO openai_native_compaction_capabilities.*ON CONFLICT \(account_id, upstream_fingerprint, effective_model, contract_version\).*DO UPDATE SET.*RETURNING id`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe, dueAt,
			service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	created, err := NewOpenAINativeCompactionCapabilityRepository(db).EnsureAutoCandidate(context.Background(), key, dueAt)
	require.NoError(t, err)
	require.False(t, created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionEnsureAutoCandidateRollsBackOnOutboxError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	dueAt := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO openai_native_compaction_capabilities.*ON CONFLICT \(account_id, upstream_fingerprint, effective_model, contract_version\).*DO UPDATE SET.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(91))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	created, err := NewOpenAINativeCompactionCapabilityRepository(db).EnsureAutoCandidate(context.Background(), key, dueAt)
	require.EqualError(t, err, "outbox failed")
	require.False(t, created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionEnsureAutoCandidateValidatesBeforeTransaction(t *testing.T) {
	tests := []struct {
		name  string
		key   service.OpenAINativeCompactionCapabilityKey
		dueAt time.Time
	}{
		{name: "invalid key", key: service.OpenAINativeCompactionCapabilityKey{}, dueAt: time.Now().UTC()},
		{name: "zero due time", key: nativeCompactionTestKey()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			created, err := NewOpenAINativeCompactionCapabilityRepository(db).EnsureAutoCandidate(context.Background(), test.key, test.dueAt)
			require.Error(t, err)
			require.False(t, created)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAINativeCompactionUpsertProbeResultAuditsStaleIdentityOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT c.id, c.mode, c.probe_claimed_by.*FOR UPDATE OF c, a`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mode", "probe_claimed_by", "claim_live", "proxy_id", "account_xmin", "credential_hash"}).
			AddRow(9, "auto", "worker:new-token", true, nil, "123", "credential-hash"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO openai_native_compaction_probe_results")).
		WithArgs(int64(9), key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			sqlmock.AnyArg(), service.OpenAINativeCompactionValid, nil, true, now, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
	mock.ExpectCommit()

	supported := true
	got, err := NewOpenAINativeCompactionCapabilityRepository(db).UpsertProbeResult(context.Background(), service.OpenAINativeCompactionProbeResult{
		Key: key, ClaimToken: "worker:old-token", AccountRevision: "old-revision",
		Supported: &supported, SemanticOutcome: service.OpenAINativeCompactionValid, CheckedAt: now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(41), got.AuditID)
	require.True(t, got.StaleIdentity)
	require.False(t, got.CanonicalUpdated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionUpsertProbeResultCanonicalAndOutboxAreAtomic(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	now := time.Now().UTC()
	next := now.Add(time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT c.id, c.mode, c.probe_claimed_by.*FOR UPDATE OF c, a`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mode", "probe_claimed_by", "claim_live", "proxy_id", "account_xmin", "credential_hash"}).
			AddRow(9, "auto", "worker:token", true, nil, "same-revision", "credential-hash"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO openai_native_compaction_probe_results")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	mock.ExpectExec(`(?s)UPDATE openai_native_compaction_capabilities.*checked_at = CASE WHEN \$1 IS NULL THEN checked_at ELSE \$3 END.*mode = \$9.*probe_claimed_by = \$10.*probe_claimed_until >= NOW\(\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	supported := true
	_, err = NewOpenAINativeCompactionCapabilityRepository(db).UpsertProbeResult(context.Background(), service.OpenAINativeCompactionProbeResult{
		Key: key, ClaimToken: "worker:token", AccountRevision: "same-revision::credential-hash",
		Supported: &supported, SemanticOutcome: service.OpenAINativeCompactionValid,
		CheckedAt: now, NextProbeAt: &next,
	})
	require.EqualError(t, err, "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionClaimDueUsesLeaseAndTokenFence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	until := now.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH expired AS.*override_expires_at <= \$4.*FOR UPDATE SKIP LOCKED.*override_actor = ''.*next_probe_at = \$4.*SELECT DISTINCT account_id FROM reconciled`).
		WithArgs(service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride, now,
			service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectQuery(`(?s)WITH due AS.*mode = \$1.*next_probe_at <= \$2.*probe_claimed_until.*FOR UPDATE OF c, a SKIP LOCKED.*probe_claimed_by = \$5.*account_revision`).
		WithArgs(service.OpenAINativeCompactionCapabilityModeAuto, now, 10, until, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "upstream_fingerprint", "effective_model", "contract_version",
			"supported", "mode", "source", "checked_at", "last_status", "last_semantic_failure",
			"quarantined_until", "override_actor", "override_reason", "override_created_at",
			"override_expires_at", "override_revoked_at", "next_probe_at", "probe_claimed_until",
			"probe_claimed_by", "created_at", "updated_at", "account_revision",
		}))
	mock.ExpectCommit()

	claims, err := NewOpenAINativeCompactionCapabilityRepository(db).ClaimDue(context.Background(), now, until, "worker-1", 10)
	require.NoError(t, err)
	require.Empty(t, claims)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionClaimDueReconcilesExpiredOverridePublishesAndClaims(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	now := time.Now().UTC()
	until := now.Add(time.Minute)
	createdAt := now.Add(-time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH expired AS.*FOR UPDATE SKIP LOCKED.*SELECT DISTINCT account_id FROM reconciled`).
		WithArgs(service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride, now,
			service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(key.AccountID))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`(?s)WITH due AS.*next_probe_at <= \$2.*FOR UPDATE OF c, a SKIP LOCKED.*probe_claimed_by = \$5`).
		WithArgs(service.OpenAINativeCompactionCapabilityModeAuto, now, 1, until, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "upstream_fingerprint", "effective_model", "contract_version",
			"supported", "mode", "source", "checked_at", "last_status", "last_semantic_failure",
			"quarantined_until", "override_actor", "override_reason", "override_created_at",
			"override_expires_at", "override_revoked_at", "next_probe_at", "probe_claimed_until",
			"probe_claimed_by", "created_at", "updated_at", "account_revision",
		}).AddRow(9, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			true, service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe, nil, nil, "", nil,
			"", "", nil, nil, nil, now, until, "worker-1:token", createdAt, now, "revision"))
	mock.ExpectCommit()

	claims, err := NewOpenAINativeCompactionCapabilityRepository(db).ClaimDue(context.Background(), now, until, "worker-1", 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, key, claims[0].Capability.Key)
	require.Equal(t, service.OpenAINativeCompactionCapabilityModeAuto, claims[0].Capability.Mode)
	require.Equal(t, service.OpenAINativeCompactionCapabilitySourceProbe, claims[0].Capability.Source)
	require.Nil(t, claims[0].Capability.CheckedAt)
	require.Equal(t, "revision", claims[0].AccountRevision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionUpsertManualOverrideRequiresBoundedAuditedMetadata(t *testing.T) {
	createdAt := time.Now().UTC()
	expiresAt := createdAt.Add(time.Hour)
	tests := []struct {
		name     string
		override service.OpenAINativeCompactionManualOverride
		wantErr  string
	}{
		{
			name: "empty actor",
			override: service.OpenAINativeCompactionManualOverride{Key: nativeCompactionTestKey(), Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
				Reason: "incident", CreatedAt: createdAt, ExpiresAt: &expiresAt},
			wantErr: "actor is empty",
		},
		{
			name: "empty reason",
			override: service.OpenAINativeCompactionManualOverride{Key: nativeCompactionTestKey(), Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
				Actor: "operator", CreatedAt: createdAt, ExpiresAt: &expiresAt},
			wantErr: "reason is empty",
		},
		{
			name: "permanent override",
			override: service.OpenAINativeCompactionManualOverride{Key: nativeCompactionTestKey(), Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
				Actor: "operator", Reason: "incident", CreatedAt: createdAt},
			wantErr: "expires_at is required",
		},
		{
			name: "nonpositive lifetime",
			override: service.OpenAINativeCompactionManualOverride{Key: nativeCompactionTestKey(), Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
				Actor: "operator", Reason: "incident", CreatedAt: createdAt, ExpiresAt: &createdAt},
			wantErr: "expires_at must be after created_at",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			err = NewOpenAINativeCompactionCapabilityRepository(db).UpsertManualOverride(context.Background(), test.override)
			require.ErrorContains(t, err, test.wantErr)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAINativeCompactionUpsertManualOverrideAuditsAndPublishesAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	createdAt := time.Now().UTC()
	expiresAt := createdAt.Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO openai_native_compaction_capabilities.*override_expires_at.*ON CONFLICT.*RETURNING id`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilitySourceManualOverride,
			"operator", "incident mitigation", createdAt, expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(91))
	mock.ExpectExec(`(?s)INSERT INTO openai_native_compaction_override_audits.*'set'`).
		WithArgs(int64(91), key.AccountID, key.UpstreamFingerprint, key.EffectiveModel,
			key.ContractVersion, service.OpenAINativeCompactionCapabilityModeForceOn,
			"operator", "incident mitigation", createdAt, expiresAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = NewOpenAINativeCompactionCapabilityRepository(db).UpsertManualOverride(context.Background(), service.OpenAINativeCompactionManualOverride{
		Key: key, Mode: service.OpenAINativeCompactionCapabilityModeForceOn,
		Actor: " operator ", Reason: " incident mitigation ", CreatedAt: createdAt, ExpiresAt: &expiresAt,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionRevokeManualOverrideAuditsThenReturnsToAuto(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	revokedAt := time.Now().UTC()
	nextProbeAt := revokedAt.Add(time.Second)
	createdAt := revokedAt.Add(-time.Hour)
	expiresAt := revokedAt.Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, mode, override_actor, override_reason,.*override_created_at, override_expires_at.*FOR UPDATE`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
			service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mode", "override_actor", "override_reason", "override_created_at", "override_expires_at"}).
			AddRow(91, service.OpenAINativeCompactionCapabilityModeForceOff, "operator", "incident mitigation", createdAt, expiresAt))
	mock.ExpectExec(`(?s)INSERT INTO openai_native_compaction_override_audits.*'revoke'`).
		WithArgs(int64(91), key.AccountID, key.UpstreamFingerprint, key.EffectiveModel,
			key.ContractVersion, service.OpenAINativeCompactionCapabilityModeForceOff,
			"operator", "incident mitigation", createdAt, expiresAt, revokedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE openai_native_compaction_capabilities.*mode = \$2.*source = \$3.*override_actor = ''.*override_reason = ''.*override_created_at = NULL.*override_expires_at = NULL.*override_revoked_at = NULL.*next_probe_at = \$4`).
		WithArgs(int64(91), service.OpenAINativeCompactionCapabilityModeAuto,
			service.OpenAINativeCompactionCapabilitySourceProbe, nextProbeAt,
			service.OpenAINativeCompactionCapabilityModeForceOn,
			service.OpenAINativeCompactionCapabilityModeForceOff,
			service.OpenAINativeCompactionCapabilitySourceManualOverride).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	updated, err := NewOpenAINativeCompactionCapabilityRepository(db).RevokeManualOverride(
		context.Background(), key, revokedAt, nextProbeAt,
	)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeCannotOverwriteManualOverride(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	key := nativeCompactionTestKey()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT c.id, c.mode, c.probe_claimed_by`).
		WithArgs(key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion).
		WillReturnRows(sqlmock.NewRows([]string{"id", "mode", "probe_claimed_by", "claim_live", "proxy_id", "account_xmin", "credential_hash"}).
			AddRow(9, service.OpenAINativeCompactionCapabilityModeForceOn, "worker:token", true, nil, "same-revision", "credential-hash"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO openai_native_compaction_probe_results")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(43))
	mock.ExpectCommit()

	supported := false
	got, err := NewOpenAINativeCompactionCapabilityRepository(db).UpsertProbeResult(context.Background(), service.OpenAINativeCompactionProbeResult{
		Key: key, ClaimToken: "worker:token", AccountRevision: "same-revision::credential-hash",
		Supported: &supported, SemanticOutcome: service.OpenAINativeCompactionZeroCompaction, CheckedAt: now,
	})
	require.NoError(t, err)
	require.True(t, got.StaleIdentity)
	require.False(t, got.CanonicalUpdated)
	require.NoError(t, mock.ExpectationsWereMet())
}
