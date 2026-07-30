//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionManualOverrideReachesSchedulerSnapshotThroughOutbox(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	client := testEntClient(t)

	accountRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	capabilityRepo := NewOpenAINativeCompactionCapabilityRepository(integrationDB)
	outboxRepo := NewSchedulerOutboxRepository(integrationDB)
	cache := NewSchedulerCache(rdb)
	groupRepo := NewGroupRepository(client, integrationDB)

	suffix := time.Now().UnixNano()
	group := mustCreateGroup(t, client, &service.Group{
		Name:        fmt.Sprintf("native-compaction-scheduler-e2e-%d", suffix),
		Platform:    service.PlatformOpenAI,
		Status:      service.StatusActive,
		IsExclusive: true,
	})
	account := &service.Account{
		Name:        fmt.Sprintf("native-compaction-scheduler-e2e-%d", suffix),
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    1,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}
	require.NoError(t, accountRepo.CreateWithAccountGroups(ctx, account, []service.AccountGroup{{
		GroupID:  group.ID,
		Priority: 1,
	}}))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1 OR group_id = $2", account.ID, group.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
		require.NoError(t, err)
	})

	bucket := service.SchedulerBucket{
		GroupID:  group.ID,
		Platform: service.PlatformOpenAI,
		Mode:     service.SchedulerModeSingle,
	}
	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				OutboxPollIntervalSeconds:  1,
				FullRebuildIntervalSeconds: 0,
				DbFallbackEnabled:          true,
			},
		},
	}
	snapshotService := service.NewSchedulerSnapshotService(cache, outboxRepo, accountRepo, groupRepo, cfg)

	// Establish a cache baseline before the canonical mutation. This makes the
	// assertion depend on outbox replay rather than the service's startup rebuild.
	baseline, _, err := snapshotService.ListSchedulableAccounts(ctx, &group.ID, service.PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, baseline, 1)
	require.Equal(t, account.ID, baseline[0].ID)
	require.Empty(t, baseline[0].OpenAINativeCompactionCapabilities)

	baselineOutboxID, err := outboxRepo.MaxID(ctx)
	require.NoError(t, err)
	snapshotService.Start()
	t.Cleanup(snapshotService.Stop)
	require.Eventually(t, func() bool {
		watermark, err := cache.GetOutboxWatermark(ctx)
		return err == nil && watermark >= baselineOutboxID
	}, 10*time.Second, 100*time.Millisecond, "consume all outbox work preceding the capability mutation")

	key := service.OpenAINativeCompactionCapabilityKey{
		AccountID:           account.ID,
		UpstreamFingerprint: service.OpenAIUpstreamFingerprint(fmt.Sprintf("upstream_v1_scheduler_e2e_%d", suffix)),
		EffectiveModel:      "gpt-native-compaction-e2e",
		ContractVersion:     service.OpenAINativeCompactionContractVersion,
	}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := createdAt.Add(time.Hour)
	require.NoError(t, capabilityRepo.UpsertManualOverride(ctx, service.OpenAINativeCompactionManualOverride{
		Key:       key,
		Mode:      service.OpenAINativeCompactionCapabilityModeForceOn,
		Actor:     "integration-test",
		Reason:    "verify canonical mutation to scheduler snapshot projection",
		CreatedAt: createdAt,
		ExpiresAt: &expiresAt,
	}))

	var mutationEventID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT id
		FROM scheduler_outbox
		WHERE id > $1 AND event_type = $2 AND account_id = $3
		ORDER BY id DESC
		LIMIT 1
	`, baselineOutboxID, service.SchedulerOutboxEventAccountChanged, account.ID).Scan(&mutationEventID))

	mismatch := key
	mismatch.EffectiveModel = "gpt-native-compaction-e2e-mismatch"
	require.Eventually(t, func() bool {
		watermark, err := cache.GetOutboxWatermark(ctx)
		if err != nil || watermark < mutationEventID {
			return false
		}
		snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
		if err != nil || !hit || len(snapshot) != 1 || snapshot[0] == nil {
			return false
		}
		cached := snapshot[0]
		if cached.ID != account.ID || len(cached.OpenAINativeCompactionCapabilities) != 1 {
			return false
		}
		capability := cached.OpenAINativeCompactionCapabilities[0]
		return capability.Key == key &&
			!capability.Supported &&
			capability.Mode == service.OpenAINativeCompactionCapabilityModeForceOn &&
			capability.Source == service.OpenAINativeCompactionCapabilitySourceManualOverride &&
			capability.OverrideActor == "integration-test" &&
			capability.OverrideReason == "verify canonical mutation to scheduler snapshot projection" &&
			capability.OverrideCreatedAt != nil && capability.OverrideCreatedAt.Equal(createdAt) &&
			cached.SupportsOpenAINativeRemoteCompactionV2(key, time.Now()) &&
			!cached.SupportsOpenAINativeRemoteCompactionV2(mismatch, time.Now())
	}, 10*time.Second, 100*time.Millisecond, "hydrate the exact capability through the mutation outbox event")

	require.NoError(t, rdb.Del(ctx, schedulerAccountMetaKey(strconv.FormatInt(account.ID, 10))).Err())
	missing, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit, "a missing sched:meta entry must fail closed as a cache miss")
	require.Nil(t, missing, "a cache miss must not return a partial account snapshot")
}
