package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/google/uuid"
)

var ErrOpenAINativeCompactionProbeIdentityInvalid = errors.New("openai native compaction probe isolated identity is invalid")

const openAINativeCompactionProbeCostSafetyBPS int64 = 12_500

type openAINativeCompactionProbeAccountRepository interface {
	GetByID(context.Context, int64) (*Account, error)
	ListByGroup(context.Context, int64) ([]Account, error)
}

type openAINativeCompactionProbeAPIKeyRepository interface {
	GetByID(context.Context, int64) (*APIKey, error)
}

type openAINativeCompactionProbeUserRepository interface {
	GetByID(context.Context, int64) (*User, error)
}

type openAINativeCompactionProbeGroupRepository interface {
	GetByID(context.Context, int64) (*Group, error)
}

type openAINativeCompactionProbeExecutor interface {
	RunOpenAINativeCompactionV2Probe(
		context.Context,
		*Account,
		string,
		OpenAINativeCompactionProbeOptions,
	) (OpenAINativeCompactionProbeAttemptResult, error)
}

// OpenAINativeCompactionProbeRunnerService owns bounded candidate enrollment,
// leasing, probing, and payload-free result persistence. Cross-process
// deduplication is provided by database claims; this service deliberately does
// not depend on the billing probe leader lock.
type OpenAINativeCompactionProbeRunnerService struct {
	accountRepo    openAINativeCompactionProbeAccountRepository
	apiKeyRepo     openAINativeCompactionProbeAPIKeyRepository
	userRepo       openAINativeCompactionProbeUserRepository
	groupRepo      openAINativeCompactionProbeGroupRepository
	capabilityRepo OpenAINativeCompactionCapabilityRepository
	executor       openAINativeCompactionProbeExecutor
	budgetRepo     OpenAINativeCompactionProbeBudgetRepository
	probeConfig    config.GatewayOpenAINativeCompactionProbeConfig

	parentCtx    context.Context
	parentCancel context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	cycleMu      sync.Mutex
	started      bool
	stopped      bool
	workerID     string
	probeSlots   chan struct{}
	stageBudget  *OpenAIStageBudget
	allowlist    map[string]struct{}
	now          func() time.Time
	jitter       func(time.Duration, float64) time.Duration
}

func NewOpenAINativeCompactionProbeRunnerService(
	accountRepo AccountRepository,
	apiKeyRepo APIKeyRepository,
	userRepo UserRepository,
	groupRepo GroupRepository,
	capabilityRepo OpenAINativeCompactionCapabilityRepository,
	gateway *OpenAIGatewayService,
	cfg *config.Config,
) *OpenAINativeCompactionProbeRunnerService {
	var probeConfig config.GatewayOpenAINativeCompactionProbeConfig
	if cfg != nil {
		probeConfig = cfg.Gateway.OpenAINativeCompactionProbe
	}
	service := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		apiKeyRepo,
		userRepo,
		groupRepo,
		capabilityRepo,
		gateway,
		probeConfig,
	)
	if gateway != nil {
		service.budgetRepo = gateway.openAIProbeBudgetRepo
	}
	return service
}

func newOpenAINativeCompactionProbeRunnerService(
	accountRepo openAINativeCompactionProbeAccountRepository,
	apiKeyRepo openAINativeCompactionProbeAPIKeyRepository,
	userRepo openAINativeCompactionProbeUserRepository,
	groupRepo openAINativeCompactionProbeGroupRepository,
	capabilityRepo OpenAINativeCompactionCapabilityRepository,
	executor openAINativeCompactionProbeExecutor,
	probeConfig config.GatewayOpenAINativeCompactionProbeConfig,
) *OpenAINativeCompactionProbeRunnerService {
	ctx, cancel := context.WithCancel(context.Background())
	workers := probeConfig.MaxWorkers
	if workers < 1 {
		workers = 1
	}
	stageLimit := probeConfig.MaxResponseBytes
	if stageLimit < 1 {
		stageLimit = openAINativeCompactionProbeMaxBytes
	}
	if workers > 1 && stageLimit <= math.MaxInt64/int64(workers) {
		stageLimit *= int64(workers)
	}
	allowlist := make(map[string]struct{}, len(probeConfig.ModelAllowlist))
	for _, model := range probeConfig.ModelAllowlist {
		model = strings.TrimSpace(model)
		if model != "" {
			allowlist[model] = struct{}{}
		}
	}
	return &OpenAINativeCompactionProbeRunnerService{
		accountRepo:    accountRepo,
		apiKeyRepo:     apiKeyRepo,
		userRepo:       userRepo,
		groupRepo:      groupRepo,
		capabilityRepo: capabilityRepo,
		executor:       executor,
		probeConfig:    probeConfig,
		parentCtx:      ctx,
		parentCancel:   cancel,
		workerID:       "openai-native-compaction-probe:" + uuid.NewString(),
		probeSlots:     make(chan struct{}, workers),
		stageBudget:    NewOpenAIStageBudget(stageLimit),
		allowlist:      allowlist,
		now:            time.Now,
		jitter:         jitterOpenAINativeCompactionProbeBackoff,
	}
}

