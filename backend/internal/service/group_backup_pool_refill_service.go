package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"
)

const groupBackupPoolRefillInterval = 60 * time.Second

// GroupBackupPoolRefillService periodically copies healthy OpenAI Codex accounts
// from configured backup pool groups into target groups whose remaining Codex
// quota capacity is below the configured threshold.
type GroupBackupPoolRefillService struct {
	accountRepo    AccountRepository
	groupRepo      GroupRepository
	quotaAutoPause OpenAIQuotaAutoPauseSettingsReader
	interval       time.Duration

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	scanMu  sync.Mutex
}

func NewGroupBackupPoolRefillService(accountRepo AccountRepository, groupRepo GroupRepository, quotaAutoPause OpenAIQuotaAutoPauseSettingsReader) *GroupBackupPoolRefillService {
	return &GroupBackupPoolRefillService{
		accountRepo:    accountRepo,
		groupRepo:      groupRepo,
		quotaAutoPause: quotaAutoPause,
		interval:       groupBackupPoolRefillInterval,
	}
}

func ProvideGroupBackupPoolRefillService(accountRepo AccountRepository, groupRepo GroupRepository, quotaAutoPause OpenAIQuotaAutoPauseSettingsReader) *GroupBackupPoolRefillService {
	svc := NewGroupBackupPoolRefillService(accountRepo, groupRepo, quotaAutoPause)
	svc.Start()
	return svc
}

func (s *GroupBackupPoolRefillService) Start() {
	if s == nil || s.accountRepo == nil || s.groupRepo == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.running = true
	stopCh := s.stopCh
	doneCh := s.doneCh
	s.mu.Unlock()

	go s.loop(stopCh, doneCh)
	slog.Info("group_backup_pool_refill.service_started", "interval", s.interval.String())
}

func (s *GroupBackupPoolRefillService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	stopCh := s.stopCh
	doneCh := s.doneCh
	s.running = false
	s.mu.Unlock()

	close(stopCh)
	<-doneCh
	slog.Info("group_backup_pool_refill.service_stopped")
}

func (s *GroupBackupPoolRefillService) loop(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	s.runScanWithTimeout(stopCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			s.runScanWithTimeout(stopCh)
		}
	}
}

func (s *GroupBackupPoolRefillService) runScanWithTimeout(stopCh <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), s.interval)
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-done:
		}
	}()
	defer close(done)
	if err := s.RunOnce(ctx); err != nil {
		slog.Warn("group_backup_pool_refill.scan_failed", "error", err)
	}
}

