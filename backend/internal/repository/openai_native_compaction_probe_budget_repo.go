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

type openAINativeCompactionProbeBudgetRepository struct {
	db *sql.DB
}

var _ service.OpenAINativeCompactionProbeBudgetRepository = (*openAINativeCompactionProbeBudgetRepository)(nil)

func NewOpenAINativeCompactionProbeBudgetRepository(db *sql.DB) service.OpenAINativeCompactionProbeBudgetRepository {
	return &openAINativeCompactionProbeBudgetRepository{db: db}
}

func (r *openAINativeCompactionProbeBudgetRepository) Reserve(
	ctx context.Context,
	reservationID string,
	amountMicroUSD int64,
	dailyLimitMicroUSD int64,
) (service.OpenAINativeCompactionProbeBudgetReservation, error) {
	var out service.OpenAINativeCompactionProbeBudgetReservation
	reservationID = strings.TrimSpace(reservationID)
	if _, err := uuid.Parse(reservationID); err != nil {
		return out, errors.New("openai native compaction probe budget reservation id is invalid")
	}
	if amountMicroUSD <= 0 || dailyLimitMicroUSD <= 0 || amountMicroUSD > dailyLimitMicroUSD {
		return out, errors.New("openai native compaction probe budget limits are invalid")
	}
	if r == nil || r.db == nil {
		return out, service.ErrOpenAINativeCompactionProbeBudgetUnavailable
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	var budgetDay time.Time
	if err := tx.QueryRowContext(ctx, `SELECT (NOW() AT TIME ZONE 'UTC')::date`).Scan(&budgetDay); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, reservationID); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO openai_native_compaction_probe_budgets (budget_day)
		VALUES ($1)
		ON CONFLICT (budget_day) DO NOTHING
	`, budgetDay); err != nil {
		return out, err
	}

	var existing service.OpenAINativeCompactionProbeBudgetReservation
	err = tx.QueryRowContext(ctx, `
		SELECT reservation_id::text, budget_day, amount_micro_usd, state
		FROM openai_native_compaction_probe_budget_reservations
		WHERE reservation_id = $1::uuid
	`, reservationID).Scan(&existing.ID, &existing.BudgetDay, &existing.AmountMicroUSD, &existing.State)
	if err == nil {
		if existing.AmountMicroUSD != amountMicroUSD {
			return out, service.ErrOpenAINativeCompactionProbeBudgetConflict
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	var reserved, committed int64
	if err := tx.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, committed_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = $1
		FOR UPDATE
	`, budgetDay).Scan(&reserved, &committed); err != nil {
		return out, err
	}
	if committed > dailyLimitMicroUSD || reserved > dailyLimitMicroUSD-committed || amountMicroUSD > dailyLimitMicroUSD-committed-reserved {
		return out, service.ErrOpenAINativeCompactionProbeBudgetExceeded
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budgets
		SET reserved_micro_usd = reserved_micro_usd + $2,
			updated_at = NOW()
		WHERE budget_day = $1
	`, budgetDay, amountMicroUSD); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO openai_native_compaction_probe_budget_reservations (
			reservation_id, budget_day, amount_micro_usd, state, expires_at
		) VALUES ($1::uuid, $2, $3, $4, NOW() + INTERVAL '10 minutes')
	`, reservationID, budgetDay, amountMicroUSD, service.OpenAINativeCompactionProbeBudgetReserved); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return service.OpenAINativeCompactionProbeBudgetReservation{
		ID:             reservationID,
		BudgetDay:      budgetDay,
		AmountMicroUSD: amountMicroUSD,
		State:          service.OpenAINativeCompactionProbeBudgetReserved,
	}, nil
}