func (s *OpenAINativeCompactionProbeRunnerService) Start() {
	if s == nil || !s.probeConfig.Enabled {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runLoop()
}

func (s *OpenAINativeCompactionProbeRunnerService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.parentCancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *OpenAINativeCompactionProbeRunnerService) runLoop() {
	defer s.wg.Done()
	if err := s.RunDue(s.parentCtx); err != nil && !errors.Is(err, context.Canceled) {
		logger.LegacyPrintf("service.openai_native_compaction_probe", "run_due_failed: err=%v", err)
	}
	ticker := time.NewTicker(time.Duration(s.probeConfig.TickIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(s.parentCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LegacyPrintf("service.openai_native_compaction_probe", "run_due_failed: err=%v", err)
			}
		}
	}
}

// RunDue performs one in-process serialized cycle. It first enrolls all exact
// candidates from the isolated group, then claims due rows with database
// leases. This order avoids a fresh-database claim/enrollment cycle.
func (s *OpenAINativeCompactionProbeRunnerService) RunDue(ctx context.Context) error {
	if s == nil || !s.probeConfig.Enabled {
		return nil
	}
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validateDependencies(); err != nil {
		return err
	}
	if s.budgetRepo != nil {
		if _, err := s.budgetRepo.ReapExpired(ctx, max(1, s.probeConfig.ClaimLimit)); err != nil {
			return fmt.Errorf("reap expired openai native compaction probe reservations: %w", err)
		}
	}
	if err := s.validateIsolatedAPIKey(ctx); err != nil {
		return err
	}
	now := s.now()
	if err := s.enrollCandidates(ctx, now); err != nil {
		return err
	}
	// Enrollment can perform multiple DB round trips. Refresh the claim clock so
	// leases are never shortened by work completed before ClaimDue.
	claimNow := s.now()
	claims, err := s.capabilityRepo.ClaimDue(
		ctx,
		claimNow,
		claimNow.Add(time.Duration(s.probeConfig.ClaimTTLSeconds)*time.Second),
		s.workerID,
		s.probeConfig.ClaimLimit,
	)
	if err != nil {
		return fmt.Errorf("claim openai native compaction probes: %w", err)
	}

	var wg sync.WaitGroup
	for i := range claims {
		claim := claims[i]
		if err := acquireOpenAINativeCompactionProbeSlot(ctx, s.probeSlots); err != nil {
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-s.probeSlots }()
			if err := s.runClaim(ctx, claim); err != nil && !errors.Is(err, context.Canceled) {
				logger.LegacyPrintf(
					"service.openai_native_compaction_probe",
					"claim_failed: account_id=%d error_class=%s",
					claim.Capability.Key.AccountID,
					openAINativeCompactionProbeClaimErrorClass(err),
				)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func openAINativeCompactionProbeClaimErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrOpenAINativeCompactionProbeIdentityInvalid):
		return "identity_invalid"
	case errors.Is(err, ErrOpenAINativeCompactionProbeClaimLost):
		return "claim_lost"
	case errors.Is(err, ErrOpenAINativeCompactionProbeEndpoint):
		return "endpoint_invalid"
	case errors.Is(err, ErrOpenAINativeCompactionProbePriceUnavailable):
		return "price_unavailable"
	case errors.Is(err, ErrOpenAINativeCompactionProbeCostLimit):
		return "per_run_cost_limit"
	case errors.Is(err, ErrOpenAINativeCompactionProbeBudgetExceeded):
		return "daily_budget_exceeded"
	case errors.Is(err, ErrOpenAINativeCompactionProbeBudgetUnavailable):
		return "budget_unavailable"
	case errors.Is(err, ErrOpenAINativeCompactionProbeBudgetConflict):
		return "budget_conflict"
	case errors.Is(err, ErrOpenAINativeCompactionProbeBudgetInvalidState):
		return "budget_invalid_state"
	case errors.Is(err, ErrOpenAINativeCompactionStageBytes),
		errors.Is(err, ErrOpenAINativeCompactionStageEvents),
		errors.Is(err, ErrOpenAINativeCompactionStageDuration),
		errors.Is(err, ErrOpenAINativeCompactionStageBudget),
		errors.Is(err, ErrOpenAINativeCompactionProbeEventBytes),
		errors.Is(err, ErrOpenAINativeCompactionProbeResponseBytes):
		return "resource_limit"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "transport_error"
	}
	return "internal_error"
}

func (s *OpenAINativeCompactionProbeRunnerService) validateDependencies() error {
	if s.accountRepo == nil || s.apiKeyRepo == nil || s.userRepo == nil || s.groupRepo == nil || s.capabilityRepo == nil || s.executor == nil {
		return errors.New("openai native compaction probe runner dependencies are unavailable")
	}
	if len(s.allowlist) == 0 {
		return errors.New("openai native compaction probe model allowlist is empty")
	}
	return nil
}

// validateIsolatedAPIKey validates an authorization sentinel / kill switch.
// The sentinel is never used as the candidate account's upstream credential.
func (s *OpenAINativeCompactionProbeRunnerService) validateIsolatedAPIKey(ctx context.Context) error {
	apiKey, err := s.apiKeyRepo.GetByID(ctx, s.probeConfig.IsolatedAPIKeyID)
	if err != nil {
		return fmt.Errorf("load openai native compaction probe authorization sentinel: %w", err)
	}
	if apiKey == nil || apiKey.ID != s.probeConfig.IsolatedAPIKeyID ||
		!validOpenAINativeCompactionProbeAPIKey(apiKey, s.probeConfig.IsolatedGroupID, s.now()) {
		return ErrOpenAINativeCompactionProbeIdentityInvalid
	}
	user, err := s.userRepo.GetByID(ctx, apiKey.UserID)
	if err != nil {
		return fmt.Errorf("load openai native compaction probe sentinel owner: %w", err)
	}
	group, err := s.groupRepo.GetByID(ctx, s.probeConfig.IsolatedGroupID)
	if err != nil {
		return fmt.Errorf("load openai native compaction probe isolated group: %w", err)
	}
	if user == nil || user.ID != apiKey.UserID || !user.IsActive() || user.DeletedAt != nil ||
		group == nil || group.ID != s.probeConfig.IsolatedGroupID || !group.IsActive() {
		return ErrOpenAINativeCompactionProbeIdentityInvalid
	}
	return nil
}

func validOpenAINativeCompactionProbeAPIKey(apiKey *APIKey, groupID int64, now time.Time) bool {
	if apiKey == nil || apiKey.ID <= 0 || apiKey.UserID <= 0 || !apiKey.IsActive() || apiKey.IsComposite || apiKey.GroupID == nil || *apiKey.GroupID != groupID {
		return false
	}
	if apiKey.ExpiresAt != nil && !now.Before(*apiKey.ExpiresAt) {
		return false
	}
	return !apiKey.IsQuotaExhausted()
}

func (s *OpenAINativeCompactionProbeRunnerService) enrollCandidates(ctx context.Context, dueAt time.Time) error {
	accounts, err := s.accountRepo.ListByGroup(ctx, s.probeConfig.IsolatedGroupID)
	if err != nil {
		return fmt.Errorf("list openai native compaction probe accounts: %w", err)
	}
	for i := range accounts {
		account := &accounts[i]
		if !s.accountEligible(account) {
			continue
		}
		seen := make(map[OpenAINativeCompactionCapabilityKey]struct{}, len(s.probeConfig.ModelAllowlist))
		for _, requestedModel := range s.probeConfig.ModelAllowlist {
			requestedModel = strings.TrimSpace(requestedModel)
			if requestedModel == "" {
				continue
			}
			key, err := ResolveOpenAINativeCompactionProbeKey(account, requestedModel)
			if err != nil || !s.modelAllowed(key.EffectiveModel) {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if err := ValidateOpenAINativeCompactionProbeEndpoint(account); err != nil {
				continue
			}
			if _, err := s.capabilityRepo.EnsureAutoCandidate(ctx, key, dueAt); err != nil {
				return fmt.Errorf("ensure openai native compaction probe candidate: %w", err)
			}
		}
	}
	return nil
}

func (s *OpenAINativeCompactionProbeRunnerService) runClaim(ctx context.Context, claim OpenAINativeCompactionProbeClaim) error {
	checkedAt := s.now()
	if err := s.validateIsolatedAPIKey(ctx); err != nil {
		return s.persistInconclusiveClaim(ctx, claim, checkedAt, OpenAINativeCompactionTransportFailure, nil, err)
	}
	account, err := s.accountRepo.GetByID(ctx, claim.Capability.Key.AccountID)
	if err != nil {
		return s.persistInconclusiveClaim(ctx, claim, checkedAt, OpenAINativeCompactionTransportFailure, nil, err)
	}
	if !s.accountEligible(account) {
		return s.persistInconclusiveClaim(ctx, claim, checkedAt, OpenAINativeCompactionTransportFailure, nil, ErrOpenAINativeCompactionProbeIdentityInvalid)
	}
	requestedModel, keyErr := s.resolveClaimRequestedModel(account, claim.Capability.Key)
	if keyErr != nil {
		return s.persistInconclusiveClaim(ctx, claim, checkedAt, OpenAINativeCompactionTransportFailure, nil, keyErr)
	}
	if err := ValidateOpenAINativeCompactionProbeEndpoint(account); err != nil {
		return s.persistInconclusiveClaim(ctx, claim, checkedAt, OpenAINativeCompactionTransportFailure, nil, err)
	}
	attempt, probeErr := s.executor.RunOpenAINativeCompactionV2Probe(
		ctx,
		account,
		requestedModel,
		OpenAINativeCompactionProbeOptions{
			Timeout:                 time.Duration(s.probeConfig.RequestTimeoutSeconds) * time.Second,
			MaxBytes:                s.probeConfig.MaxResponseBytes,
			MaxEventBytes:           minInt64AsInt(s.probeConfig.MaxResponseBytes, openAINativeCompactionProbeMaxEventBytes),
			MaxEvents:               s.probeConfig.MaxEvents,
			MaxOutputTokens:         s.probeConfig.MaxOutputTokens,
			PerRunCostLimitMicroUSD: s.probeConfig.MaxCostPerRunMicroUSD,
			DailyCostLimitMicroUSD:  s.probeConfig.MaxCostPerDayMicroUSD,
			CostSafetyBPS:           openAINativeCompactionProbeCostSafetyBPS,
			ReservationID:           uuid.NewString(),
			DispatchFence: OpenAINativeCompactionProbeDispatchFence{
				Claim:            claim,
				IsolatedGroupID:  s.probeConfig.IsolatedGroupID,
				IsolatedAPIKeyID: s.probeConfig.IsolatedAPIKeyID,
			},
			StageBudget: s.stageBudget,
			Now:         s.now,
		},
	)
	if attempt.CheckedAt.IsZero() {
		attempt.CheckedAt = checkedAt
	}
	if attempt.SemanticOutcome == "" {
		attempt.SemanticOutcome = OpenAINativeCompactionTransportFailure
	}
	if attempt.Key.Valid() && attempt.Key != claim.Capability.Key {
		probeErr = errors.Join(probeErr, errors.New("openai native compaction probe returned a different capability key"))
		attempt.Supported = nil
	}
	nextProbeAt := s.nextProbeAt(claim, attempt)
	_, persistErr := s.capabilityRepo.UpsertProbeResult(ctx, OpenAINativeCompactionProbeResult{
		Key:                          claim.Capability.Key,
		ClaimToken:                   claim.ClaimToken,
		AccountRevision:              claim.AccountRevision,
		Supported:                    attempt.Supported,
		SemanticOutcome:              attempt.SemanticOutcome,
		StatusCode:                   attempt.StatusCode,
		CheckedAt:                    attempt.CheckedAt,
		NextProbeAt:                  &nextProbeAt,
		LastSemanticFailure:          semanticFailureForProbe(attempt),
		AuthorizationPrincipalSHA256: attempt.AuthorizationPrincipalSHA256,
	})
	if persistErr != nil {
		return fmt.Errorf("persist openai native compaction probe result: %w", persistErr)
	}
	return probeErr
}

func (s *OpenAINativeCompactionProbeRunnerService) persistInconclusiveClaim(
	ctx context.Context,
	claim OpenAINativeCompactionProbeClaim,
	checkedAt time.Time,
	outcome OpenAINativeCompactionOutcome,
	statusCode *int,
	cause error,
) error {
	attempt := OpenAINativeCompactionProbeAttemptResult{
		Key:             claim.Capability.Key,
		SemanticOutcome: outcome,
		StatusCode:      statusCode,
		CheckedAt:       checkedAt,
	}
	nextProbeAt := s.nextProbeAt(claim, attempt)
	_, err := s.capabilityRepo.UpsertProbeResult(ctx, OpenAINativeCompactionProbeResult{
		Key:             claim.Capability.Key,
		ClaimToken:      claim.ClaimToken,
		AccountRevision: claim.AccountRevision,
		SemanticOutcome: outcome,
		StatusCode:      statusCode,
		CheckedAt:       checkedAt,
		NextProbeAt:     &nextProbeAt,
	})
	if err != nil {
		return fmt.Errorf("persist inconclusive openai native compaction probe result: %w", err)
	}
	return cause
}

func (s *OpenAINativeCompactionProbeRunnerService) nextProbeAt(
	claim OpenAINativeCompactionProbeClaim,
	attempt OpenAINativeCompactionProbeAttemptResult,
) time.Time {
	checkedAt := attempt.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}
	var delay time.Duration
	switch {
	case attempt.Supported != nil && *attempt.Supported:
		delay = time.Duration(s.probeConfig.SuccessReprobeMinutes) * time.Minute
	case attempt.Supported != nil:
		delay = time.Duration(s.probeConfig.UnsupportedReprobeMinutes) * time.Minute
	default:
		delay = exponentialOpenAINativeCompactionProbeBackoff(
			time.Duration(s.probeConfig.RetryInitialSeconds)*time.Second,
			time.Duration(s.probeConfig.RetryMaxSeconds)*time.Second,
			claim.Capability.CheckedAt,
			claim.Capability.NextProbeAt,
		)
		delay = s.jitter(delay, s.probeConfig.RetryJitterRatio)
	}
	deadline := checkedAt.Add(delay)
	if attempt.RetryAfterUntil != nil && attempt.RetryAfterUntil.After(deadline) {
		deadline = *attempt.RetryAfterUntil
	}
	return deadline
}

func (s *OpenAINativeCompactionProbeRunnerService) accountEligible(account *Account) bool {
	if account == nil || account.ParentAccountID != nil || !account.IsSchedulable() || account.Platform != PlatformOpenAI || !accountInGroup(account, s.probeConfig.IsolatedGroupID) {
		return false
	}
	return account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses)
}

