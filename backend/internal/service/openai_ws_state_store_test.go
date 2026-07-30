package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSStateStore_BindGetDeleteResponseAccount(t *testing.T) {
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(7)

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_abc", 101, time.Minute))

	accountID, err := store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Equal(t, int64(101), accountID)

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_abc"))
	accountID, err = store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestOpenAIWSStateStore_ResponseAccountLocalCacheIsGroupScoped(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	ctx := context.Background()

	require.NoError(t, store.BindResponseAccount(ctx, 7, "resp_shared", 101, time.Minute))

	accountID, err := store.GetResponseAccount(ctx, 7, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(101), accountID)

	accountID, err = store.GetResponseAccount(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Zero(t, accountID, "本地 response 绑定必须按 group 隔离，避免跨组命中")

	require.NoError(t, store.BindResponseAccount(ctx, 8, "resp_shared", 202, time.Minute))
	accountID, err = store.GetResponseAccount(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(202), accountID)

	require.NoError(t, store.DeleteResponseAccount(ctx, 7, "resp_shared"))
	accountID, err = store.GetResponseAccount(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(202), accountID, "删除某个 group 的绑定不应影响其它 group")
}

func TestOpenAIWSStateStore_ResponseConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindResponseConn("resp_conn", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetResponseConn("resp_conn")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponseConn("resp_conn")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_SessionTurnStateTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionTurnState(9, "session_hash_1", "turn_state_1", 30*time.Millisecond)

	state, ok := store.GetSessionTurnState(9, "session_hash_1")
	require.True(t, ok)
	require.Equal(t, "turn_state_1", state)

	// group 隔离
	_, ok = store.GetSessionTurnState(10, "session_hash_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionTurnState(9, "session_hash_1")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_SessionConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionConn(9, "session_hash_conn_1", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetSessionConn(9, "session_hash_conn_1")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	// group 隔离
	_, ok = store.GetSessionConn(10, "session_hash_conn_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionConn(9, "session_hash_conn_1")
	require.False(t, ok)
}

func testOpenAICompatibilityDomain(t *testing.T) OpenAICompatibilityDomain {
	t.Helper()
	fingerprint, err := NewOpenAIUpstreamFingerprint("https://api.example.test/v1/responses", "responses")
	require.NoError(t, err)
	return OpenAICompatibilityDomain{
		Provider:            OpenAIUpstreamProvider(PlatformOpenAI),
		UpstreamFingerprint: fingerprint,
		EffectiveModel:      "gpt-test",
		ContractVersion:     OpenAINativeCompactionContractVersion,
	}
}

type sharedOpenAICompatibilityDomainCache struct {
	mu       sync.Mutex
	bindings map[string]openAICompatibilityDomainBinding
}

type distributedOpenAICompatibilityDomainCacheStub struct {
	*stubGatewayCache
	shared *sharedOpenAICompatibilityDomainCache
}

func (c *distributedOpenAICompatibilityDomainCacheStub) SetOpenAICompatibilityDomain(
	_ context.Context,
	groupID int64,
	bindingKey string,
	domain OpenAICompatibilityDomain,
	ttl time.Duration,
) error {
	if c == nil || c.shared == nil || !domain.Valid() || ttl <= 0 {
		return ErrOpenAICompatibilityDomainUnknown
	}
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	c.shared.bindings[fmt.Sprintf("%d:%s", groupID, bindingKey)] = openAICompatibilityDomainBinding{
		domain: domain, expiresAt: time.Now().Add(ttl),
	}
	return nil
}

func (c *distributedOpenAICompatibilityDomainCacheStub) GetOpenAICompatibilityDomain(
	_ context.Context,
	groupID int64,
	bindingKey string,
) (OpenAICompatibilityDomain, error) {
	if c == nil || c.shared == nil {
		return OpenAICompatibilityDomain{}, ErrOpenAICompatibilityDomainUnknown
	}
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	key := fmt.Sprintf("%d:%s", groupID, bindingKey)
	binding, ok := c.shared.bindings[key]
	if !ok || !time.Now().Before(binding.expiresAt) || !binding.domain.Valid() {
		delete(c.shared.bindings, key)
		return OpenAICompatibilityDomain{}, ErrOpenAICompatibilityDomainUnknown
	}
	return binding.domain, nil
}

func (c *distributedOpenAICompatibilityDomainCacheStub) DeleteOpenAICompatibilityDomain(
	_ context.Context,
	groupID int64,
	bindingKey string,
) error {
	if c == nil || c.shared == nil {
		return nil
	}
	c.shared.mu.Lock()
	delete(c.shared.bindings, fmt.Sprintf("%d:%s", groupID, bindingKey))
	c.shared.mu.Unlock()
	return nil
}

func newDistributedOpenAICompatibilityDomainCachePair() (GatewayCache, GatewayCache) {
	shared := &sharedOpenAICompatibilityDomainCache{bindings: make(map[string]openAICompatibilityDomainBinding)}
	return &distributedOpenAICompatibilityDomainCacheStub{stubGatewayCache: &stubGatewayCache{}, shared: shared},
		&distributedOpenAICompatibilityDomainCacheStub{stubGatewayCache: &stubGatewayCache{}, shared: shared}
}

func TestOpenAIWSStateStore_CompatibilityDomainSurvivesInstanceHandoff(t *testing.T) {
	firstCache, secondCache := newDistributedOpenAICompatibilityDomainCachePair()
	first := NewOpenAIWSStateStore(firstCache)
	second := NewOpenAIWSStateStore(secondCache)
	ctx := context.Background()
	domain := testOpenAICompatibilityDomain(t)

	require.True(t, first.BindResponseDomain(ctx, 7, "resp_handoff", domain, time.Minute))
	require.True(t, first.BindSessionDomain(ctx, 7, "session_handoff", domain, time.Minute))
	responseDomain, ok := second.GetResponseDomain(ctx, 7, "resp_handoff")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(responseDomain))
	sessionDomain, ok := second.GetSessionDomain(ctx, 7, "session_handoff")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(sessionDomain))

	_, ok = second.GetResponseDomain(ctx, 8, "resp_handoff")
	require.False(t, ok)
	_, ok = second.GetSessionDomain(ctx, 8, "session_handoff")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_CompatibilityDomainFailClosed(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	domain := testOpenAICompatibilityDomain(t)
	ctx := context.Background()

	require.False(t, store.BindResponseDomain(ctx, 7, "resp_invalid", OpenAICompatibilityDomain{}, time.Minute))
	_, ok := store.GetResponseDomain(ctx, 7, "resp_invalid")
	require.False(t, ok)
	require.False(t, store.BindSessionDomain(ctx, 7, "session_invalid", OpenAICompatibilityDomain{}, time.Minute))
	_, ok = store.GetSessionDomain(ctx, 7, "session_invalid")
	require.False(t, ok)

	require.True(t, store.BindResponseDomain(ctx, 7, "resp_domain", domain, time.Minute))
	gotResponse, ok := store.GetResponseDomain(ctx, 7, "resp_domain")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(gotResponse))
	_, ok = store.GetResponseDomain(ctx, 8, "resp_domain")
	require.False(t, ok, "response domain must be group scoped")

	require.True(t, store.BindSessionDomain(ctx, 7, "session_domain", domain, time.Minute))
	gotSession, ok := store.GetSessionDomain(ctx, 7, "session_domain")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(gotSession))
	_, ok = store.GetSessionDomain(ctx, 8, "session_domain")
	require.False(t, ok, "session domain must be group scoped")

	require.NoError(t, store.DeleteResponseDomain(ctx, 7, "resp_domain"))
	_, ok = store.GetResponseDomain(ctx, 7, "resp_domain")
	require.False(t, ok)
	require.NoError(t, store.DeleteSessionDomain(ctx, 7, "session_domain"))
	_, ok = store.GetSessionDomain(ctx, 7, "session_domain")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_CompatibilityDomainTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	domain := testOpenAICompatibilityDomain(t)
	ctx := context.Background()
	require.True(t, store.BindResponseDomain(ctx, 9, "resp_ttl", domain, 30*time.Millisecond))
	require.True(t, store.BindSessionDomain(ctx, 9, "session_ttl", domain, 30*time.Millisecond))

	time.Sleep(60 * time.Millisecond)
	_, ok := store.GetResponseDomain(ctx, 9, "resp_ttl")
	require.False(t, ok)
	_, ok = store.GetSessionDomain(ctx, 9, "session_ttl")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_GetResponseAccount_NoStaleAfterCacheMiss(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(17)
	responseID := "resp_cache_stale"
	cacheKey := openAIWSResponseAccountCacheKey(responseID)

	cache.sessionBindings[cacheKey] = 501
	accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, int64(501), accountID)

	delete(cache.sessionBindings, cacheKey)
	accountID, err = store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Zero(t, accountID, "上游缓存失效后不应继续命中本地陈旧映射")
}

func TestOpenAIWSStateStore_MaybeCleanupRemovesExpiredIncrementally(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	expiredAt := time.Now().Add(-time.Minute)
	total := 2048
	store.responseToConnMu.Lock()
	for i := 0; i < total; i++ {
		store.responseToConn[fmt.Sprintf("resp_%d", i)] = openAIWSConnBinding{
			connID:    "conn_incremental",
			expiresAt: expiredAt,
		}
	}
	store.responseToConnMu.Unlock()

	store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
	store.maybeCleanup()

	store.responseToConnMu.RLock()
	remainingAfterFirst := len(store.responseToConn)
	store.responseToConnMu.RUnlock()
	require.Less(t, remainingAfterFirst, total, "单轮 cleanup 应至少有进展")
	require.Greater(t, remainingAfterFirst, 0, "增量清理不要求单轮清空全部键")

	for i := 0; i < 8; i++ {
		store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
		store.maybeCleanup()
	}

	store.responseToConnMu.RLock()
	remaining := len(store.responseToConn)
	store.responseToConnMu.RUnlock()
	require.Zero(t, remaining, "多轮 cleanup 后应逐步清空全部过期键")
}

func TestEnsureBindingCapacity_EvictsOneWhenMapIsFull(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "c", 2)
	bindings["c"] = 3

	require.Len(t, bindings, 2)
	require.Equal(t, 3, bindings["c"])
}

