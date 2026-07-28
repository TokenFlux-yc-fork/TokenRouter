package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/domain"
)

var ErrUsageBillingRequestIDRequired = errors.New("usage billing request_id is required")
var ErrUsageBillingRequestConflict = errors.New("usage billing request fingerprint conflict")

// UsageBillingCommand describes one billable request that must be applied at most once.
type UsageBillingCommand struct {
	RequestID          string
	APIKeyID           int64
	RequestFingerprint string
	RequestPayloadHash string

	UserID                          int64
	ActorUserID                     int64
	TeamID                          *int64
	AccountID                       int64
	GroupID                         *int64
	BillableAmountUSD               float64
	BaseAmountUSD                   float64
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	// 批量预占可关闭套餐倍率覆盖，并要求返回后续结算所需的基础金额明细。
	DisablePlanGroupRateMultiplier bool
	IncludeAllocationPricing       bool
	AccountType                    string
	Model                          string
	ServiceTier                    string
	ReasoningEffort                string
	BillingType                    int8
	InputTokens                    int
	OutputTokens                   int
	CacheCreationTokens            int
	CacheReadTokens                int
	ImageCount                     int
	MediaType                      string

	APIKeyQuotaCost     float64
	APIKeyRateLimitCost float64
	AccountQuotaCost    float64
}