func accountInGroup(account *Account, groupID int64) bool {
	if account == nil || groupID <= 0 {
		return false
	}
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, relation := range account.AccountGroups {
		if relation.GroupID == groupID {
			return true
		}
	}
	return false
}

func (s *OpenAINativeCompactionProbeRunnerService) modelAllowed(model string) bool {
	_, ok := s.allowlist[strings.TrimSpace(model)]
	return ok
}

func (s *OpenAINativeCompactionProbeRunnerService) resolveClaimRequestedModel(
	account *Account,
	claimedKey OpenAINativeCompactionCapabilityKey,
) (string, error) {
	for _, requestedModel := range s.probeConfig.ModelAllowlist {
		requestedModel = strings.TrimSpace(requestedModel)
		if requestedModel == "" {
			continue
		}
		key, err := ResolveOpenAINativeCompactionProbeKey(account, requestedModel)
		if err == nil && key == claimedKey && s.modelAllowed(key.EffectiveModel) {
			return requestedModel, nil
		}
	}
	return "", errors.New("openai native compaction probe capability identity changed")
}

func semanticFailureForProbe(attempt OpenAINativeCompactionProbeAttemptResult) string {
	if attempt.Supported == nil || *attempt.Supported || attempt.SemanticOutcome == OpenAINativeCompactionValid {
		return ""
	}
	return string(attempt.SemanticOutcome)
}

