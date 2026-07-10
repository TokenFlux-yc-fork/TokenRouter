package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type groupHealthRepoStub struct {
	groupRepoNoop
	groups  map[int64]*Group
	updates []groupHealthUpdateCall
}

type groupHealthUpdateCall struct {
	groupID int64
	update  HealthStatusUpdate
}

func (s *groupHealthRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if g, ok := s.groups[id]; ok {
		return g, nil
	}
	return nil, ErrGroupNotFound
}

func (s *groupHealthRepoStub) UpdateHealthStatus(_ context.Context, groupID int64, update *HealthStatusUpdate) error {
	s.updates = append(s.updates, groupHealthUpdateCall{
		groupID: groupID,
		update:  *update,
	})
	return nil
}

type groupHealthAuthCacheInvalidator struct {
	groupIDs []int64
}

func (s *groupHealthAuthCacheInvalidator) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *groupHealthAuthCacheInvalidator) InvalidateAuthCacheByUserID(context.Context, int64) {
}
func (s *groupHealthAuthCacheInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

func TestGroupHealthMonitor_RecordProbeResultCircuitBreaksAndInvalidates(t *testing.T) {
	repo := &groupHealthRepoStub{}
	invalidator := &groupHealthAuthCacheInvalidator{}
	monitor := NewGroupHealthMonitor(repo, invalidator)
	group := &Group{
		ID:                          10,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusHealthy,
		HealthCheckFailureThreshold: 2,
	}
	checkedAt := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)

	require.NoError(t, monitor.RecordProbeResult(context.Background(), group, false, checkedAt, "first failure"))
	require.Equal(t, HealthStatusHealthy, group.HealthStatus)
	require.Equal(t, 1, group.HealthConsecutiveFailures)
	require.Empty(t, invalidator.groupIDs)

	require.NoError(t, monitor.RecordProbeResult(context.Background(), group, false, checkedAt.Add(time.Second), "second failure"))
	require.Equal(t, HealthStatusUnhealthy, group.HealthStatus)
	require.Equal(t, 2, group.HealthConsecutiveFailures)
	require.Equal(t, []int64{10}, invalidator.groupIDs)
	require.Len(t, repo.updates, 2)
	require.Equal(t, HealthStatusUnhealthy, repo.updates[1].update.HealthStatus)
}

func TestGroupHealthMonitor_RecordProbeResultRecoversAfterSuccessThreshold(t *testing.T) {
	repo := &groupHealthRepoStub{}
	invalidator := &groupHealthAuthCacheInvalidator{}
	monitor := NewGroupHealthMonitor(repo, invalidator)
	group := &Group{
		ID:                          11,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusUnhealthy,
		HealthCheckSuccessThreshold: 2,
		HealthConsecutiveFailures:   3,
	}
	checkedAt := time.Date(2026, 7, 3, 2, 0, 0, 0, time.UTC)

	require.NoError(t, monitor.RecordProbeResult(context.Background(), group, true, checkedAt, "first success"))
	require.Equal(t, HealthStatusUnhealthy, group.HealthStatus)
	require.Equal(t, 1, group.HealthConsecutiveSuccesses)
	require.Empty(t, invalidator.groupIDs)

	require.NoError(t, monitor.RecordProbeResult(context.Background(), group, true, checkedAt.Add(time.Second), "second success"))
	require.Equal(t, HealthStatusHealthy, group.HealthStatus)
	require.Equal(t, 2, group.HealthConsecutiveSuccesses)
	require.Equal(t, []int64{11}, invalidator.groupIDs)
	require.Len(t, repo.updates, 2)
	require.Equal(t, HealthStatusHealthy, repo.updates[1].update.HealthStatus)
}

func TestGroupHealthMonitor_RecordProbeResultSkipsStaleCheck(t *testing.T) {
	lastCheckAt := time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC)
	group := &Group{
		ID:                         12,
		HealthCheckEnabled:         true,
		HealthStatus:               HealthStatusHealthy,
		HealthLastCheckAt:          &lastCheckAt,
		HealthConsecutiveFailures:  0,
		HealthConsecutiveSuccesses: 4,
	}
	repo := &groupHealthRepoStub{
		groups: map[int64]*Group{group.ID: group},
	}
	invalidator := &groupHealthAuthCacheInvalidator{}
	monitor := NewGroupHealthMonitor(repo, invalidator)

	require.NoError(t, monitor.RecordProbeResult(context.Background(), group, false, lastCheckAt.Add(-time.Second), "stale failure"))
	require.Empty(t, repo.updates)
	require.Empty(t, invalidator.groupIDs)
	require.Equal(t, HealthStatusHealthy, group.HealthStatus)
	require.Equal(t, 0, group.HealthConsecutiveFailures)
	require.Equal(t, 4, group.HealthConsecutiveSuccesses)
}