func (c *UsageBillingCommand) Normalize() {
	if c == nil {
		return
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	if strings.TrimSpace(c.RequestFingerprint) == "" {
		c.RequestFingerprint = buildUsageBillingFingerprint(c)
	}
}

func buildUsageBillingFingerprint(c *UsageBillingCommand) string {
	if c == nil {
		return ""
	}
	teamID := int64(0)
	if c.TeamID != nil {
		teamID = *c.TeamID
	}
	raw := fmt.Sprintf(
		"%d|%d|%d|%d|%d|%s|%s|%s|%s|%d|%d|%d|%d|%d|%d|%s|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f",
		c.UserID,
		c.ActorUserID,
		teamID,
		c.AccountID,
		c.APIKeyID,
		strings.TrimSpace(c.AccountType),
		strings.TrimSpace(c.Model),
		strings.TrimSpace(c.ServiceTier),
		strings.TrimSpace(c.ReasoningEffort),
		c.BillingType,
		c.InputTokens,
		c.OutputTokens,
		c.CacheCreationTokens,
		c.CacheReadTokens,
		c.ImageCount,
		strings.TrimSpace(c.MediaType),
		c.BillableAmountUSD,
		c.BaseAmountUSD,
		c.SubscriptionRateMultiplier,
		c.SubscriptionRateMultiplierScale,
		c.BalanceRateMultiplier,
		c.APIKeyQuotaCost,
		c.APIKeyRateLimitCost,
		c.AccountQuotaCost,
	)
	if payloadHash := strings.TrimSpace(c.RequestPayloadHash); payloadHash != "" {
		raw += "|" + payloadHash
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func HashUsageRequestPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// AccountQuotaState holds the post-increment quota state returned by the DB transaction.
// All values are post-update (i.e., already include the increment).
type AccountQuotaState struct {
	TotalUsed   float64
	TotalLimit  float64
	DailyUsed   float64
	DailyLimit  float64
	WeeklyUsed  float64
	WeeklyLimit float64
}

type UsageBillingApplyResult struct {
	Applied                 bool
	APIKeyQuotaExhausted    bool
	NewBalance              *float64           // post-deduction balance (nil = no balance deduction)
	QuotaState              *AccountQuotaState // post-increment quota state (nil = no quota increment)
	SubscriptionAmountUSD   float64
	BalanceAmountUSD        float64
	BillingAllocations      []domain.BillingAllocation
	EffectiveRateMultiplier *float64
}

// BatchImageBalanceHoldCommand describes an idempotent balance hold operation.
type BatchImageBalanceHoldCommand struct {
	RequestID          string
	APIKeyID           int64
	RequestFingerprint string
	RequestPayloadHash string
	UserID             int64
	ActorUserID        int64
	TeamID             *int64
	GroupID            *int64
	BatchID            string
	HoldAmount         float64
	ActualAmount       float64
	// 第二版价格快照按基础金额分配，避免订阅与余额共担时沿用同一个倍率。
	PricingSnapshotVersion          int
	BaseAmountUSD                   float64
	ActualBaseAmountUSD             float64
	SubscriptionRateMultiplier      float64
	SubscriptionRateMultiplierScale float64
	BalanceRateMultiplier           float64
	SettlementRateScale             float64
	DisablePlanGroupRateMultiplier  bool
	// BalanceHoldAmount 和 SubscriptionHoldAllocations 是提交时持久化的资金预占快照。
	// 两者都为空时按旧任务处理，视为 HoldAmount 全部来自余额冻结。
	BalanceHoldAmount           float64
	SubscriptionHoldAllocations []domain.BillingAllocation
	// AllowanceReserved 区分新任务预记和滚动升级期间的旧任务。
	AllowanceReserved bool
	// ReservedAt 用于只回退仍属于原窗口的预记额度。
	ReservedAt time.Time
}

func (c *BatchImageBalanceHoldCommand) Normalize() {
	if c == nil {
		return
	}
	c.RequestID = strings.TrimSpace(c.RequestID)
	c.BatchID = strings.TrimSpace(c.BatchID)
	if strings.TrimSpace(c.RequestFingerprint) == "" {
		c.RequestFingerprint = buildBatchImageBalanceHoldFingerprint(c)
	}
}

func buildBatchImageBalanceHoldFingerprint(c *BatchImageBalanceHoldCommand) string {
	if c == nil {
		return ""
	}
	teamID := int64(0)
	if c.TeamID != nil {
		teamID = *c.TeamID
	}
	var raw string
	if c.PricingSnapshotVersion >= 2 {
		raw = fmt.Sprintf(
			"%d|%d|%d|%d|%s|%d|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%0.10f|%t|%s",
			c.UserID,
			c.ActorUserID,
			teamID,
			c.APIKeyID,
			strings.TrimSpace(c.BatchID),
			c.PricingSnapshotVersion,
			c.BaseAmountUSD,
			c.ActualBaseAmountUSD,
			c.SubscriptionRateMultiplier,
			c.SubscriptionRateMultiplierScale,
			c.BalanceRateMultiplier,
			c.SettlementRateScale,
			c.DisablePlanGroupRateMultiplier,
			c.ReservedAt.UTC().Format(time.RFC3339Nano),
		)
	} else {
		raw = fmt.Sprintf(
			"%d|%d|%d|%d|%s|%0.10f|%0.10f|%s",
			c.UserID,
			c.ActorUserID,
			teamID,
			c.APIKeyID,
			strings.TrimSpace(c.BatchID),
			c.HoldAmount,
			c.ActualAmount,
			c.ReservedAt.UTC().Format(time.RFC3339Nano),
		)
	}
	if payloadHash := strings.TrimSpace(c.RequestPayloadHash); payloadHash != "" {
		raw += "|" + payloadHash
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type BatchImageBalanceHoldResult struct {
	Applied               bool
	NewBalance            *float64
	FrozenBalance         *float64
	HoldAmountUSD         float64
	EstimatedAmountUSD    float64
	ActualAmountUSD       float64
	SubscriptionAmountUSD float64
	BalanceAmountUSD      float64
	BillingAllocations    []domain.BillingAllocation
}

// BatchImageBillingCapturePlan 描述批量任务从预占金额收敛到实际金额时的资金拆分。
type BatchImageBillingCapturePlan struct {
	BalanceHoldAmount     float64
	ActualAmountUSD       float64
	SubscriptionAmountUSD float64
	BalanceAmountUSD      float64
	BillingAllocations    []domain.BillingAllocation
	SubscriptionReleases  []domain.BillingAllocation
}

// EffectiveBatchImageBalanceHoldAmount 返回实际冻结余额，并兼容迁移前全额冻结余额的任务。
func EffectiveBatchImageBalanceHoldAmount(cmd *BatchImageBalanceHoldCommand) float64 {
	if cmd == nil {
		return 0
	}
	if cmd.BalanceHoldAmount > 0 || len(cmd.SubscriptionHoldAllocations) > 0 {
		return math.Max(cmd.BalanceHoldAmount, 0)
	}
	return math.Max(cmd.HoldAmount, 0)
}

// TotalBatchImageHoldAmount 返回订阅预占与余额冻结的总和。
func TotalBatchImageHoldAmount(cmd *BatchImageBalanceHoldCommand) float64 {
	if cmd == nil {
		return 0
	}
	total := EffectiveBatchImageBalanceHoldAmount(cmd)
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription && allocation.AmountUSD > 0 {
			total += allocation.AmountUSD
		}
	}
	return total
}

// PlanBatchImageBillingCapture 按订阅优先顺序计算实际结算和应释放的订阅额度。
func PlanBatchImageBillingCapture(cmd *BatchImageBalanceHoldCommand) (*BatchImageBillingCapturePlan, error) {
	plan := &BatchImageBillingCapturePlan{}
	if cmd == nil {
		return plan, nil
	}
	if cmd.PricingSnapshotVersion >= 2 && cmd.BaseAmountUSD > 0 {
		return planBatchImageBaseAmountCapture(cmd)
	}
	actualAmount := math.Max(cmd.ActualAmount, 0)
	remaining := actualAmount
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.AmountUSD <= 0 || allocation.SubscriptionID == nil {
			continue
		}
		kept := math.Min(allocation.AmountUSD, remaining)
		if kept > 0 {
			keptAllocation := cloneBatchImageBillingAllocation(allocation, kept)
			plan.BillingAllocations = append(plan.BillingAllocations, keptAllocation)
			plan.SubscriptionAmountUSD += kept
			remaining -= kept
		}
		if released := allocation.AmountUSD - kept; released > batchImageCostEpsilon {
			plan.SubscriptionReleases = append(plan.SubscriptionReleases, cloneBatchImageBillingAllocation(allocation, released))
		}
	}

	plan.BalanceHoldAmount = EffectiveBatchImageBalanceHoldAmount(cmd)
	if remaining-plan.BalanceHoldAmount > batchImageCostEpsilon {
		return nil, ErrBatchImageSettlementCostExceedsHold
	}
	plan.BalanceAmountUSD = math.Min(remaining, plan.BalanceHoldAmount)
	if plan.BalanceAmountUSD > 0 {
		plan.BillingAllocations = append(plan.BillingAllocations, domain.BillingAllocation{
			Type:      domain.BillingAllocationTypeBalance,
			AmountUSD: plan.BalanceAmountUSD,
		})
	}
	plan.ActualAmountUSD = plan.SubscriptionAmountUSD + plan.BalanceAmountUSD
	return plan, nil
}

// planBatchImageBaseAmountCapture 按预占时记录的各来源倍率覆盖实际基础金额。
func planBatchImageBaseAmountCapture(cmd *BatchImageBalanceHoldCommand) (*BatchImageBillingCapturePlan, error) {
	plan := &BatchImageBillingCapturePlan{BalanceHoldAmount: EffectiveBatchImageBalanceHoldAmount(cmd)}
	remainingBase := math.Max(cmd.ActualBaseAmountUSD, 0)
	settlementScale := math.Max(cmd.SettlementRateScale, 0)
	for _, allocation := range cmd.SubscriptionHoldAllocations {
		if allocation.Type != domain.BillingAllocationTypeSubscription || allocation.AmountUSD <= 0 || allocation.SubscriptionID == nil {
			continue
		}
		rate := allocation.RateMultiplier * settlementScale
		if rate <= 0 {
			continue
		}
		kept := math.Min(allocation.AmountUSD, remainingBase*rate)
		if kept > 0 {
			keptAllocation := cloneBatchImageBillingAllocation(allocation, kept)
			keptAllocation.BaseAmountUSD = kept / rate
			keptAllocation.RateMultiplier = rate
			plan.BillingAllocations = append(plan.BillingAllocations, keptAllocation)
			plan.SubscriptionAmountUSD += kept
			remainingBase -= kept / rate
		}
		if released := allocation.AmountUSD - kept; released > batchImageCostEpsilon {
			plan.SubscriptionReleases = append(plan.SubscriptionReleases, cloneBatchImageBillingAllocation(allocation, released))
		}
	}

	balanceRate := math.Max(cmd.BalanceRateMultiplier, 0) * settlementScale
	balanceAmount := remainingBase * balanceRate
	if balanceAmount-plan.BalanceHoldAmount > batchImageCostEpsilon {
		return nil, ErrBatchImageSettlementCostExceedsHold
	}
	plan.BalanceAmountUSD = math.Min(balanceAmount, plan.BalanceHoldAmount)
	if plan.BalanceAmountUSD > 0 {
		baseAmount := 0.0
		if balanceRate > 0 {
			baseAmount = plan.BalanceAmountUSD / balanceRate
		}
		plan.BillingAllocations = append(plan.BillingAllocations, domain.BillingAllocation{
			Type:           domain.BillingAllocationTypeBalance,
			AmountUSD:      plan.BalanceAmountUSD,
			BaseAmountUSD:  baseAmount,
			RateMultiplier: balanceRate,
		})
		remainingBase -= baseAmount
	}
	if remainingBase > batchImageCostEpsilon && balanceRate > 0 {
		return nil, ErrBatchImageSettlementCostExceedsHold
	}
	plan.ActualAmountUSD = plan.SubscriptionAmountUSD + plan.BalanceAmountUSD
	return plan, nil
}

func cloneBatchImageBillingAllocation(allocation domain.BillingAllocation, amount float64) domain.BillingAllocation {
	cloned := allocation
	cloned.AmountUSD = amount
	if allocation.SubscriptionID != nil {
		value := *allocation.SubscriptionID
		cloned.SubscriptionID = &value
	}
	if allocation.PlanID != nil {
		value := *allocation.PlanID
		cloned.PlanID = &value
	}
	return cloned
}

type UsageBillingRepository interface {
	Apply(ctx context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error)
	ReserveBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error)
	CaptureBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error)
	ReleaseBatchImageBalance(ctx context.Context, cmd *BatchImageBalanceHoldCommand) (*BatchImageBalanceHoldResult, error)
}
