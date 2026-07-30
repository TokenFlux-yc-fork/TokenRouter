package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

var (
	ErrOpenAINativeCompactionProbeBudgetExceeded     = errors.New("openai native compaction probe daily budget exceeded")
	ErrOpenAINativeCompactionProbeBudgetConflict     = errors.New("openai native compaction probe budget reservation conflicts with existing state")
	ErrOpenAINativeCompactionProbeBudgetUnavailable  = errors.New("openai native compaction probe budget store is unavailable")
	ErrOpenAINativeCompactionProbeBudgetInvalidState = errors.New("openai native compaction probe budget reservation has invalid state")
)

const (
	OpenAINativeCompactionProbeBudgetReserved  = "reserved"
	OpenAINativeCompactionProbeBudgetCommitted = "committed"
	OpenAINativeCompactionProbeBudgetReleased  = "released"
)

type OpenAINativeCompactionProbeBudgetReservation struct {
	ID                           string
	BudgetDay                    time.Time
	AmountMicroUSD               int64
	State                        string
	ExpiresAt                    time.Time
	AuthorizationPrincipalSHA256 string
}

func (r OpenAINativeCompactionProbeBudgetReservation) Valid() bool {
	return strings.TrimSpace(r.ID) != "" && !r.BudgetDay.IsZero() && r.AmountMicroUSD > 0
}

// OpenAINativeCompactionProbeDispatchFence is transient authorization input.
// IsolatedAPIKeyID is a kill-switch sentinel, never an upstream credential.
type OpenAINativeCompactionProbeDispatchFence struct {
	Claim            OpenAINativeCompactionProbeClaim
	IsolatedGroupID  int64
	IsolatedAPIKeyID int64
}

func (f OpenAINativeCompactionProbeDispatchFence) Valid() bool {
	return f.Claim.Capability.ID > 0 && f.Claim.Capability.Key.Valid() &&
		strings.TrimSpace(f.Claim.ClaimToken) != "" && validOpenAINativeCompactionProbeRevision(f.Claim.AccountRevision) &&
		f.IsolatedGroupID > 0 && f.IsolatedAPIKeyID > 0
}

func validOpenAINativeCompactionProbeRevision(revision string) bool {
	parts := strings.Split(revision, ":")
	if len(parts) != 3 || parts[0] == "" || len(parts[2]) != 64 {
		return false
	}
	for _, part := range parts[:2] {
		for _, char := range part {
			if !unicode.IsDigit(char) {
				return false
			}
		}
	}
	for _, char := range parts[2] {
		if !unicode.IsDigit(char) && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

type OpenAINativeCompactionProbeBudgetReapResult struct {
	Examined  int
	Committed int
	Released  int
}

// OpenAINativeCompactionProbeBudgetRepository is an operator-cost ledger. It is
// deliberately separate from customer usage, balances, subscriptions, and
// settlement repositories.
type OpenAINativeCompactionProbeBudgetRepository interface {
	Reserve(
		ctx context.Context,
		reservationID string,
		amountMicroUSD int64,
		dailyLimitMicroUSD int64,
	) (OpenAINativeCompactionProbeBudgetReservation, error)
	// MarkDispatched atomically validates the live DB claim, isolated sentinel,
	// account/proxy identity and writes the possibly-cost-bearing boundary.
	MarkDispatched(ctx context.Context, reservation OpenAINativeCompactionProbeBudgetReservation, fence OpenAINativeCompactionProbeDispatchFence) (string, error)
	CommitFull(ctx context.Context, reservation OpenAINativeCompactionProbeBudgetReservation) error
	Release(ctx context.Context, reservation OpenAINativeCompactionProbeBudgetReservation) error
	// ReapExpired releases only definitely-undispatched reservations and fully
	// commits every expired reservation whose dispatch marker may represent cost.
	ReapExpired(ctx context.Context, limit int) (OpenAINativeCompactionProbeBudgetReapResult, error)
}
