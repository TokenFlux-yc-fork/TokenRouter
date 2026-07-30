package service

import (
	"context"
	"errors"
	"time"
)

var ErrOpenAINativeCompactionProbeClaimLost = errors.New("openai native compaction probe claim is no longer owned")

// OpenAINativeCompactionCapabilityRecord is the persisted capability projection,
// including probe scheduling metadata that is intentionally omitted from Account.
type OpenAINativeCompactionCapabilityRecord struct {
	ID int64
	OpenAINativeCompactionCapability
	NextProbeAt       *time.Time
	ProbeClaimedUntil *time.Time
	ProbeClaimedBy    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OpenAINativeCompactionProbeClaim is fenced by ClaimToken. A probe result must
// present the exact token returned by ClaimDue; worker identity alone is not a fence.
type OpenAINativeCompactionProbeClaim struct {
	Capability      OpenAINativeCompactionCapabilityRecord
	ClaimToken      string
	AccountRevision string
}

// OpenAINativeCompactionProbeResult contains only payload-free probe metadata.
// A nil Supported value records an inconclusive audit result without changing
// the canonical supported bit.
type OpenAINativeCompactionProbeResult struct {
	Key                          OpenAINativeCompactionCapabilityKey
	ClaimToken                   string
	AccountRevision              string
	Supported                    *bool
	SemanticOutcome              OpenAINativeCompactionOutcome
	StatusCode                   *int
	CheckedAt                    time.Time
	NextProbeAt                  *time.Time
	QuarantinedUntil             *time.Time
	LastSemanticFailure          string
	AuthorizationPrincipalSHA256 string
}

type OpenAINativeCompactionProbeWriteResult struct {
	AuditID          int64
	StaleIdentity    bool
	CanonicalUpdated bool
}

type OpenAINativeCompactionProbeAudit struct {
	ID                           int64
	CapabilityID                 *int64
	Key                          OpenAINativeCompactionCapabilityKey
	Supported                    *bool
	SemanticOutcome              OpenAINativeCompactionOutcome
	StatusCode                   *int
	StaleIdentity                bool
	CheckedAt                    time.Time
	AuthorizationPrincipalSHA256 string
}

type OpenAINativeCompactionManualOverride struct {
	Key       OpenAINativeCompactionCapabilityKey
	Mode      string
	Actor     string
	Reason    string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// OpenAINativeCompactionCapabilityRepository owns exact-key capability state.
// Every canonical mutation also publishes account_changed in the same SQL transaction.
type OpenAINativeCompactionCapabilityRepository interface {
	GetExact(ctx context.Context, key OpenAINativeCompactionCapabilityKey) (*OpenAINativeCompactionCapabilityRecord, error)
	UpsertTrustedOfficial(ctx context.Context, account *Account, key OpenAINativeCompactionCapabilityKey) (bool, error)
	EnsureAutoCandidate(ctx context.Context, key OpenAINativeCompactionCapabilityKey, dueAt time.Time) (bool, error)
	UpsertProbeResult(ctx context.Context, result OpenAINativeCompactionProbeResult) (OpenAINativeCompactionProbeWriteResult, error)
	SetQuarantine(ctx context.Context, key OpenAINativeCompactionCapabilityKey, until *time.Time, semanticFailure string) (bool, error)
	ClaimDue(ctx context.Context, now, claimUntil time.Time, workerID string, limit int) ([]OpenAINativeCompactionProbeClaim, error)
	// FenceProbeDispatch validates the exact claim token, live DB lease, and full
	// account/proxy/credential identity immediately before an upstream dispatch.
	FenceProbeDispatch(ctx context.Context, claim OpenAINativeCompactionProbeClaim) error
	UpsertManualOverride(ctx context.Context, override OpenAINativeCompactionManualOverride) error
	RevokeManualOverride(ctx context.Context, key OpenAINativeCompactionCapabilityKey, revokedAt, nextProbeAt time.Time) (bool, error)
	ListAudit(ctx context.Context, key OpenAINativeCompactionCapabilityKey, limit int) ([]OpenAINativeCompactionProbeAudit, error)
}