func (r *openAINativeCompactionProbeBudgetRepository) ReapExpired(
	ctx context.Context,
	limit int,
) (service.OpenAINativeCompactionProbeBudgetReapResult, error) {
	var out service.OpenAINativeCompactionProbeBudgetReapResult
	if limit <= 0 {
		return out, nil
	}
	if r == nil || r.db == nil {
		return out, service.ErrOpenAINativeCompactionProbeBudgetUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT reservation_id::text, budget_day, amount_micro_usd, dispatched_at IS NOT NULL
		FROM openai_native_compaction_probe_budget_reservations
		WHERE state = $1 AND expires_at <= NOW()
		ORDER BY expires_at, reservation_id
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, service.OpenAINativeCompactionProbeBudgetReserved, limit)
	if err != nil {
		return out, err
	}
	type expired struct {
		id         string
		day        time.Time
		amount     int64
		dispatched bool
	}
	var reservations []expired
	for rows.Next() {
		var value expired
		if err := rows.Scan(&value.id, &value.day, &value.amount, &value.dispatched); err != nil {
			_ = rows.Close()
			return out, err
		}
		reservations = append(reservations, value)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, reservation := range reservations {
		target := service.OpenAINativeCompactionProbeBudgetReleased
		committedDelta := int64(0)
		if reservation.dispatched {
			target = service.OpenAINativeCompactionProbeBudgetCommitted
			committedDelta = reservation.amount
			out.Committed++
		} else {
			out.Released++
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE openai_native_compaction_probe_budgets
			SET reserved_micro_usd = reserved_micro_usd - $2,
				committed_micro_usd = committed_micro_usd + $3,
				updated_at = NOW()
			WHERE budget_day = $1 AND reserved_micro_usd >= $2
		`, reservation.day, reservation.amount, committedDelta)
		if err != nil {
			return service.OpenAINativeCompactionProbeBudgetReapResult{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			if err == nil {
				err = service.ErrOpenAINativeCompactionProbeBudgetInvalidState
			}
			return service.OpenAINativeCompactionProbeBudgetReapResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE openai_native_compaction_probe_budget_reservations
			SET state = $2, settled_at = NOW()
			WHERE reservation_id = $1::uuid AND state = $3
		`, reservation.id, target, service.OpenAINativeCompactionProbeBudgetReserved); err != nil {
			return service.OpenAINativeCompactionProbeBudgetReapResult{}, err
		}
	}
	out.Examined = len(reservations)
	if err := tx.Commit(); err != nil {
		return service.OpenAINativeCompactionProbeBudgetReapResult{}, err
	}
	return out, nil
}

func (r *openAINativeCompactionProbeBudgetRepository) MarkDispatched(
	ctx context.Context,
	reservation service.OpenAINativeCompactionProbeBudgetReservation,
	fence service.OpenAINativeCompactionProbeDispatchFence,
) (string, error) {
	if !reservation.Valid() {
		return "", errors.New("invalid openai native compaction probe budget reservation")
	}
	if !fence.Valid() {
		return "", service.ErrOpenAINativeCompactionProbeClaimLost
	}
	if r == nil || r.db == nil {
		return "", service.ErrOpenAINativeCompactionProbeBudgetUnavailable
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	principalSHA256, err := validateOpenAINativeCompactionProbeDispatchFence(ctx, tx, fence)
	if err != nil {
		return "", err
	}

	var budgetDay time.Time
	var amount int64
	var state string
	var dispatchedAt sql.NullTime
	var storedPrincipal sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT budget_day, amount_micro_usd, state, dispatched_at,
			authorization_principal_sha256
		FROM openai_native_compaction_probe_budget_reservations
		WHERE reservation_id = $1::uuid
		FOR UPDATE
	`, reservation.ID).Scan(&budgetDay, &amount, &state, &dispatchedAt, &storedPrincipal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", service.ErrOpenAINativeCompactionProbeBudgetConflict
		}
		return "", err
	}
	if !budgetDay.Equal(reservation.BudgetDay) || amount != reservation.AmountMicroUSD {
		return "", service.ErrOpenAINativeCompactionProbeBudgetConflict
	}
	if state == service.OpenAINativeCompactionProbeBudgetCommitted {
		if !storedPrincipal.Valid || storedPrincipal.String != principalSHA256 {
			return "", service.ErrOpenAINativeCompactionProbeBudgetConflict
		}
		return principalSHA256, tx.Commit()
	}
	if state != service.OpenAINativeCompactionProbeBudgetReserved {
		return "", service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	if dispatchedAt.Valid {
		if !storedPrincipal.Valid || storedPrincipal.String != principalSHA256 {
			return "", service.ErrOpenAINativeCompactionProbeBudgetConflict
		}
		return principalSHA256, tx.Commit()
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budget_reservations
		SET dispatched_at = NOW(),
			authorization_principal_sha256 = $3
		WHERE reservation_id = $1::uuid
		  AND state = $2
		  AND dispatched_at IS NULL
	`, reservation.ID, service.OpenAINativeCompactionProbeBudgetReserved, principalSHA256)
	if err != nil {
		return "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if affected != 1 {
		return "", service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	return principalSHA256, tx.Commit()
}

func validateOpenAINativeCompactionProbeDispatchFence(
	ctx context.Context,
	tx *sql.Tx,
	fence service.OpenAINativeCompactionProbeDispatchFence,
) (string, error) {
	if tx == nil || !fence.Valid() {
		return "", service.ErrOpenAINativeCompactionProbeClaimLost
	}
	claim := fence.Claim
	key := claim.Capability.Key
	var principalSHA256 string
	err := tx.QueryRowContext(ctx, `
		SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
		FROM openai_native_compaction_capabilities c
		JOIN accounts a
		  ON a.id = c.account_id
		 AND a.deleted_at IS NULL
		 AND a.status = 'active'
		 AND a.schedulable
		 AND a.parent_account_id IS NULL
		JOIN account_groups ag
		  ON ag.account_id = a.id
		 AND ag.group_id = $8
		JOIN groups g
		  ON g.id = ag.group_id
		 AND g.deleted_at IS NULL
		 AND g.status = 'active'
		JOIN api_keys k
		  ON k.id = $9
		 AND k.deleted_at IS NULL
		 AND k.status = 'active'
		 AND NOT k.team_owner_disabled
		 AND NOT k.is_composite
		 AND k.group_id = g.id
		 AND (k.expires_at IS NULL OR k.expires_at > NOW())
		 AND (k.quota <= 0 OR k.quota_used < k.quota)
		JOIN users u
		  ON u.id = k.user_id
		 AND u.deleted_at IS NULL
		 AND u.status = 'active'
		LEFT JOIN proxies p
		  ON p.id = a.proxy_id
		 AND p.deleted_at IS NULL
		WHERE c.id = $1
		  AND c.account_id = $2
		  AND c.upstream_fingerprint = $3
		  AND c.effective_model = $4
		  AND c.contract_version = $5
		  AND c.mode = $10
		  AND c.probe_claimed_by = $6
		  AND c.probe_claimed_until >= NOW()
		  AND a.xmin::text || ':' || COALESCE(p.xmin::text, '') || ':' ||
			  encode(sha256(convert_to(a.credentials::text, 'UTF8')), 'hex') = $7
		FOR UPDATE OF c, a, ag, g, k, u
	`, claim.Capability.ID, key.AccountID, key.UpstreamFingerprint,
		key.EffectiveModel, key.ContractVersion, claim.ClaimToken,
		claim.AccountRevision, fence.IsolatedGroupID, fence.IsolatedAPIKeyID,
		service.OpenAINativeCompactionCapabilityModeAuto,
	).Scan(&principalSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrOpenAINativeCompactionProbeClaimLost
	}
	if err != nil {
		return "", err
	}
	if len(principalSHA256) != 64 || strings.ToLower(principalSHA256) != principalSHA256 {
		return "", service.ErrOpenAINativeCompactionProbeClaimLost
	}
	return principalSHA256, nil
}

func (r *openAINativeCompactionProbeBudgetRepository) CommitFull(ctx context.Context, reservation service.OpenAINativeCompactionProbeBudgetReservation) error {
	return r.settle(ctx, reservation, service.OpenAINativeCompactionProbeBudgetCommitted)
}

func (r *openAINativeCompactionProbeBudgetRepository) Release(ctx context.Context, reservation service.OpenAINativeCompactionProbeBudgetReservation) error {
	return r.settle(ctx, reservation, service.OpenAINativeCompactionProbeBudgetReleased)
}

func (r *openAINativeCompactionProbeBudgetRepository) settle(
	ctx context.Context,
	reservation service.OpenAINativeCompactionProbeBudgetReservation,
	target string,
) error {
	if !reservation.Valid() {
		return errors.New("invalid openai native compaction probe budget reservation")
	}
	if target != service.OpenAINativeCompactionProbeBudgetCommitted && target != service.OpenAINativeCompactionProbeBudgetReleased {
		return service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	if r == nil || r.db == nil {
		return service.ErrOpenAINativeCompactionProbeBudgetUnavailable
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var budgetDay time.Time
	var amount int64
	var state string
	var dispatchedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT budget_day, amount_micro_usd, state, dispatched_at
		FROM openai_native_compaction_probe_budget_reservations
		WHERE reservation_id = $1::uuid
		FOR UPDATE
	`, reservation.ID).Scan(&budgetDay, &amount, &state, &dispatchedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrOpenAINativeCompactionProbeBudgetConflict
		}
		return err
	}
	if !budgetDay.Equal(reservation.BudgetDay) || amount != reservation.AmountMicroUSD {
		return service.ErrOpenAINativeCompactionProbeBudgetConflict
	}
	if state == target {
		return tx.Commit()
	}
	if state != service.OpenAINativeCompactionProbeBudgetReserved {
		return service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	if target == service.OpenAINativeCompactionProbeBudgetCommitted && !dispatchedAt.Valid {
		return service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	if target == service.OpenAINativeCompactionProbeBudgetReleased && dispatchedAt.Valid {
		return service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}

	committedDelta := int64(0)
	if target == service.OpenAINativeCompactionProbeBudgetCommitted {
		committedDelta = amount
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budgets
		SET reserved_micro_usd = reserved_micro_usd - $2,
			committed_micro_usd = committed_micro_usd + $3,
			updated_at = NOW()
		WHERE budget_day = $1
		  AND reserved_micro_usd >= $2
	`, budgetDay, amount, committedDelta)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: daily ledger reservation is missing", service.ErrOpenAINativeCompactionProbeBudgetInvalidState)
	}

	result, err = tx.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budget_reservations
		SET state = $2,
			settled_at = NOW()
		WHERE reservation_id = $1::uuid
		  AND state = $3
	`, reservation.ID, target, service.OpenAINativeCompactionProbeBudgetReserved)
	if err != nil {
		return err
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrOpenAINativeCompactionProbeBudgetInvalidState
	}
	return tx.Commit()
}
