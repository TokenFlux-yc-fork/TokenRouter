package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
)

type openAINativeCompactionCapabilityRepository struct {
	db *sql.DB
}

var _ service.OpenAINativeCompactionCapabilityRepository = (*openAINativeCompactionCapabilityRepository)(nil)

func NewOpenAINativeCompactionCapabilityRepository(db *sql.DB) service.OpenAINativeCompactionCapabilityRepository {
	return &openAINativeCompactionCapabilityRepository{db: db}
}

func (r *openAINativeCompactionCapabilityRepository) GetExact(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey) (*service.OpenAINativeCompactionCapabilityRecord, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, upstream_fingerprint, effective_model, contract_version,
			supported, mode, source, checked_at, last_status,
			last_semantic_failure, quarantined_until, override_actor, override_reason,
			override_created_at, override_expires_at, override_revoked_at,
			next_probe_at, probe_claimed_until, probe_claimed_by, created_at, updated_at
		FROM openai_native_compaction_capabilities
		WHERE account_id = $1
		  AND upstream_fingerprint = $2
		  AND effective_model = $3
		  AND contract_version = $4
	`, capabilityKeyArgs(key)...)
	record, err := scanOpenAINativeCompactionCapabilityRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (r *openAINativeCompactionCapabilityRepository) EnsureAutoCandidate(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey, dueAt time.Time) (bool, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return false, err
	}
	if dueAt.IsZero() {
		return false, errors.New("openai native compaction candidate due_at is zero")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var capabilityID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO openai_native_compaction_capabilities (
			account_id, upstream_fingerprint, effective_model, contract_version,
			supported, mode, source, checked_at, next_probe_at,
			probe_claimed_until, probe_claimed_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, FALSE, $5, $6, NULL, $7, NULL, '', NOW(), NOW())
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version)
		DO UPDATE SET
			mode = EXCLUDED.mode,
			source = EXCLUDED.source,
			checked_at = NULL,
			override_actor = '',
			override_reason = '',
			override_created_at = NULL,
			override_expires_at = NULL,
			override_revoked_at = NULL,
			next_probe_at = EXCLUDED.next_probe_at,
			probe_claimed_until = NULL,
			probe_claimed_by = '',
			updated_at = NOW()
		WHERE openai_native_compaction_capabilities.mode IN ($8, $9)
		  AND openai_native_compaction_capabilities.source = $10
		  AND openai_native_compaction_capabilities.override_revoked_at IS NULL
		  AND openai_native_compaction_capabilities.override_expires_at IS NOT NULL
		  AND openai_native_compaction_capabilities.override_expires_at <= $7
		  AND (openai_native_compaction_capabilities.quarantined_until IS NULL
		       OR openai_native_compaction_capabilities.quarantined_until <= $7)
		  AND (openai_native_compaction_capabilities.probe_claimed_until IS NULL
		       OR openai_native_compaction_capabilities.probe_claimed_until <= $7)
		RETURNING id
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
		service.OpenAINativeCompactionCapabilityModeAuto,
		service.OpenAINativeCompactionCapabilitySourceProbe, dueAt,
		service.OpenAINativeCompactionCapabilityModeForceOn,
		service.OpenAINativeCompactionCapabilityModeForceOff,
		service.OpenAINativeCompactionCapabilitySourceManualOverride,
	).Scan(&capabilityID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &key.AccountID, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *openAINativeCompactionCapabilityRepository) UpsertTrustedOfficial(
	ctx context.Context,
	account *service.Account,
	key service.OpenAINativeCompactionCapabilityKey,
) (bool, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return false, err
	}
	if account == nil || account.ID != key.AccountID || !account.TrustedOfficialOpenAINativeCompactionKey(key) {
		return false, errors.New("trusted official native compaction requires exact official OpenAI OAuth identity")
	}
	return r.upsertTrustedOfficial(ctx, key)
}

func (r *openAINativeCompactionCapabilityRepository) upsertTrustedOfficial(
	ctx context.Context,
	key service.OpenAINativeCompactionCapabilityKey,
) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO openai_native_compaction_capabilities (
			account_id, upstream_fingerprint, effective_model, contract_version,
			supported, mode, source, checked_at, next_probe_at,
			probe_claimed_until, probe_claimed_by, created_at, updated_at
		)
		SELECT a.id, $2, $3, $4, TRUE, $5, $6, NULL, NULL, NULL, '', NOW(), NOW()
		FROM accounts a
		WHERE a.id = $1
		  AND a.deleted_at IS NULL
		  AND a.platform = $7
		  AND a.type = $8
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version)
		DO UPDATE SET
			supported = TRUE,
			mode = EXCLUDED.mode,
			source = EXCLUDED.source,
			checked_at = NULL,
			next_probe_at = NULL,
			probe_claimed_until = NULL,
			probe_claimed_by = '',
			updated_at = NOW()
		WHERE openai_native_compaction_capabilities.mode = $5
		  AND openai_native_compaction_capabilities.source <> $9
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
		service.OpenAINativeCompactionCapabilityModeAuto,
		service.OpenAINativeCompactionCapabilitySourceTrustedOfficial,
		service.PlatformOpenAI, service.AccountTypeOAuth,
		service.OpenAINativeCompactionCapabilitySourceManualOverride,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if affected != 1 {
		return false, fmt.Errorf("trusted official native compaction mutation affected %d rows", affected)
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &key.AccountID, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *openAINativeCompactionCapabilityRepository) UpsertProbeResult(ctx context.Context, result service.OpenAINativeCompactionProbeResult) (service.OpenAINativeCompactionProbeWriteResult, error) {
	var out service.OpenAINativeCompactionProbeWriteResult
	if err := validateOpenAINativeCompactionKey(result.Key); err != nil {
		return out, err
	}
	if strings.TrimSpace(result.ClaimToken) == "" {
		return out, errors.New("openai native compaction probe claim token is empty")
	}
	if strings.TrimSpace(result.AccountRevision) == "" {
		return out, errors.New("openai native compaction probe account revision is empty")
	}
	if strings.TrimSpace(string(result.SemanticOutcome)) == "" {
		return out, errors.New("openai native compaction semantic outcome is empty")
	}
	if result.CheckedAt.IsZero() {
		return out, errors.New("openai native compaction checked_at is zero")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		capabilityID   int64
		mode           string
		claimToken     string
		claimLive      bool
		proxyID        sql.NullInt64
		accountXmin    string
		credentialHash string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT c.id, c.mode, c.probe_claimed_by,
			COALESCE(c.probe_claimed_until >= NOW(), FALSE),
			a.proxy_id, a.xmin::text,
			encode(sha256(convert_to(a.credentials::text, 'UTF8')), 'hex')
		FROM openai_native_compaction_capabilities c
		JOIN accounts a ON a.id = c.account_id AND a.deleted_at IS NULL
		WHERE c.account_id = $1
		  AND c.upstream_fingerprint = $2
		  AND c.effective_model = $3
		  AND c.contract_version = $4
		FOR UPDATE OF c, a
	`, capabilityKeyArgs(result.Key)...).Scan(
		&capabilityID, &mode, &claimToken, &claimLive, &proxyID, &accountXmin, &credentialHash,
	)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	accountRevision := accountXmin + ":"
	if proxyID.Valid {
		var proxyXmin string
		if err == nil {
			err = tx.QueryRowContext(ctx, `
				SELECT xmin::text
				FROM proxies
				WHERE id = $1 AND deleted_at IS NULL
				FOR SHARE
			`, proxyID.Int64).Scan(&proxyXmin)
		}
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			err = nil
		}
		if err != nil {
			return out, err
		}
		accountRevision += proxyXmin
	}
	accountRevision += ":" + credentialHash
	accountMatches := accountRevision == result.AccountRevision
	claimMatches := found && mode == service.OpenAINativeCompactionCapabilityModeAuto &&
		claimToken == result.ClaimToken && claimLive && accountMatches
	var capabilityIDArg any
	if found {
		capabilityIDArg = capabilityID
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO openai_native_compaction_probe_results (
			capability_id, account_id, upstream_fingerprint, effective_model,
			contract_version, supported, semantic_outcome, status_code,
			stale_identity, checked_at, authorization_principal_sha256
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id
	`, capabilityIDArg, result.Key.AccountID, result.Key.UpstreamFingerprint,
		result.Key.EffectiveModel, result.Key.ContractVersion, result.Supported,
		result.SemanticOutcome, result.StatusCode, !claimMatches, result.CheckedAt,
		nullIfEmpty(result.AuthorizationPrincipalSHA256),
	).Scan(&out.AuditID)
	if err != nil {
		return out, err
	}
	if !claimMatches {
		out.StaleIdentity = true
		if err := tx.Commit(); err != nil {
			return service.OpenAINativeCompactionProbeWriteResult{}, err
		}
		return out, nil
	}

	update, err := tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_capabilities
		SET supported = COALESCE($1, supported),
			source = $2,
			checked_at = CASE WHEN $1 IS NULL THEN checked_at ELSE $3 END,
			last_status = $4,
			last_semantic_failure = $5,
			quarantined_until = $6,
			next_probe_at = $7,
			probe_claimed_until = NULL,
			probe_claimed_by = '',
			updated_at = NOW()
		WHERE id = $8
		  AND mode = $9
		  AND probe_claimed_by = $10
		  AND probe_claimed_until >= NOW()
	`, result.Supported, service.OpenAINativeCompactionCapabilitySourceProbe,
		result.CheckedAt, result.StatusCode, result.LastSemanticFailure,
		result.QuarantinedUntil, result.NextProbeAt, capabilityID,
		service.OpenAINativeCompactionCapabilityModeAuto, result.ClaimToken,
	)
	if err != nil {
		return out, err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return out, err
	}
	if affected != 1 {
		return out, service.ErrOpenAINativeCompactionProbeClaimLost
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &result.Key.AccountID, nil, nil); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return service.OpenAINativeCompactionProbeWriteResult{}, err
	}
	out.CanonicalUpdated = true
	return out, nil
}

