package repository

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

const nativeProbeBudgetReservationID = "db76bf71-0967-45a2-a898-8e7f42f67d75"

func TestOpenAINativeCompactionProbeBudgetReserve(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT (NOW() AT TIME ZONE 'UTC')::date")).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day"}).AddRow(day))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO openai_native_compaction_probe_budgets").
		WithArgs(day).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT reservation_id::text, budget_day, amount_micro_usd, state").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT reserved_micro_usd, committed_micro_usd.*FOR UPDATE").
		WithArgs(day).
		WillReturnRows(sqlmock.NewRows([]string{"reserved", "committed"}).AddRow(int64(100), int64(200)))
	mock.ExpectExec("UPDATE openai_native_compaction_probe_budgets").
		WithArgs(day, int64(300)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO openai_native_compaction_probe_budget_reservations").
		WithArgs(nativeProbeBudgetReservationID, day, int64(300), service.OpenAINativeCompactionProbeBudgetReserved).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := NewOpenAINativeCompactionProbeBudgetRepository(db)
	reservation, err := repo.Reserve(context.Background(), nativeProbeBudgetReservationID, 300, 1000)
	require.NoError(t, err)
	require.Equal(t, int64(300), reservation.AmountMicroUSD)
	require.Equal(t, service.OpenAINativeCompactionProbeBudgetReserved, reservation.State)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeBudgetReserveIsIdempotentAcrossUTCDays(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	currentDay := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	originalDay := currentDay.AddDate(0, 0, -1)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT (NOW() AT TIME ZONE 'UTC')::date")).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day"}).AddRow(currentDay))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO openai_native_compaction_probe_budgets").
		WithArgs(currentDay).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT reservation_id::text, budget_day, amount_micro_usd, state").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"reservation_id", "budget_day", "amount", "state"}).AddRow(
			nativeProbeBudgetReservationID,
			originalDay,
			int64(300),
			service.OpenAINativeCompactionProbeBudgetReserved,
		))
	mock.ExpectCommit()

	reservation, err := NewOpenAINativeCompactionProbeBudgetRepository(db).Reserve(
		context.Background(),
		nativeProbeBudgetReservationID,
		300,
		1000,
	)
	require.NoError(t, err)
	require.Equal(t, originalDay, reservation.BudgetDay)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeBudgetMarkDispatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	principal := strings.Repeat("a", 64)
	reservation := service.OpenAINativeCompactionProbeBudgetReservation{
		ID:             nativeProbeBudgetReservationID,
		BudgetDay:      day,
		AmountMicroUSD: 300,
		State:          service.OpenAINativeCompactionProbeBudgetReserved,
	}
	fence := nativeProbeDispatchFence()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT encode\\(sha256").
		WithArgs(fence.Claim.Capability.ID, fence.Claim.Capability.Key.AccountID,
			fence.Claim.Capability.Key.UpstreamFingerprint, fence.Claim.Capability.Key.EffectiveModel,
			fence.Claim.Capability.Key.ContractVersion, fence.Claim.ClaimToken,
			fence.Claim.AccountRevision, fence.IsolatedGroupID, fence.IsolatedAPIKeyID,
			service.OpenAINativeCompactionCapabilityModeAuto).
		WillReturnRows(sqlmock.NewRows([]string{"principal"}).AddRow(principal))
	mock.ExpectQuery("SELECT budget_day, amount_micro_usd, state, dispatched_at,.*authorization_principal_sha256.*FOR UPDATE").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day", "amount", "state", "dispatched_at", "principal"}).AddRow(
			day, int64(300), service.OpenAINativeCompactionProbeBudgetReserved, nil, nil,
		))
	mock.ExpectExec("UPDATE openai_native_compaction_probe_budget_reservations.*SET dispatched_at = NOW").
		WithArgs(nativeProbeBudgetReservationID, service.OpenAINativeCompactionProbeBudgetReserved, principal).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	principalResult, err := NewOpenAINativeCompactionProbeBudgetRepository(db).MarkDispatched(context.Background(), reservation, fence)
	require.NoError(t, err)
	require.Equal(t, principal, principalResult)
	require.NoError(t, mock.ExpectationsWereMet())
}

