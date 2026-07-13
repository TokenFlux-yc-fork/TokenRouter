package service

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultGroupHealthIntervalSec      = 60
	defaultGroupHealthTimeoutSec       = 10
	defaultGroupHealthFailureThreshold = 3
	defaultGroupHealthSuccessThreshold = 2
	minGroupHealthIntervalSec          = 10
	maxGroupHealthIntervalSec          = 3600
	minGroupHealthTimeoutSec           = 5
	maxGroupHealthTimeoutSec           = 60
	minGroupHealthThreshold            = 1
	maxGroupHealthThreshold            = 10
)

// GroupHealthMonitor updates probe counters and invalidates cached snapshots
// when a group crosses a healthy/unhealthy boundary.
type GroupHealthMonitor struct {
	groupRepo            GroupRepository
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

func NewGroupHealthMonitor(groupRepo GroupRepository, authCacheInvalidator APIKeyAuthCacheInvalidator) *GroupHealthMonitor {
	return &GroupHealthMonitor{
		groupRepo:            groupRepo,
		authCacheInvalidator: authCacheInvalidator,
	}
}

func (m *GroupHealthMonitor) RecordProbeResult(ctx context.Context, group *Group, success bool, checkedAt time.Time, _ string) error {
	if m == nil || m.groupRepo == nil || group == nil {
		return nil
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	if group.ID > 0 {
		if latest, err := m.groupRepo.GetByIDLite(ctx, group.ID); err == nil && latest != nil {
			group = latest
		}
	}
	if !group.HealthCheckEnabled {
		return nil
	}
	return m.recordResult(ctx, group, success, checkedAt)
}

func (m *GroupHealthMonitor) recordResult(ctx context.Context, group *Group, success bool, checkedAt time.Time) error {
	if group.HealthLastCheckAt != nil && !checkedAt.After(*group.HealthLastCheckAt) {
		return nil
	}
	oldStatus := normalizeGroupHealthStatus(group.HealthStatus)
	failures := group.HealthConsecutiveFailures
	successes := group.HealthConsecutiveSuccesses
	newStatus := oldStatus

	if success {
		successes++
		failures = 0
		if oldStatus == HealthStatusUnhealthy {
			if successes >= normalizeHealthThreshold(group.HealthCheckSuccessThreshold, defaultGroupHealthSuccessThreshold) {
				newStatus = HealthStatusHealthy
			}
		} else {
			newStatus = HealthStatusHealthy
		}
	} else {
		failures++
		successes = 0
		if failures >= normalizeHealthThreshold(group.HealthCheckFailureThreshold, defaultGroupHealthFailureThreshold) {
			newStatus = HealthStatusUnhealthy
		}
	}

	if err := m.groupRepo.UpdateHealthStatus(ctx, group.ID, &HealthStatusUpdate{
		HealthStatus:               newStatus,
		HealthLastCheckAt:          &checkedAt,
		HealthConsecutiveFailures:  failures,
		HealthConsecutiveSuccesses: successes,
	}); err != nil {
		return fmt.Errorf("update group health status: %w", err)
	}
	if newStatus != oldStatus && m.authCacheInvalidator != nil {
		m.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, group.ID)
	}
	group.HealthStatus = newStatus
	group.HealthConsecutiveFailures = failures
	group.HealthConsecutiveSuccesses = successes
	group.HealthLastCheckAt = &checkedAt
	return nil
}

func normalizeHealthThreshold(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