func (r *openAINativeCompactionCapabilityRepository) SetQuarantine(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey, until *time.Time, semanticFailure string) (bool, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return false, err
	}
	return r.mutateExact(ctx, key, `
		UPDATE openai_native_compaction_capabilities
		SET quarantined_until = $5,
			last_semantic_failure = $6,
			updated_at = NOW()
		WHERE account_id = $1
		  AND upstream_fingerprint = $2
		  AND effective_model = $3
		  AND contract_version = $4
	`, until, semanticFailure)
}

func (r *openAINativeCompactionCapabilityRepository) ClaimDue(ctx context.Context, now, claimUntil time.Time, workerID string, limit int) ([]service.OpenAINativeCompactionProbeClaim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, errors.New("openai native compaction probe worker id is empty")
	}
	if !claimUntil.After(now) {
		return nil, errors.New("openai native compaction probe claim deadline must be after now")
	}
	if limit <= 0 {
		return []service.OpenAINativeCompactionProbeClaim{}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	expiredRows, err := tx.QueryContext(ctx, `
		WITH expired AS (
			SELECT c.id
			FROM openai_native_compaction_capabilities c
			WHERE c.mode IN ($1, $2)
			  AND c.source = $3
			  AND c.override_revoked_at IS NULL
			  AND c.override_expires_at IS NOT NULL
			  AND c.override_expires_at <= $4
			  AND (c.quarantined_until IS NULL OR c.quarantined_until <= $4)
			  AND (c.probe_claimed_until IS NULL OR c.probe_claimed_until <= $4)
			FOR UPDATE SKIP LOCKED
		), reconciled AS (
			UPDATE openai_native_compaction_capabilities c
			SET mode = $5,
				source = $6,
				checked_at = NULL,
				override_actor = '',
				override_reason = '',
				override_created_at = NULL,
				override_expires_at = NULL,
				override_revoked_at = NULL,
				next_probe_at = $4,
				probe_claimed_until = NULL,
				probe_claimed_by = '',
				updated_at = NOW()
			FROM expired
			WHERE c.id = expired.id
			RETURNING c.account_id
		)
		SELECT DISTINCT account_id FROM reconciled
	`, service.OpenAINativeCompactionCapabilityModeForceOn,
		service.OpenAINativeCompactionCapabilityModeForceOff,
		service.OpenAINativeCompactionCapabilitySourceManualOverride, now,
		service.OpenAINativeCompactionCapabilityModeAuto,
		service.OpenAINativeCompactionCapabilitySourceProbe,
	)
	if err != nil {
		return nil, err
	}
	reconciledAccountIDs := make([]int64, 0)
	for expiredRows.Next() {
		var accountID int64
		if err := expiredRows.Scan(&accountID); err != nil {
			_ = expiredRows.Close()
			return nil, err
		}
		reconciledAccountIDs = append(reconciledAccountIDs, accountID)
	}
	if err := expiredRows.Err(); err != nil {
		_ = expiredRows.Close()
		return nil, err
	}
	if err := expiredRows.Close(); err != nil {
		return nil, err
	}
	for _, accountID := range reconciledAccountIDs {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			return nil, err
		}
	}

	claimToken := workerID + ":" + uuid.NewString()
	rows, err := tx.QueryContext(ctx, `
		WITH due AS (
			SELECT c.id
			FROM openai_native_compaction_capabilities c
			JOIN accounts a ON a.id = c.account_id AND a.deleted_at IS NULL
			LEFT JOIN proxies p ON p.id = a.proxy_id AND p.deleted_at IS NULL
			WHERE c.mode = $1
			  AND a.parent_account_id IS NULL
			  AND c.next_probe_at IS NOT NULL
			  AND c.next_probe_at <= $2
			  AND (c.probe_claimed_until IS NULL OR c.probe_claimed_until <= $2)
			ORDER BY c.next_probe_at ASC, c.id ASC
			LIMIT $3
			FOR UPDATE OF c, a SKIP LOCKED
		), claimed AS (
			UPDATE openai_native_compaction_capabilities AS c
			SET probe_claimed_until = $4,
				probe_claimed_by = $5,
				updated_at = NOW()
			FROM due
			WHERE c.id = due.id
			RETURNING c.*
		)
		SELECT claimed.id, claimed.account_id, claimed.upstream_fingerprint,
			claimed.effective_model, claimed.contract_version, claimed.supported,
			claimed.mode, claimed.source, claimed.checked_at, claimed.last_status,
			claimed.last_semantic_failure, claimed.quarantined_until,
			claimed.override_actor, claimed.override_reason, claimed.override_created_at,
			claimed.override_expires_at, claimed.override_revoked_at,
			claimed.next_probe_at, claimed.probe_claimed_until, claimed.probe_claimed_by,
			claimed.created_at, claimed.updated_at,
			a.xmin::text || ':' || COALESCE((
				SELECT p.xmin::text
				FROM proxies p
				WHERE p.id = a.proxy_id AND p.deleted_at IS NULL
				FOR SHARE
			), '') || ':' || encode(sha256(convert_to(a.credentials::text, 'UTF8')), 'hex') AS account_revision
		FROM claimed
		JOIN accounts a ON a.id = claimed.account_id AND a.deleted_at IS NULL
		ORDER BY claimed.next_probe_at ASC, claimed.id ASC
	`, service.OpenAINativeCompactionCapabilityModeAuto, now, limit, claimUntil, claimToken)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	claims := make([]service.OpenAINativeCompactionProbeClaim, 0, limit)
	for rows.Next() {
		var accountRevision string
		record, err := scanOpenAINativeCompactionCapabilityRecordWithExtra(rows, &accountRevision)
		if err != nil {
			return nil, err
		}
		claims = append(claims, service.OpenAINativeCompactionProbeClaim{
			Capability:      *record,
			ClaimToken:      claimToken,
			AccountRevision: accountRevision,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (r *openAINativeCompactionCapabilityRepository) FenceProbeDispatch(
	ctx context.Context,
	claim service.OpenAINativeCompactionProbeClaim,
) error {
	if err := validateOpenAINativeCompactionKey(claim.Capability.Key); err != nil {
		return err
	}
	if strings.TrimSpace(claim.ClaimToken) == "" || strings.TrimSpace(claim.AccountRevision) == "" {
		return service.ErrOpenAINativeCompactionProbeClaimLost
	}
	var live bool
	err := r.db.QueryRowContext(ctx, `
		SELECT c.mode = $5
			AND c.probe_claimed_by = $6
			AND c.probe_claimed_until >= NOW()
			AND a.parent_account_id IS NULL
			AND a.xmin::text || ':' || COALESCE((
				SELECT p.xmin::text
				FROM proxies p
				WHERE p.id = a.proxy_id AND p.deleted_at IS NULL
			), '') || ':' || encode(sha256(convert_to(a.credentials::text, 'UTF8')), 'hex') = $7
		FROM openai_native_compaction_capabilities c
		JOIN accounts a ON a.id = c.account_id AND a.deleted_at IS NULL
		WHERE c.account_id = $1
		  AND c.upstream_fingerprint = $2
		  AND c.effective_model = $3
		  AND c.contract_version = $4
	`, claim.Capability.Key.AccountID, claim.Capability.Key.UpstreamFingerprint,
		claim.Capability.Key.EffectiveModel, claim.Capability.Key.ContractVersion,
		service.OpenAINativeCompactionCapabilityModeAuto, claim.ClaimToken,
		claim.AccountRevision).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !live {
		return service.ErrOpenAINativeCompactionProbeClaimLost
	}
	return err
}

func (r *openAINativeCompactionCapabilityRepository) UpsertManualOverride(ctx context.Context, override service.OpenAINativeCompactionManualOverride) error {
	if err := validateOpenAINativeCompactionKey(override.Key); err != nil {
		return err
	}
	if override.Mode != service.OpenAINativeCompactionCapabilityModeForceOn && override.Mode != service.OpenAINativeCompactionCapabilityModeForceOff {
		return errors.New("openai native compaction manual override mode must be force_on or force_off")
	}
	override.Actor = strings.TrimSpace(override.Actor)
	override.Reason = strings.TrimSpace(override.Reason)
	if override.Actor == "" {
		return errors.New("openai native compaction manual override actor is empty")
	}
	if override.Reason == "" {
		return errors.New("openai native compaction manual override reason is empty")
	}
	if override.CreatedAt.IsZero() {
		return errors.New("openai native compaction manual override created_at is zero")
	}
	if override.ExpiresAt == nil || override.ExpiresAt.IsZero() {
		return errors.New("openai native compaction manual override expires_at is required")
	}
	if !override.ExpiresAt.After(override.CreatedAt) {
		return errors.New("openai native compaction manual override expires_at must be after created_at")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var capabilityID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO openai_native_compaction_capabilities (
			account_id, upstream_fingerprint, effective_model, contract_version,
			supported, mode, source, override_actor, override_reason,
			override_created_at, override_expires_at, override_revoked_at,
			next_probe_at, probe_claimed_until, probe_claimed_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, FALSE, $5, $6, $7, $8, $9, $10, NULL, NULL, NULL, '', NOW(), NOW())
		ON CONFLICT (account_id, upstream_fingerprint, effective_model, contract_version)
		DO UPDATE SET
			mode = EXCLUDED.mode,
			source = EXCLUDED.source,
			override_actor = EXCLUDED.override_actor,
			override_reason = EXCLUDED.override_reason,
			override_created_at = EXCLUDED.override_created_at,
			override_expires_at = EXCLUDED.override_expires_at,
			override_revoked_at = NULL,
			next_probe_at = NULL,
			probe_claimed_until = NULL,
			probe_claimed_by = '',
			updated_at = NOW()
		RETURNING id
	`, override.Key.AccountID, override.Key.UpstreamFingerprint, override.Key.EffectiveModel,
		override.Key.ContractVersion, override.Mode,
		service.OpenAINativeCompactionCapabilitySourceManualOverride,
		override.Actor, override.Reason, override.CreatedAt, override.ExpiresAt).Scan(&capabilityID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO openai_native_compaction_override_audits (
			capability_id, account_id, upstream_fingerprint, effective_model,
			contract_version, action, mode, override_actor, override_reason,
			override_created_at, override_expires_at, override_revoked_at
		)
		VALUES ($1, $2, $3, $4, $5, 'set', $6, $7, $8, $9, $10, NULL)
	`, capabilityID, override.Key.AccountID, override.Key.UpstreamFingerprint,
		override.Key.EffectiveModel, override.Key.ContractVersion, override.Mode,
		override.Actor, override.Reason, override.CreatedAt, override.ExpiresAt)
	if err != nil {
		return err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &override.Key.AccountID, nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *openAINativeCompactionCapabilityRepository) RevokeManualOverride(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey, revokedAt, nextProbeAt time.Time) (bool, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return false, err
	}
	if revokedAt.IsZero() || nextProbeAt.IsZero() {
		return false, errors.New("openai native compaction override revocation timestamps must be non-zero")
	}
	if nextProbeAt.Before(revokedAt) {
		return false, errors.New("openai native compaction next probe must not precede override revocation")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		capabilityID int64
		mode         string
		actor        string
		reason       string
		createdAt    time.Time
		expiresAt    time.Time
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, mode, override_actor, override_reason,
			override_created_at, override_expires_at
		FROM openai_native_compaction_capabilities
		WHERE account_id = $1
		  AND upstream_fingerprint = $2
		  AND effective_model = $3
		  AND contract_version = $4
		  AND mode IN ($5, $6)
		  AND source = $7
		  AND override_revoked_at IS NULL
		FOR UPDATE
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion,
		service.OpenAINativeCompactionCapabilityModeForceOn,
		service.OpenAINativeCompactionCapabilityModeForceOff,
		service.OpenAINativeCompactionCapabilitySourceManualOverride,
	).Scan(&capabilityID, &mode, &actor, &reason, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revokedAt.Before(createdAt) {
		return false, errors.New("openai native compaction override revocation precedes creation")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO openai_native_compaction_override_audits (
			capability_id, account_id, upstream_fingerprint, effective_model,
			contract_version, action, mode, override_actor, override_reason,
			override_created_at, override_expires_at, override_revoked_at
		)
		VALUES ($1, $2, $3, $4, $5, 'revoke', $6, $7, $8, $9, $10, $11)
	`, capabilityID, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel,
		key.ContractVersion, mode, actor, reason, createdAt, expiresAt, revokedAt)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_capabilities
		SET mode = $2,
			source = $3,
			checked_at = NULL,
			override_actor = '',
			override_reason = '',
			override_created_at = NULL,
			override_expires_at = NULL,
			override_revoked_at = NULL,
			next_probe_at = $4,
			probe_claimed_until = NULL,
			probe_claimed_by = '',
			updated_at = NOW()
		WHERE id = $1
		  AND mode IN ($5, $6)
		  AND source = $7
		  AND override_revoked_at IS NULL
	`, capabilityID, service.OpenAINativeCompactionCapabilityModeAuto,
		service.OpenAINativeCompactionCapabilitySourceProbe, nextProbeAt,
		service.OpenAINativeCompactionCapabilityModeForceOn,
		service.OpenAINativeCompactionCapabilityModeForceOff,
		service.OpenAINativeCompactionCapabilitySourceManualOverride)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, fmt.Errorf("openai native compaction override revocation affected %d rows", affected)
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &key.AccountID, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *openAINativeCompactionCapabilityRepository) ListAudit(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey, limit int) ([]service.OpenAINativeCompactionProbeAudit, error) {
	if err := validateOpenAINativeCompactionKey(key); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []service.OpenAINativeCompactionProbeAudit{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, capability_id, account_id, upstream_fingerprint, effective_model,
			contract_version, supported, semantic_outcome, status_code,
			stale_identity, checked_at, authorization_principal_sha256
		FROM openai_native_compaction_probe_results
		WHERE account_id = $1
		  AND upstream_fingerprint = $2
		  AND effective_model = $3
		  AND contract_version = $4
		ORDER BY checked_at DESC, id DESC
		LIMIT $5
	`, key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	audits := make([]service.OpenAINativeCompactionProbeAudit, 0, limit)
	for rows.Next() {
		var (
			audit        service.OpenAINativeCompactionProbeAudit
			capabilityID sql.NullInt64
			supported    sql.NullBool
			statusCode   sql.NullInt64
			fingerprint  string
			outcome      string
			principal    sql.NullString
		)
		if err := rows.Scan(&audit.ID, &capabilityID, &audit.Key.AccountID, &fingerprint,
			&audit.Key.EffectiveModel, &audit.Key.ContractVersion, &supported, &outcome,
			&statusCode, &audit.StaleIdentity, &audit.CheckedAt, &principal); err != nil {
			return nil, err
		}
		audit.Key.UpstreamFingerprint = service.OpenAIUpstreamFingerprint(fingerprint)
		audit.SemanticOutcome = service.OpenAINativeCompactionOutcome(outcome)
		if principal.Valid {
			audit.AuthorizationPrincipalSHA256 = principal.String
		}
		if capabilityID.Valid {
			value := capabilityID.Int64
			audit.CapabilityID = &value
		}
		if supported.Valid {
			value := supported.Bool
			audit.Supported = &value
		}
		if statusCode.Valid {
			value := int(statusCode.Int64)
			audit.StatusCode = &value
		}
		audits = append(audits, audit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return audits, nil
}

func nullIfEmpty(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func (r *openAINativeCompactionCapabilityRepository) mutateExact(ctx context.Context, key service.OpenAINativeCompactionCapabilityKey, query string, extraArgs ...any) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	args := append(capabilityKeyArgs(key), extraArgs...)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if affected != 1 {
		return false, fmt.Errorf("openai native compaction exact mutation affected %d rows", affected)
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &key.AccountID, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func validateOpenAINativeCompactionKey(key service.OpenAINativeCompactionCapabilityKey) error {
	if !key.Valid() {
		return errors.New("invalid openai native compaction capability key")
	}
	return nil
}

func capabilityKeyArgs(key service.OpenAINativeCompactionCapabilityKey) []any {
	return []any{key.AccountID, key.UpstreamFingerprint, key.EffectiveModel, key.ContractVersion}
}

func scanOpenAINativeCompactionCapabilityRecord(row interface{ Scan(...any) error }) (*service.OpenAINativeCompactionCapabilityRecord, error) {
	return scanOpenAINativeCompactionCapabilityRecordWithExtra(row)
}

func scanOpenAINativeCompactionCapabilityRecordWithExtra(row interface{ Scan(...any) error }, extra ...any) (*service.OpenAINativeCompactionCapabilityRecord, error) {
	var (
		record            service.OpenAINativeCompactionCapabilityRecord
		fingerprint       string
		checkedAt         sql.NullTime
		lastStatus        sql.NullInt64
		quarantinedUntil  sql.NullTime
		overrideCreatedAt sql.NullTime
		overrideExpiresAt sql.NullTime
		overrideRevokedAt sql.NullTime
		nextProbeAt       sql.NullTime
		probeClaimedUntil sql.NullTime
	)
	dest := []any{
		&record.ID, &record.Key.AccountID, &fingerprint, &record.Key.EffectiveModel,
		&record.Key.ContractVersion, &record.Supported, &record.Mode, &record.Source,
		&checkedAt, &lastStatus, &record.LastSemanticFailure, &quarantinedUntil,
		&record.OverrideActor, &record.OverrideReason, &overrideCreatedAt,
		&overrideExpiresAt, &overrideRevokedAt, &nextProbeAt, &probeClaimedUntil,
		&record.ProbeClaimedBy, &record.CreatedAt, &record.UpdatedAt,
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	record.Key.UpstreamFingerprint = service.OpenAIUpstreamFingerprint(fingerprint)
	record.CheckedAt = nullableTimePointer(checkedAt)
	record.QuarantinedUntil = nullableTimePointer(quarantinedUntil)
	record.OverrideCreatedAt = nullableTimePointer(overrideCreatedAt)
	record.OverrideExpiresAt = nullableTimePointer(overrideExpiresAt)
	record.OverrideRevokedAt = nullableTimePointer(overrideRevokedAt)
	record.NextProbeAt = nullableTimePointer(nextProbeAt)
	record.ProbeClaimedUntil = nullableTimePointer(probeClaimedUntil)
	if lastStatus.Valid {
		value := int(lastStatus.Int64)
		record.LastStatus = &value
	}
	return &record, nil
}
