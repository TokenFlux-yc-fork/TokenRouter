package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAICodexRemainingCapacityPoints_UsesLowerWindow(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 40, 70, now)

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.True(t, ok)
	require.InDelta(t, 30, points, 1e-9)
}

func TestOpenAICodexRemainingCapacityPoints_TreatsMissingSnapshotAsFull(t *testing.T) {
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
	}

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, time.Now().UTC())
	require.True(t, ok)
	require.Equal(t, 100.0, points)
}

func TestOpenAICodexRemainingCapacityPoints_TreatsResetWindowAsFull(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 95, 40, now)
	account.Extra["codex_5h_reset_at"] = now.Add(-time.Minute).Format(time.RFC3339)

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.True(t, ok)
	require.InDelta(t, 60, points, 1e-9)
}

func TestOpenAICodexRemainingCapacityPoints_TreatsStaleSnapshotAsFull(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 99, 99, now)
	account.Extra["codex_usage_updated_at"] = now.Add(-openAICodexAutoPauseStaleAfter - time.Minute).Format(time.RFC3339)

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.True(t, ok)
	require.Equal(t, 100.0, points)
}

func TestOpenAICodexRemainingCapacityPoints_TreatsUsageWithoutFreshTimestampAsFull(t *testing.T) {
	now := time.Now().UTC()
	ctx := WithOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{
		DefaultThreshold5h: 0.95,
	})
	account := openAICodexBackupPoolTestAccount(1, 96, 20, now)
	delete(account.Extra, "codex_usage_updated_at")

	points, ok := openAICodexRemainingCapacityPoints(ctx, &account, now)
	require.True(t, ok)
	require.Equal(t, 100.0, points)
}

func TestOpenAICodexRemainingCapacityPoints_IgnoresNonFiniteUsageSnapshotValues(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 10, 40, now)
	account.Extra["codex_5h_used_percent"] = "NaN"

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.True(t, ok)
	require.False(t, math.IsNaN(points))
	require.InDelta(t, 60, points, 1e-9)
}

func TestOpenAICodexRemainingCapacityPoints_IgnoresInfUsageForAutoPause(t *testing.T) {
	now := time.Now().UTC()
	ctx := WithOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{
		DefaultThreshold5h: 0.95,
	})
	account := openAICodexBackupPoolTestAccount(1, 10, 40, now)
	account.Extra["codex_5h_used_percent"] = math.Inf(1)

	points, ok := openAICodexRemainingCapacityPoints(ctx, &account, now)
	require.True(t, ok)
	require.False(t, math.IsInf(points, 0))
	require.InDelta(t, 60, points, 1e-9)
}

func TestOpenAICodexRemainingCapacityPoints_ExcludesQuotaAutoPausedAccount(t *testing.T) {
	now := time.Now().UTC()
	ctx := WithOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{
		DefaultThreshold5h: 0.95,
	})
	account := openAICodexBackupPoolTestAccount(1, 96, 20, now)

	points, ok := openAICodexRemainingCapacityPoints(ctx, &account, now)
	require.False(t, ok)
	require.Zero(t, points)
}

func TestOpenAICodexRemainingCapacityPoints_ExcludesAccountWithoutCodexModelSupport(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 10, 20, now)
	account.Credentials = map[string]any{
		"model_whitelist": []string{"gpt-4.1"},
	}

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.False(t, ok)
	require.Zero(t, points)
}

func TestOpenAICodexRemainingCapacityPoints_AllowsAccountWithCodexModelSupport(t *testing.T) {
	now := time.Now().UTC()
	account := openAICodexBackupPoolTestAccount(1, 10, 20, now)
	account.Credentials = map[string]any{
		"model_whitelist": []string{"gpt-5.3-codex"},
	}

	points, ok := openAICodexRemainingCapacityPoints(context.Background(), &account, now)
	require.True(t, ok)
	require.InDelta(t, 80, points, 1e-9)
}

