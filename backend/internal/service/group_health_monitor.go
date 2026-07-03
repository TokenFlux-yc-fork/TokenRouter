package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// GroupHealthMonitor updates group health counters and invalidates request-time
// auth snapshots when a group crosses healthy/unhealthy boundaries.
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

func (m *GroupHealthMonitor) RecordProbeResult(ctx context.Context, group *Group, success bool, checkedAt time.Time, message string) error {
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

func (m *GroupHealthMonitor) RecordPassiveAccountFailure(ctx context.Context, account *Account, statusCode int, message string) error {
	if m == nil || m.groupRepo == nil || account == nil || !account.IsUpstreamPoolHealthTarget() {
		return nil
	}
	if !shouldRecordPassiveGroupHealthFailure(account, statusCode) {
		return nil
	}

	groupIDs := accountHealthGroupIDs(account)
	if len(groupIDs) == 0 {
		return nil
	}

	var firstErr error
	now := time.Now()
	for _, groupID := range groupIDs {
		group, err := m.groupRepo.GetByIDLite(ctx, groupID)
		if err != nil || group == nil {
			if firstErr == nil && err != nil {
				firstErr = err
			}
			continue
		}
		if !group.HealthCheckEnabled {
			continue
		}
		if err := m.recordResult(ctx, group, false, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
	if newStatus == "" {
		newStatus = HealthStatusUnknown
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

func shouldRecordPassiveGroupHealthFailure(account *Account, statusCode int) bool {
	if account == nil || !account.IsUpstreamPoolHealthTarget() {
		return false
	}
	if statusCode == 0 || statusCode >= http.StatusInternalServerError {
		return true
	}
	if account.Type == AccountTypeUpstream && isPoolModeRetryableStatus(statusCode) {
		return true
	}
	if account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode) {
		return true
	}
	return false
}

func accountHealthGroupIDs(account *Account) []int64 {
	if account == nil {
		return nil
	}
	seen := make(map[int64]struct{})
	add := func(id int64) {
		if id <= 0 {
			return
		}
		seen[id] = struct{}{}
	}
	for _, id := range account.GroupIDs {
		add(id)
	}
	for i := range account.Groups {
		if account.Groups[i] != nil {
			add(account.Groups[i].ID)
		}
	}
	for i := range account.AccountGroups {
		add(account.AccountGroups[i].GroupID)
		if account.AccountGroups[i].Group != nil {
			add(account.AccountGroups[i].Group.ID)
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

func passiveHealthErrorMessage(statusCode int, body []byte) string {
	message := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if message == "" && len(body) > 0 {
		message = string(body)
	}
	message = sanitizeUpstreamErrorMessage(message)
	if statusCode > 0 {
		if message == "" {
			return fmt.Sprintf("upstream status %d", statusCode)
		}
		return fmt.Sprintf("upstream status %d: %s", statusCode, truncateString(message, 512))
	}
	if message == "" {
		return "upstream request failed"
	}
	return truncateString(message, 512)
}
