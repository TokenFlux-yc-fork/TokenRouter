package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountChangedFailClosedCache struct {
	SchedulerCache

	account         *Account
	snapshotIDs     []int64
	watermark       int64
	watermarkWrites []int64
	deleteErr       error
	deleteCalls     []int64
}

func (c *accountChangedFailClosedCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	accounts := make([]*Account, 0, len(c.snapshotIDs))
	for _, accountID := range c.snapshotIDs {
		if c.account == nil || c.account.ID != accountID {
			return nil, false, nil
		}
		accounts = append(accounts, c.account)
	}
	return accounts, true, nil
}

func (c *accountChangedFailClosedCache) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	if c.account == nil || c.account.ID != accountID {
		return nil, nil
	}
	return c.account, nil
}

func (c *accountChangedFailClosedCache) DeleteAccount(_ context.Context, accountID int64) error {
	c.deleteCalls = append(c.deleteCalls, accountID)
	if c.deleteErr != nil {
		return c.deleteErr
	}
	if c.account != nil && c.account.ID == accountID {
		c.account = nil
	}
	return nil
}

func (c *accountChangedFailClosedCache) GetOutboxWatermark(context.Context) (int64, error) {
	return c.watermark, nil
}

func (c *accountChangedFailClosedCache) SetOutboxWatermark(_ context.Context, id int64) error {
	c.watermark = id
	c.watermarkWrites = append(c.watermarkWrites, id)
	return nil
}

type accountChangedFailClosedOutbox struct {
	SchedulerOutboxRepository
	event SchedulerOutboxEvent
}

func (r *accountChangedFailClosedOutbox) ListAfterAndReleaseDedup(_ context.Context, afterID int64, _ int) ([]SchedulerOutboxEvent, error) {
	if r.event.ID <= afterID {
		return nil, nil
	}
	return []SchedulerOutboxEvent{r.event}, nil
}

type accountChangedHydrationErrorRepo struct {
	AccountRepository
	err error
}

func (r *accountChangedHydrationErrorRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, r.err
}

func accountChangedInt64(value int64) *int64 {
	return &value
}

func newPositiveNativeCompactionAccount(accountID int64) (*Account, OpenAINativeCompactionCapabilityKey) {
	checkedAt := time.Now()
	key := OpenAINativeCompactionCapabilityKey{
		AccountID:           accountID,
		UpstreamFingerprint: "upstream_v1_fail_closed_test",
		EffectiveModel:      "gpt-fail-closed-test",
		ContractVersion:     OpenAINativeCompactionContractVersion,
	}
	return &Account{
		ID:       accountID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_supported": true},
		OpenAINativeCompactionCapabilities: []OpenAINativeCompactionCapability{{
			Key:       key,
			Supported: true,
			Mode:      OpenAINativeCompactionCapabilityModeAuto,
			Source:    OpenAINativeCompactionCapabilitySourceProbe,
			CheckedAt: &checkedAt,
		}},
	}, key
}

func TestSchedulerAccountChangedHydrationErrorInvalidatesStalePositiveWithoutAck(t *testing.T) {
	const (
		accountID int64 = 41
		eventID   int64 = 72
	)
	hydrateErr := errors.New("hydrate account failed")
	account, capabilityKey := newPositiveNativeCompactionAccount(accountID)
	cache := &accountChangedFailClosedCache{
		account:     account,
		snapshotIDs: []int64{accountID},
		watermark:   eventID - 1,
	}
	outbox := &accountChangedFailClosedOutbox{event: SchedulerOutboxEvent{
		ID:        eventID,
		EventType: SchedulerOutboxEventAccountChanged,
		AccountID: accountChangedInt64(accountID),
	}}
	svc := NewSchedulerSnapshotService(
		cache,
		outbox,
		&accountChangedHydrationErrorRepo{err: hydrateErr},
		nil,
		nil,
	)

	cached, err := cache.GetAccount(context.Background(), accountID)
	require.NoError(t, err)
	require.True(t, cached.SupportsOpenAINativeRemoteCompactionV2(capabilityKey, time.Now()))
	snapshot, hit, err := cache.GetSnapshot(context.Background(), SchedulerBucket{})
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.True(t, snapshot[0].SupportsOpenAINativeRemoteCompactionV2(capabilityKey, time.Now()))

	svc.pollOutbox()

	require.Equal(t, []int64{accountID}, cache.deleteCalls)
	require.Equal(t, eventID-1, cache.watermark)
	require.Empty(t, cache.watermarkWrites)
	cached, err = cache.GetAccount(context.Background(), accountID)
	require.NoError(t, err)
	require.Nil(t, cached, "stale positive account metadata must be invalidated")
	snapshot, hit, err = cache.GetSnapshot(context.Background(), SchedulerBucket{})
	require.NoError(t, err)
	require.False(t, hit, "snapshot must miss when its account metadata was invalidated")
	require.Empty(t, snapshot)
}

func TestSchedulerAccountChangedHydrationAndDeleteErrorsDoNotAck(t *testing.T) {
	const (
		accountID int64 = 51
		eventID   int64 = 82
	)
	hydrateErr := errors.New("hydrate account failed")
	deleteErr := errors.New("delete account cache failed")
	account, _ := newPositiveNativeCompactionAccount(accountID)
	cache := &accountChangedFailClosedCache{
		account:     account,
		snapshotIDs: []int64{accountID},
		watermark:   eventID - 1,
		deleteErr:   deleteErr,
	}
	event := SchedulerOutboxEvent{
		ID:        eventID,
		EventType: SchedulerOutboxEventAccountChanged,
		AccountID: accountChangedInt64(accountID),
	}
	svc := NewSchedulerSnapshotService(
		cache,
		&accountChangedFailClosedOutbox{event: event},
		&accountChangedHydrationErrorRepo{err: hydrateErr},
		nil,
		nil,
	)

	err := svc.handleOutboxEvent(context.Background(), event, make(map[batchSeenKey]struct{}))
	require.ErrorIs(t, err, hydrateErr)
	require.ErrorIs(t, err, deleteErr)

	svc.pollOutbox()

	require.Equal(t, []int64{accountID, accountID}, cache.deleteCalls)
	require.Equal(t, eventID-1, cache.watermark)
	require.Empty(t, cache.watermarkWrites)
}