func TestGroupBackupPoolRefillService_RefillsUntilThreshold(t *testing.T) {
	now := time.Now().UTC()
	target := openAICodexBackupPoolTestAccount(10, 80, 80, now)  // 20 points
	backupA := openAICodexBackupPoolTestAccount(20, 40, 70, now) // 30 points
	backupB := openAICodexBackupPoolTestAccount(21, 10, 20, now) // 80 points

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {target.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {target},
				2: {backupA, backupB},
			},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 100,
	})

	require.NoError(t, err)
	require.Equal(t, 2, added)
	require.Equal(t, int64(1), groupRepo.boundGroupID)
	require.Equal(t, []int64{20, 21}, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_RefillsFromBackupAccountWithoutSnapshot(t *testing.T) {
	now := time.Now().UTC()
	target := openAICodexBackupPoolTestAccount(10, 80, 80, now) // 20 points
	backup := Account{
		ID:          20,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
	}

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {target.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {target},
				2: {backup},
			},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 100,
	})

	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, []int64{20}, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_RequirePrivacySetSkipsUnsetAccounts(t *testing.T) {
	now := time.Now().UTC()
	target := openAICodexBackupPoolTestAccount(10, 0, 0, now)
	backupWithoutPrivacy := openAICodexBackupPoolTestAccount(20, 0, 0, now)
	backupWithPrivacy := withOpenAIPrivacySet(openAICodexBackupPoolTestAccount(21, 0, 0, now))

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {target.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {target},
				2: {backupWithoutPrivacy, backupWithPrivacy},
			},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		RequirePrivacySet:               true,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 100,
	})

	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, []int64{21}, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_IgnoresTargetShadowWithUnhealthyParent(t *testing.T) {
	now := time.Now().UTC()
	targetShadow := openAICodexBackupPoolTestAccount(10, 0, 0, now)
	parentID := int64(100)
	targetShadow.ParentAccountID = &parentID
	backup := openAICodexBackupPoolTestAccount(20, 0, 0, now)

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {targetShadow.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {targetShadow},
				2: {backup},
			},
			accountsByID: map[int64]*Account{},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 100,
	})

	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, []int64{20}, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_SkipsBackupShadowWithUnhealthyParent(t *testing.T) {
	now := time.Now().UTC()
	target := openAICodexBackupPoolTestAccount(10, 80, 80, now) // 20 points
	backupShadow := openAICodexBackupPoolTestAccount(20, 0, 0, now)
	parentID := int64(100)
	backupShadow.ParentAccountID = &parentID
	backupNormal := openAICodexBackupPoolTestAccount(21, 0, 0, now)

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {target.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {target},
				2: {backupShadow, backupNormal},
			},
			accountsByID: map[int64]*Account{},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 100,
	})

	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, []int64{21}, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_SkipsWhenAboveThreshold(t *testing.T) {
	now := time.Now().UTC()
	target := openAICodexBackupPoolTestAccount(10, 10, 20, now) // 80 points

	groupRepo := &backupPoolGroupRepoStub{
		existingByGroup: map[int64][]int64{1: {target.ID}},
	}
	svc := NewGroupBackupPoolRefillService(
		&backupPoolAccountRepoStub{
			accountsByGroup: map[int64][]Account{
				1: {target},
				2: {openAICodexBackupPoolTestAccount(20, 0, 0, now)},
			},
		},
		groupRepo,
		nil,
	)

	added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
		ID:                              1,
		Platform:                        PlatformOpenAI,
		BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
		BackupPoolRefillThresholdPoints: 50,
	})

	require.NoError(t, err)
	require.Zero(t, added)
	require.Nil(t, groupRepo.boundAccountIDs)
}

func TestGroupBackupPoolRefillService_SkipsNonFiniteThreshold(t *testing.T) {
	now := time.Now().UTC()
	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		groupRepo := &backupPoolGroupRepoStub{
			existingByGroup: map[int64][]int64{},
		}
		svc := NewGroupBackupPoolRefillService(
			&backupPoolAccountRepoStub{
				accountsByGroup: map[int64][]Account{
					1: {},
					2: {openAICodexBackupPoolTestAccount(20, 0, 0, now)},
				},
			},
			groupRepo,
			nil,
		)

		added, err := svc.RefillGroupIfNeeded(context.Background(), &Group{
			ID:                              1,
			Platform:                        PlatformOpenAI,
			BackupPoolGroupID:               int64PtrForBackupPoolTest(2),
			BackupPoolRefillThresholdPoints: threshold,
		})

		require.NoError(t, err)
		require.Zero(t, added)
		require.Nil(t, groupRepo.boundAccountIDs)
	}
}

type backupPoolAccountRepoStub struct {
	AccountRepository
	accountsByGroup map[int64][]Account
	accountsByID    map[int64]*Account
}

func (s *backupPoolAccountRepoStub) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, _ string) ([]Account, error) {
	return append([]Account(nil), s.accountsByGroup[groupID]...), nil
}

func (s *backupPoolAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if s.accountsByID == nil {
		return nil, ErrAccountNotFound
	}
	account, ok := s.accountsByID[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

type backupPoolGroupRepoStub struct {
	GroupRepository
	existingByGroup map[int64][]int64
	boundGroupID    int64
	boundAccountIDs []int64
}

func (s *backupPoolGroupRepoStub) GetAccountIDsByGroupIDs(_ context.Context, groupIDs []int64) ([]int64, error) {
	var out []int64
	for _, groupID := range groupIDs {
		out = append(out, s.existingByGroup[groupID]...)
	}
	return out, nil
}

func (s *backupPoolGroupRepoStub) BindAccountsToGroup(_ context.Context, groupID int64, accountIDs []int64) error {
	s.boundGroupID = groupID
	s.boundAccountIDs = append([]int64(nil), accountIDs...)
	return nil
}

func openAICodexBackupPoolTestAccount(id int64, used5h, used7d float64, now time.Time) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Format(time.RFC3339),
			"codex_5h_used_percent":  used5h,
			"codex_7d_used_percent":  used7d,
			"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
			"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
}

func int64PtrForBackupPoolTest(v int64) *int64 {
	return &v
}

func withOpenAIPrivacySet(account Account) Account {
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra["privacy_mode"] = PrivacyModeTrainingOff
	return account
}