// RunOnce scans configured active OpenAI groups once. It is exported for tests
// and for explicit maintenance use; normal operation is driven by Start.
func (s *GroupBackupPoolRefillService) RunOnce(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.groupRepo == nil {
		return nil
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	groups, err := s.groupRepo.ListActiveByPlatformLite(ctx, PlatformOpenAI)
	if err != nil {
		return fmt.Errorf("list openai groups: %w", err)
	}
	activeOpenAIGroups := make(map[int64]struct{}, len(groups))
	for i := range groups {
		activeOpenAIGroups[groups[i].ID] = struct{}{}
	}

	var firstErr error
	for i := range groups {
		group := &groups[i]
		if group.BackupPoolGroupID == nil || !backupPoolRefillThresholdEnabled(group.BackupPoolRefillThresholdPoints) {
			continue
		}
		if _, ok := activeOpenAIGroups[*group.BackupPoolGroupID]; !ok {
			slog.Warn(
				"group_backup_pool_refill.backup_group_inactive_or_missing",
				"group_id", group.ID,
				"backup_group_id", *group.BackupPoolGroupID,
			)
			continue
		}
		added, err := s.RefillGroupIfNeeded(ctx, group)
		if err != nil {
			slog.Warn("group_backup_pool_refill.group_failed", "group_id", group.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if added > 0 {
			slog.Info(
				"group_backup_pool_refill.group_refilled",
				"group_id", group.ID,
				"backup_group_id", *group.BackupPoolGroupID,
				"added_accounts", added,
			)
		}
	}
	return firstErr
}

// RefillGroupIfNeeded copies accounts from group.BackupPoolGroupID into group.ID
// until the target group's Codex remaining capacity reaches its threshold, or
// no eligible backup accounts remain.
func (s *GroupBackupPoolRefillService) RefillGroupIfNeeded(ctx context.Context, group *Group) (int, error) {
	if s == nil || group == nil || group.BackupPoolGroupID == nil || !backupPoolRefillThresholdEnabled(group.BackupPoolRefillThresholdPoints) {
		return 0, nil
	}
	if group.Platform != PlatformOpenAI {
		return 0, nil
	}
	if group.ID == *group.BackupPoolGroupID {
		return 0, fmt.Errorf("group %d uses itself as backup pool", group.ID)
	}
	quotaCtx := s.withOpenAIQuotaAutoPauseContext(ctx)

	now := time.Now()
	targetAccounts, err := s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, PlatformOpenAI)
	if err != nil {
		return 0, fmt.Errorf("list target accounts: %w", err)
	}
	parentLookup := s.newBackupPoolShadowParentLookup(ctx)
	currentPoints := sumOpenAICodexRemainingCapacityPoints(quotaCtx, targetAccounts, now, group.RequirePrivacySet, parentLookup)
	if currentPoints >= group.BackupPoolRefillThresholdPoints {
		return 0, nil
	}

	existingIDs, err := s.groupRepo.GetAccountIDsByGroupIDs(ctx, []int64{group.ID})
	if err != nil {
		return 0, fmt.Errorf("list target account ids: %w", err)
	}
	existing := make(map[int64]struct{}, len(existingIDs))
	for _, id := range existingIDs {
		existing[id] = struct{}{}
	}

	backupAccounts, err := s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *group.BackupPoolGroupID, PlatformOpenAI)
	if err != nil {
		return 0, fmt.Errorf("list backup accounts: %w", err)
	}

	selected := make([]int64, 0)
	for i := range backupAccounts {
		account := &backupAccounts[i]
		if _, ok := existing[account.ID]; ok {
			continue
		}
		points, ok := openAICodexRemainingCapacityPointsForGroup(quotaCtx, group, account, now, parentLookup)
		if !ok || points <= 0 {
			continue
		}
		selected = append(selected, account.ID)
		currentPoints += points
		existing[account.ID] = struct{}{}
		if currentPoints >= group.BackupPoolRefillThresholdPoints {
			break
		}
	}
	if len(selected) == 0 {
		return 0, nil
	}
	if err := s.groupRepo.BindAccountsToGroup(ctx, group.ID, selected); err != nil {
		return 0, fmt.Errorf("bind backup pool accounts: %w", err)
	}
	return len(selected), nil
}

func backupPoolRefillThresholdEnabled(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// NormalizeBackupPoolRefillThresholdPoints returns 0 for disabled or non-finite
// backup-pool thresholds so dirty rows never leak unsupported JSON floats.
func NormalizeBackupPoolRefillThresholdPoints(value float64) float64 {
	if !backupPoolRefillThresholdEnabled(value) {
		return 0
	}
	return value
}

func (s *GroupBackupPoolRefillService) withOpenAIQuotaAutoPauseContext(ctx context.Context) context.Context {
	if s == nil || s.quotaAutoPause == nil {
		return ctx
	}
	return WithOpenAIQuotaAutoPauseSettings(ctx, s.quotaAutoPause.GetOpenAIQuotaAutoPauseSettings(ctx))
}

func (s *GroupBackupPoolRefillService) newBackupPoolShadowParentLookup(ctx context.Context) func(int64) *Account {
	if s == nil || s.accountRepo == nil {
		return nil
	}
	cache := make(map[int64]*Account)
	return func(id int64) *Account {
		if id <= 0 {
			return nil
		}
		if account, ok := cache[id]; ok {
			return account
		}
		account, err := s.accountRepo.GetByID(ctx, id)
		if err != nil {
			cache[id] = nil
			return nil
		}
		cache[id] = account
		return account
	}
}

func sumOpenAICodexRemainingCapacityPoints(ctx context.Context, accounts []Account, now time.Time, requirePrivacySet bool, parentLookup func(int64) *Account) float64 {
	var total float64
	for i := range accounts {
		if points, ok := openAICodexRemainingCapacityPointsForGroupOptions(ctx, &accounts[i], now, requirePrivacySet, parentLookup); ok {
			total += points
		}
	}
	return total
}

func openAICodexRemainingCapacityPointsForGroup(ctx context.Context, group *Group, account *Account, now time.Time, parentLookup func(int64) *Account) (float64, bool) {
	requirePrivacySet := group != nil && group.RequirePrivacySet
	return openAICodexRemainingCapacityPointsForGroupOptions(ctx, account, now, requirePrivacySet, parentLookup)
}

func openAICodexRemainingCapacityPointsForGroupOptions(ctx context.Context, account *Account, now time.Time, requirePrivacySet bool, parentLookup func(int64) *Account) (float64, bool) {
	if requirePrivacySet && (account == nil || !account.IsPrivacySet()) {
		return 0, false
	}
	if account != nil && account.IsShadow() && parentLookup == nil {
		return 0, false
	}
	if !parentHealthyForShadow(account, parentLookup) {
		return 0, false
	}
	return openAICodexRemainingCapacityPoints(ctx, account, now)
}

func openAICodexRemainingCapacityPoints(ctx context.Context, account *Account, now time.Time) (float64, bool) {
	if account == nil || !account.IsOpenAIOAuth() || !account.IsSchedulable() {
		return 0, false
	}
	if !openAICodexBackupPoolSupportsRequestModel(ctx, account) {
		return 0, false
	}
	used5h, ok5h := resolveAccountExtraNumber(account.Extra, "codex_5h_used_percent")
	used7d, ok7d := resolveAccountExtraNumber(account.Extra, "codex_7d_used_percent")
	if ok5h && (math.IsNaN(used5h) || math.IsInf(used5h, 0)) {
		ok5h = false
	}
	if ok7d && (math.IsNaN(used7d) || math.IsInf(used7d, 0)) {
		ok7d = false
	}
	if !ok5h && !ok7d {
		// 新加入或长期未承载 Codex 流量的账号可能还没有 quota header 快照；
		// 只要基础调度状态可用，就按一个完整账号计入，避免备用池永远补不进冷账号。
		return 100, true
	}
	if !openAICodexSnapshotFreshForBackupPool(account.Extra, now) {
		// 调度层对陈旧 quota 快照不会继续自动暂停账号；补池容量也按未知但可调度
		// 处理为完整账号，避免旧快照长期把健康账号误判为枯竭。
		return 100, true
	}
	if paused, _ := evaluateOpenAIQuotaAutoPause(ctx, account, now); paused {
		return 0, false
	}

	remaining5h := 100.0
	if ok5h && !openAIQuotaWindowReset(account.Extra, "5h", now) {
		remaining5h = 100 * (1 - clamp01(used5h/100))
	}
	remaining7d := 100.0
	if ok7d && !openAIQuotaWindowReset(account.Extra, "7d", now) {
		remaining7d = 100 * (1 - clamp01(used7d/100))
	}
	points := math.Min(remaining5h, remaining7d)
	if points <= 0 {
		return 0, false
	}
	return points, true
}

func openAICodexBackupPoolSupportsRequestModel(ctx context.Context, account *Account) bool {
	if account == nil {
		return false
	}
	for requestedModel := range codexModelMap {
		if account.IsModelSupported(requestedModel) && account.IsSchedulableForModelWithContext(ctx, requestedModel) {
			return true
		}
	}
	return false
}

func openAICodexSnapshotFreshForBackupPool(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	raw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return false
	}
	updatedAt, err := parseTime(fmt.Sprint(raw))
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) < openAICodexAutoPauseStaleAfter
}