func TestEnsureBindingCapacity_DoesNotEvictWhenUpdatingExistingKey(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "a", 2)
	bindings["a"] = 9

	require.Len(t, bindings, 2)
	require.Equal(t, 9, bindings["a"])
}

type openAIWSStateStoreTimeoutProbeCache struct {
	setHasDeadline    bool
	getHasDeadline    bool
	deleteHasDeadline bool
	setDeadlineDelta  time.Duration
	getDeadlineDelta  time.Duration
	delDeadlineDelta  time.Duration
}

func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionAccountID(ctx context.Context, _ int64, _ string) (int64, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.getHasDeadline = true
		c.getDeadlineDelta = time.Until(deadline)
	}
	return 123, nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionAccountID(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.setHasDeadline = true
		c.setDeadlineDelta = time.Until(deadline)
	}
	return errors.New("set failed")
}

func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) DeleteSessionAccountID(ctx context.Context, _ int64, _ string) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.deleteHasDeadline = true
		c.delDeadlineDelta = time.Until(deadline)
	}
	return nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error) {
	return true, nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	return 0, errors.New("not found")
}

func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	return nil
}

func TestOpenAIWSStateStore_RedisOpsUseShortTimeout(t *testing.T) {
	probe := &openAIWSStateStoreTimeoutProbeCache{}
	store := NewOpenAIWSStateStore(probe)
	ctx := context.Background()
	groupID := int64(5)

	err := store.BindResponseAccount(ctx, groupID, "resp_timeout_probe", 11, time.Minute)
	require.Error(t, err)

	accountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_timeout_probe")
	require.NoError(t, getErr)
	require.Equal(t, int64(11), accountID, "本地缓存命中应优先返回已绑定账号")

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_timeout_probe"))

	require.True(t, probe.setHasDeadline, "SetSessionAccountID 应携带独立超时上下文")
	require.True(t, probe.deleteHasDeadline, "DeleteSessionAccountID 应携带独立超时上下文")
	require.False(t, probe.getHasDeadline, "GetSessionAccountID 本用例应由本地缓存命中，不触发 Redis 读取")
	require.Greater(t, probe.setDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.setDeadlineDelta, 3*time.Second)
	require.Greater(t, probe.delDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.delDeadlineDelta, 3*time.Second)

	probe2 := &openAIWSStateStoreTimeoutProbeCache{}
	store2 := NewOpenAIWSStateStore(probe2)
	accountID2, err2 := store2.GetResponseAccount(ctx, groupID, "resp_cache_only")
	require.NoError(t, err2)
	require.Equal(t, int64(123), accountID2)
	require.True(t, probe2.getHasDeadline, "GetSessionAccountID 在缓存未命中时应携带独立超时上下文")
	require.Greater(t, probe2.getDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe2.getDeadlineDelta, 3*time.Second)
}

func TestWithOpenAIWSStateStoreRedisTimeout_WithParentContext(t *testing.T) {
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
	defer cancel()
	require.NotNil(t, ctx)
	_, ok := ctx.Deadline()
	require.True(t, ok, "应附加短超时")
}
