//go:build integration

package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func prepareOpenAINativeCompactionProbeBudgetIntegration(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{
		"229_openai_native_compaction_probe_budget.sql",
		"230_harden_openai_native_compaction_probe_dispatch.sql",
	} {
		migrationSQL, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, string(migrationSQL))
		require.NoError(t, err)
	}
	_, err := integrationDB.ExecContext(ctx, `TRUNCATE openai_native_compaction_probe_budget_reservations, openai_native_compaction_probe_budgets`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `TRUNCATE openai_native_compaction_probe_budget_reservations, openai_native_compaction_probe_budgets`)
	})
	return ctx
}

func markNativeCompactionProbeReservationDispatched(ctx context.Context, reservationID string) error {
	_, err := integrationDB.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budget_reservations
		SET dispatched_at = NOW(), authorization_principal_sha256 = $2
		WHERE reservation_id = $1::uuid AND state = 'reserved'
	`, reservationID, strings.Repeat("a", 64))
	return err
}

func TestOpenAINativeCompactionProbeBudgetConcurrentReserveHonorsDailyLimit(t *testing.T) {
	db := integrationDB
	ctx := prepareOpenAINativeCompactionProbeBudgetIntegration(t)

	repo := NewOpenAINativeCompactionProbeBudgetRepository(db)
	const (
		workers    = 12
		amount     = int64(100)
		dailyLimit = int64(500)
	)
	var granted atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			_, reserveErr := repo.Reserve(ctx, uuid.NewString(), amount, dailyLimit)
			switch {
			case reserveErr == nil:
				granted.Add(1)
			case errors.Is(reserveErr, service.ErrOpenAINativeCompactionProbeBudgetExceeded):
			default:
				t.Errorf("unexpected reserve error: %v", reserveErr)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, dailyLimit/amount, granted.Load())

	var reserved, committed int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, committed_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = (NOW() AT TIME ZONE 'UTC')::date
	`).Scan(&reserved, &committed))
	require.Equal(t, dailyLimit, reserved+committed)
}

func TestOpenAINativeCompactionProbeBudgetDispatchStateMachine(t *testing.T) {
	ctx := prepareOpenAINativeCompactionProbeBudgetIntegration(t)
	repo := NewOpenAINativeCompactionProbeBudgetRepository(integrationDB)

	committedReservation, err := repo.Reserve(ctx, uuid.NewString(), 300, 1000)
	require.NoError(t, err)
	require.ErrorIs(
		t,
		repo.CommitFull(ctx, committedReservation),
		service.ErrOpenAINativeCompactionProbeBudgetInvalidState,
	)
	require.NoError(t, markNativeCompactionProbeReservationDispatched(ctx, committedReservation.ID))
	require.ErrorIs(
		t,
		repo.Release(ctx, committedReservation),
		service.ErrOpenAINativeCompactionProbeBudgetInvalidState,
	)
	require.NoError(t, repo.CommitFull(ctx, committedReservation))
	require.NoError(t, repo.CommitFull(ctx, committedReservation))

	releasedReservation, err := repo.Reserve(ctx, uuid.NewString(), 200, 1000)
	require.NoError(t, err)
	require.NoError(t, repo.Release(ctx, releasedReservation))
	require.NoError(t, repo.Release(ctx, releasedReservation))
	// A released reservation cannot be reintroduced into the possibly-cost-bearing state.
	require.NoError(t, markNativeCompactionProbeReservationDispatched(ctx, releasedReservation.ID))
	var releasedState string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT state FROM openai_native_compaction_probe_budget_reservations
		WHERE reservation_id = $1::uuid
	`, releasedReservation.ID).Scan(&releasedState))
	require.Equal(t, service.OpenAINativeCompactionProbeBudgetReleased, releasedState)
	require.ErrorIs(
		t,
		repo.CommitFull(ctx, releasedReservation),
		service.ErrOpenAINativeCompactionProbeBudgetInvalidState,
	)

	var reserved, committed int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, committed_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = (NOW() AT TIME ZONE 'UTC')::date
	`).Scan(&reserved, &committed))
	require.Zero(t, reserved)
	require.Equal(t, int64(300), committed)
}