func nativeProbeDispatchFence() service.OpenAINativeCompactionProbeDispatchFence {
	return service.OpenAINativeCompactionProbeDispatchFence{
		Claim: service.OpenAINativeCompactionProbeClaim{
			Capability: service.OpenAINativeCompactionCapabilityRecord{
				ID: 17,
				OpenAINativeCompactionCapability: service.OpenAINativeCompactionCapability{Key: service.OpenAINativeCompactionCapabilityKey{
					AccountID: 73, UpstreamFingerprint: "upstream_v1_fixture", EffectiveModel: "gpt-5.6", ContractVersion: service.OpenAINativeCompactionContractVersion,
				}},
			},
			ClaimToken:      "worker:claim",
			AccountRevision: "101:202:" + strings.Repeat("b", 64),
		},
		IsolatedGroupID:  31,
		IsolatedAPIKeyID: 41,
	}
}

func TestOpenAINativeCompactionProbeBudgetMarkDispatchedRejectsLostClaimBeforeReservationMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	reservation := service.OpenAINativeCompactionProbeBudgetReservation{
		ID: nativeProbeBudgetReservationID, BudgetDay: day, AmountMicroUSD: 300,
		State: service.OpenAINativeCompactionProbeBudgetReserved,
	}
	fence := nativeProbeDispatchFence()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT encode\\(sha256").
		WithArgs(fence.Claim.Capability.ID, fence.Claim.Capability.Key.AccountID,
			fence.Claim.Capability.Key.UpstreamFingerprint, fence.Claim.Capability.Key.EffectiveModel,
			fence.Claim.Capability.Key.ContractVersion, fence.Claim.ClaimToken,
			fence.Claim.AccountRevision, fence.IsolatedGroupID, fence.IsolatedAPIKeyID,
			service.OpenAINativeCompactionCapabilityModeAuto).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	principal, err := NewOpenAINativeCompactionProbeBudgetRepository(db).MarkDispatched(context.Background(), reservation, fence)
	require.Empty(t, principal)
	require.ErrorIs(t, err, service.ErrOpenAINativeCompactionProbeClaimLost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeBudgetCommitFull(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	reservation := service.OpenAINativeCompactionProbeBudgetReservation{
		ID:             nativeProbeBudgetReservationID,
		BudgetDay:      day,
		AmountMicroUSD: 300,
		State:          service.OpenAINativeCompactionProbeBudgetReserved,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT budget_day, amount_micro_usd, state, dispatched_at.*FOR UPDATE").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day", "amount", "state", "dispatched_at"}).AddRow(day, int64(300), service.OpenAINativeCompactionProbeBudgetReserved, time.Now()))
	mock.ExpectExec("UPDATE openai_native_compaction_probe_budgets").
		WithArgs(day, int64(300), int64(300)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE openai_native_compaction_probe_budget_reservations").
		WithArgs(nativeProbeBudgetReservationID, service.OpenAINativeCompactionProbeBudgetCommitted, service.OpenAINativeCompactionProbeBudgetReserved).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = NewOpenAINativeCompactionProbeBudgetRepository(db).CommitFull(context.Background(), reservation)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeBudgetReleaseRejectsDispatchedReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	reservation := service.OpenAINativeCompactionProbeBudgetReservation{
		ID:             nativeProbeBudgetReservationID,
		BudgetDay:      day,
		AmountMicroUSD: 300,
		State:          service.OpenAINativeCompactionProbeBudgetReserved,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT budget_day, amount_micro_usd, state, dispatched_at.*FOR UPDATE").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day", "amount", "state", "dispatched_at"}).AddRow(
			day,
			int64(300),
			service.OpenAINativeCompactionProbeBudgetReserved,
			time.Now(),
		))
	mock.ExpectRollback()

	err = NewOpenAINativeCompactionProbeBudgetRepository(db).Release(context.Background(), reservation)
	require.ErrorIs(t, err, service.ErrOpenAINativeCompactionProbeBudgetInvalidState)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAINativeCompactionProbeBudgetReleaseRejectsCommittedReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	day := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	reservation := service.OpenAINativeCompactionProbeBudgetReservation{
		ID:             nativeProbeBudgetReservationID,
		BudgetDay:      day,
		AmountMicroUSD: 300,
		State:          service.OpenAINativeCompactionProbeBudgetReserved,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT budget_day, amount_micro_usd, state, dispatched_at.*FOR UPDATE").
		WithArgs(nativeProbeBudgetReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"budget_day", "amount", "state", "dispatched_at"}).AddRow(day, int64(300), service.OpenAINativeCompactionProbeBudgetCommitted, time.Now()))
	mock.ExpectRollback()

	err = NewOpenAINativeCompactionProbeBudgetRepository(db).Release(context.Background(), reservation)
	require.ErrorIs(t, err, service.ErrOpenAINativeCompactionProbeBudgetInvalidState)
	require.NoError(t, mock.ExpectationsWereMet())
}
