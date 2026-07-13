//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminService_CreateGroup_BackupPoolRejectsUnsupportedPlatform(t *testing.T) {
	backupID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			backupID: {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:                            "g1",
		Platform:                        PlatformAnthropic,
		RateMultiplier:                  1.0,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 100,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "backup pool auto refill only supports openai groups")
	require.Nil(t, repo.created)
}

func TestAdminService_CreateGroup_BackupPoolPersistsConfig(t *testing.T) {
	backupID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			backupID: {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		RateMultiplier:                  1.0,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 150,
	})
	require.NoError(t, err)
	require.Equal(t, group, repo.created)
	require.Equal(t, backupID, *repo.created.BackupPoolGroupID)
	require.Equal(t, 150.0, repo.created.BackupPoolRefillThresholdPoints)
}

func TestAdminService_CreateGroup_BackupPoolRejectsNonPositiveThreshold(t *testing.T) {
	backupID := int64(10)
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			backupID: {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:              "g1",
		Platform:          PlatformOpenAI,
		RateMultiplier:    1.0,
		BackupPoolGroupID: &backupID,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))
	require.Contains(t, err.Error(), "backup pool refill threshold points must be > 0")
	require.Nil(t, repo.created)
}

func TestAdminService_CreateGroup_BackupPoolRejectsNonFiniteThreshold(t *testing.T) {
	backupID := int64(10)
	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run("threshold", func(t *testing.T) {
			repo := &groupRepoStubForInvalidRequestFallback{
				groups: map[int64]*Group{
					backupID: {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
				},
			}
			svc := &adminServiceImpl{groupRepo: repo}

			_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
				Name:                            "g1",
				Platform:                        PlatformOpenAI,
				RateMultiplier:                  1.0,
				BackupPoolGroupID:               &backupID,
				BackupPoolRefillThresholdPoints: threshold,
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "backup pool refill threshold points must be > 0")
			require.Nil(t, repo.created)
		})
	}
}
func TestAdminService_UpdateGroup_BackupPoolRejectsSelf(t *testing.T) {
	existing := &Group{
		ID:       1,
		Name:     "g1",
		Platform: PlatformOpenAI,
		Status:   StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{existing.ID: existing},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	threshold := 100.0
	_, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		BackupPoolGroupID:               &existing.ID,
		BackupPoolRefillThresholdPoints: &threshold,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot set self as backup pool group")
	require.Nil(t, repo.updated)
}

func TestAdminService_UpdateGroup_BackupPoolClearsOnZero(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:                              1,
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		Status:                          StatusActive,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 100,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	clear := int64(0)
	group, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		BackupPoolGroupID: &clear,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Nil(t, repo.updated.BackupPoolGroupID)
	require.Zero(t, repo.updated.BackupPoolRefillThresholdPoints)
}

func TestAdminService_UpdateGroup_BackupPoolClearsStaleDisabledConfigOnUnrelatedUpdate(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:                              1,
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		Status:                          StatusActive,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 0,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		Name: "renamed",
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, "renamed", repo.updated.Name)
	require.Nil(t, repo.updated.BackupPoolGroupID)
	require.Zero(t, repo.updated.BackupPoolRefillThresholdPoints)
}

func TestAdminService_UpdateGroup_BackupPoolClearsStaleInactivePoolOnUnrelatedUpdate(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:                              1,
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		Status:                          StatusActive,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 100,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusDisabled},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		Name: "renamed",
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, "renamed", repo.updated.Name)
	require.Nil(t, repo.updated.BackupPoolGroupID)
	require.Zero(t, repo.updated.BackupPoolRefillThresholdPoints)
}

func TestAdminService_UpdateGroup_BackupPoolKeepsUnknownValidationErrorOnUnrelatedUpdate(t *testing.T) {
	backupID := int64(10)
	dbErr := errors.New("temporary lookup failure")
	existing := &Group{
		ID:                              1,
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		Status:                          StatusActive,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 100,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
		},
		getByIDLiteErrors: map[int64]error{
			backupID: dbErr,
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		Name: "renamed",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, dbErr)
	require.Nil(t, repo.updated)
}

func TestAdminService_UpdateGroup_BackupPoolSetSuccess(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:       1,
		Name:     "g1",
		Platform: PlatformOpenAI,
		Status:   StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	threshold := 150.0
	group, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: &threshold,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, backupID, *repo.updated.BackupPoolGroupID)
	require.Equal(t, threshold, repo.updated.BackupPoolRefillThresholdPoints)
}

func TestAdminService_UpdateGroup_BackupPoolClearsWhenPlatformChangesAwayFromOpenAI(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:                              1,
		Name:                            "g1",
		Platform:                        PlatformOpenAI,
		Status:                          StatusActive,
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: 100,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		Platform: PlatformAnthropic,
	})
	require.NoError(t, err)
	require.NotNil(t, group)
	require.NotNil(t, repo.updated)
	require.Equal(t, PlatformAnthropic, repo.updated.Platform)
	require.Nil(t, repo.updated.BackupPoolGroupID)
	require.Zero(t, repo.updated.BackupPoolRefillThresholdPoints)
}

func TestAdminService_UpdateGroup_BackupPoolRejectsExplicitNonOpenAIPlatform(t *testing.T) {
	backupID := int64(10)
	existing := &Group{
		ID:       1,
		Name:     "g1",
		Platform: PlatformAnthropic,
		Status:   StatusActive,
	}
	repo := &groupRepoStubForInvalidRequestFallback{
		groups: map[int64]*Group{
			existing.ID: existing,
			backupID:    {ID: backupID, Platform: PlatformOpenAI, Status: StatusActive},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo}

	threshold := 100.0
	_, err := svc.UpdateGroup(context.Background(), existing.ID, &UpdateGroupInput{
		BackupPoolGroupID:               &backupID,
		BackupPoolRefillThresholdPoints: &threshold,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "backup pool auto refill only supports openai groups")
	require.Nil(t, repo.updated)
}