func TestOpenAINativeCompactionProbeBudgetConcurrentSameReservationIsIdempotent(t *testing.T) {
	ctx := prepareOpenAINativeCompactionProbeBudgetIntegration(t)
	repo := NewOpenAINativeCompactionProbeBudgetRepository(integrationDB)
	reservationID := uuid.NewString()

	const workers = 8
	start := make(chan struct{})
	reservations := make(chan service.OpenAINativeCompactionProbeBudgetReservation, workers)
	errorsByWorker := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			reservation, reserveErr := repo.Reserve(ctx, reservationID, 300, 1000)
			if reserveErr == nil {
				reservations <- reservation
			}
			errorsByWorker <- reserveErr
		}()
	}
	close(start)
	wg.Wait()
	close(reservations)
	close(errorsByWorker)

	for reserveErr := range errorsByWorker {
		require.NoError(t, reserveErr)
	}
	for reservation := range reservations {
		require.Equal(t, reservationID, reservation.ID)
		require.Equal(t, int64(300), reservation.AmountMicroUSD)
	}

	var reserved int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT reserved_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = (NOW() AT TIME ZONE 'UTC')::date
	`).Scan(&reserved))
	require.Equal(t, int64(300), reserved)
}

func TestOpenAINativeCompactionProbeBudgetReapsExpiredConservativelyAndConcurrently(t *testing.T) {
	ctx := prepareOpenAINativeCompactionProbeBudgetIntegration(t)
	repoA := NewOpenAINativeCompactionProbeBudgetRepository(integrationDB)
	repoB := NewOpenAINativeCompactionProbeBudgetRepository(integrationDB)

	undispatched, err := repoA.Reserve(ctx, uuid.NewString(), 200, 1000)
	require.NoError(t, err)
	dispatched, err := repoA.Reserve(ctx, uuid.NewString(), 300, 1000)
	require.NoError(t, err)
	require.NoError(t, markNativeCompactionProbeReservationDispatched(ctx, dispatched.ID))
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE openai_native_compaction_probe_budget_reservations
		SET expires_at = NOW() - INTERVAL '1 second'
		WHERE reservation_id IN ($1::uuid, $2::uuid)
	`, undispatched.ID, dispatched.ID)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan service.OpenAINativeCompactionProbeBudgetReapResult, 2)
	errs := make(chan error, 2)
	for _, repo := range []service.OpenAINativeCompactionProbeBudgetRepository{repoA, repoB} {
		go func(repository service.OpenAINativeCompactionProbeBudgetRepository) {
			<-start
			result, reapErr := repository.ReapExpired(ctx, 10)
			results <- result
			errs <- reapErr
		}(repo)
	}
	close(start)
	var total service.OpenAINativeCompactionProbeBudgetReapResult
	for range 2 {
		require.NoError(t, <-errs)
		result := <-results
		total.Examined += result.Examined
		total.Committed += result.Committed
		total.Released += result.Released
	}
	require.Equal(t, 2, total.Examined)
	require.Equal(t, 1, total.Committed)
	require.Equal(t, 1, total.Released)

	var reserved, committed int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, committed_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = (NOW() AT TIME ZONE 'UTC')::date
	`).Scan(&reserved, &committed))
	require.Zero(t, reserved)
	require.Equal(t, int64(300), committed)
}

func TestOpenAINativeCompactionProbeBudgetReserveIsIdempotent(t *testing.T) {
	ctx := prepareOpenAINativeCompactionProbeBudgetIntegration(t)
	repo := NewOpenAINativeCompactionProbeBudgetRepository(integrationDB)
	reservationID := uuid.NewString()

	first, err := repo.Reserve(ctx, reservationID, 300, 1000)
	require.NoError(t, err)
	second, err := repo.Reserve(ctx, reservationID, 300, 1000)
	require.NoError(t, err)
	require.Equal(t, first, second)
	_, err = repo.Reserve(ctx, reservationID, 301, 1000)
	require.ErrorIs(t, err, service.ErrOpenAINativeCompactionProbeBudgetConflict)

	var reserved, committed int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT reserved_micro_usd, committed_micro_usd
		FROM openai_native_compaction_probe_budgets
		WHERE budget_day = (NOW() AT TIME ZONE 'UTC')::date
	`).Scan(&reserved, &committed))
	require.Equal(t, int64(300), reserved)
	require.Zero(t, committed)
}