func acquireOpenAINativeCompactionProbeSlot(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func exponentialOpenAINativeCompactionProbeBackoff(
	initial, maximum time.Duration,
	checkedAt, nextProbeAt *time.Time,
) time.Duration {
	if initial <= 0 {
		initial = time.Second
	}
	if maximum < initial {
		maximum = initial
	}
	previous := time.Duration(0)
	if checkedAt != nil && nextProbeAt != nil && nextProbeAt.After(*checkedAt) {
		previous = nextProbeAt.Sub(*checkedAt)
	}
	if previous < initial {
		return initial
	}
	if previous >= maximum/2 {
		return maximum
	}
	return minDuration(previous*2, maximum)
}

func jitterOpenAINativeCompactionProbeBackoff(delay time.Duration, ratio float64) time.Duration {
	if delay <= 0 || ratio <= 0 {
		return delay
	}
	if ratio > 1 {
		ratio = 1
	}
	window := time.Duration(float64(delay) * ratio)
	if window <= 0 {
		return delay
	}
	delta := time.Duration(rand.Int64N(int64(window)*2+1)) - window
	if delay+delta <= 0 {
		return time.Nanosecond
	}
	return delay + delta
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func minInt64AsInt(a int64, b int) int {
	if a > 0 && a < int64(b) {
		return int(a)
	}
	return b
}
