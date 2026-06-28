package service

import (
	"context"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

// HealthChecker 健康检查器，定期 ping 上游并根据结果触发熔断/恢复
type HealthChecker struct {
	groupRepo   GroupRepository
	accountRepo AccountRepository
	upstream    UpstreamPinger
	mu          sync.RWMutex
	stopChan    chan struct{}
}

// UpstreamPinger 上游 ping 接口（由具体的 HTTP 客户端实现）
type UpstreamPinger interface {
	PingAnthropic(ctx context.Context, account *Account) bool
	PingOpenAI(ctx context.Context, account *Account) bool
	PingGemini(ctx context.Context, account *Account) bool
	PingAntigravity(ctx context.Context, account *Account) bool
}

// NewHealthChecker 创建健康检查器实例
func NewHealthChecker(
	groupRepo GroupRepository,
	accountRepo AccountRepository,
	upstream UpstreamPinger,
) *HealthChecker {
	return &HealthChecker{
		groupRepo:   groupRepo,
		accountRepo: accountRepo,
		upstream:    upstream,
		stopChan:    make(chan struct{}),
	}
}

// Start 启动健康检查后台任务
func (hc *HealthChecker) Start(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second) // 每 10 秒扫描一次
	defer ticker.Stop()

	logger.LegacyPrintf("health_checker", "Health checker started")

	for {
		select {
		case <-ctx.Done():
			logger.LegacyPrintf("health_checker", "Health checker stopped by context")
			return
		case <-hc.stopChan:
			logger.LegacyPrintf("health_checker", "Health checker stopped by stop signal")
			return
		case <-ticker.C:
			hc.checkAllGroups(ctx)
		}
	}
}

// Stop 停止健康检查
func (hc *HealthChecker) Stop() {
	close(hc.stopChan)
}

// checkAllGroups 检查所有启用健康检查的 Group
func (hc *HealthChecker) checkAllGroups(ctx context.Context) {
	groups, err := hc.groupRepo.FindByHealthCheckEnabled(ctx, true)
	if err != nil {
		logger.LegacyPrintf("health_checker", "Failed to fetch groups for health check: %v", err)
		return
	}

	if len(groups) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, group := range groups {
		// 判断是否到了检查时间
		if group.HealthLastCheckAt != nil {
			nextCheck := group.HealthLastCheckAt.Add(time.Duration(group.HealthCheckIntervalSec) * time.Second)
			if time.Now().Before(nextCheck) {
				continue
			}
		}

		wg.Add(1)
		go func(g *Group) {
			defer wg.Done()
			hc.checkGroup(ctx, g)
		}(group)
	}
	wg.Wait()
}

// checkGroup 对单个 Group 执行健康检查
func (hc *HealthChecker) checkGroup(ctx context.Context, group *Group) {
	now := time.Now()

	// 从 group 的 AccountGroups 中选择一个 active 账号进行 ping
	// 注意：这里假设 group 已经加载了 AccountGroups，如果没有需要单独查询
	var account *Account
	if len(group.AccountGroups) > 0 {
		// 获取第一个账号组的账号 ID，然后查询账号详情
		acc, err := hc.accountRepo.GetByID(ctx, group.AccountGroups[0].AccountID)
		if err == nil && acc != nil && acc.Status == StatusActive {
			account = acc
		}
	}

	if account == nil {
		logger.LegacyPrintf("health_checker", "No active accounts for health check: group_id=%d", group.ID)
		hc.updateHealthStatus(ctx, group, false, now)
		return
	}

	// 执行 ping
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(group.HealthCheckTimeoutSec)*time.Second)
	defer cancel()

	healthy := hc.pingUpstream(checkCtx, group.Platform, account)
	hc.updateHealthStatus(ctx, group, healthy, now)
}

// pingUpstream 实际 ping 逻辑（根据平台调用不同端点）
func (hc *HealthChecker) pingUpstream(ctx context.Context, platform string, account *Account) bool {
	switch platform {
	case "anthropic":
		return hc.upstream.PingAnthropic(ctx, account)
	case "openai":
		return hc.upstream.PingOpenAI(ctx, account)
	case "gemini":
		return hc.upstream.PingGemini(ctx, account)
	case "antigravity":
		return hc.upstream.PingAntigravity(ctx, account)
	default:
		logger.LegacyPrintf("health_checker", "Unknown platform for health check: %s", platform)
		return false
	}
}

// updateHealthStatus 更新健康状态并应用熔断逻辑
func (hc *HealthChecker) updateHealthStatus(ctx context.Context, group *Group, healthy bool, checkTime time.Time) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	var newStatus string
	var shouldCircuitBreak bool
	var shouldRecover bool

	if healthy {
		group.HealthConsecutiveSuccesses++
		group.HealthConsecutiveFailures = 0

		// 如果之前是 unhealthy，连续成功达到阈值后恢复
		if group.HealthStatus == "unhealthy" && group.HealthConsecutiveSuccesses >= group.HealthCheckSuccessThreshold {
			newStatus = "healthy"
			shouldRecover = true
			logger.LegacyPrintf("health_checker", "Group recovered from unhealthy state: group_id=%d name=%s", group.ID, group.Name)
		} else if group.HealthStatus != "unhealthy" {
			newStatus = "healthy"
		} else {
			newStatus = group.HealthStatus // 保持 unhealthy，等待连续成功
		}
	} else {
		group.HealthConsecutiveFailures++
		group.HealthConsecutiveSuccesses = 0

		// 连续失败达到阈值，触发熔断
		if group.HealthConsecutiveFailures >= group.HealthCheckFailureThreshold {
			newStatus = "unhealthy"
			shouldCircuitBreak = true
			logger.LegacyPrintf("health_checker", "Group circuit breaker triggered: group_id=%d name=%s failures=%d",
				group.ID, group.Name, group.HealthConsecutiveFailures)
		} else {
			newStatus = group.HealthStatus // 保持当前状态
		}
	}

	// 更新数据库
	err := hc.groupRepo.UpdateHealthStatus(ctx, group.ID, &HealthStatusUpdate{
		HealthStatus:               newStatus,
		HealthLastCheckAt:          &checkTime,
		HealthConsecutiveFailures:  group.HealthConsecutiveFailures,
		HealthConsecutiveSuccesses: group.HealthConsecutiveSuccesses,
	})

	if err != nil {
		logger.LegacyPrintf("health_checker", "Failed to update health status: group_id=%d err=%v", group.ID, err)
		return
	}

	// 如果触发熔断，自动将 Group status 设置为 disabled
	if shouldCircuitBreak && group.Status == StatusActive {
		err := hc.groupRepo.UpdateGroupStatus(ctx, group.ID, StatusDisabled)
		if err != nil {
			logger.LegacyPrintf("health_checker", "Failed to disable group via circuit breaker: group_id=%d err=%v", group.ID, err)
		} else {
			logger.LegacyPrintf("health_checker", "Group auto-disabled by circuit breaker: group_id=%d name=%s", group.ID, group.Name)
		}
	}

	// 如果恢复，自动将 Group status 设置为 active
	if shouldRecover && group.Status == StatusDisabled {
		err := hc.groupRepo.UpdateGroupStatus(ctx, group.ID, StatusActive)
		if err != nil {
			logger.LegacyPrintf("health_checker", "Failed to enable group after recovery: group_id=%d err=%v", group.ID, err)
		} else {
			logger.LegacyPrintf("health_checker", "Group auto-enabled after health recovery: group_id=%d name=%s", group.ID, group.Name)
		}
	}
}
