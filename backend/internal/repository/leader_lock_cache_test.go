package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newLeaderLockTestCache(t *testing.T) (*leaderLockCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &leaderLockCache{rdb: rdb}, mr
}

func TestLeaderLockCache_AcquireContendedRelease(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "dashboard:aggregation:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, cache.ReleaseLeaderLock(ctx, key, "A"))

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestLeaderLockCache_ReleaseIsCompareAndDelete(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "payment:order:expiry:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// 模拟 A 的锁过期后，实例 B 已重新获取同一个锁。
	require.NoError(t, cache.rdb.Set(ctx, leaderLockKeyPrefix+key, "B", time.Minute).Err())

	require.NoError(t, cache.ReleaseLeaderLock(ctx, key, "A"))

	val, err := cache.rdb.Get(ctx, leaderLockKeyPrefix+key).Result()
	require.NoError(t, err)
	require.Equal(t, "B", val)
}

func TestLeaderLockCache_TTLExpires(t *testing.T) {
	cache, mr := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "subscription:expiry:reminder:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(2 * time.Minute)

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestFencedLeaderLeaseCache_AcquireRenewAndInspect(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "scheduled-test-runner"

	token, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(1), token)

	lease, err := cache.GetFencedLeaderLease(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "instance-a", lease.Owner)
	require.Equal(t, token, lease.FencingToken)
	require.Positive(t, lease.TTL)

	renewed, err := cache.RenewFencedLeaderLease(ctx, key, "instance-a", token, 2*time.Minute)
	require.NoError(t, err)
	require.True(t, renewed)

	lease, err = cache.GetFencedLeaderLease(ctx, key)
	require.NoError(t, err)
	require.Greater(t, lease.TTL, time.Minute)
}

func TestFencedLeaderLeaseCache_ContendedTakeoverIncrementsToken(t *testing.T) {
	cache, mr := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "scheduled-test-runner"

	firstToken, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	observedToken, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-b", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Equal(t, firstToken, observedToken)

	mr.FastForward(2 * time.Minute)
	secondToken, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Greater(t, secondToken, firstToken)

	renewed, err := cache.RenewFencedLeaderLease(ctx, key, "instance-a", firstToken, time.Minute)
	require.NoError(t, err)
	require.False(t, renewed)

	released, err := cache.ReleaseFencedLeaderLease(ctx, key, "instance-a", firstToken)
	require.NoError(t, err)
	require.False(t, released)
	lease, err := cache.GetFencedLeaderLease(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "instance-b", lease.Owner)
	require.Equal(t, secondToken, lease.FencingToken)
}

func TestFencedLeaderLeaseCache_ReleaseAllowsImmediateFencedHandoff(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "scheduled-test-runner"

	firstToken, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-a", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	released, err := cache.ReleaseFencedLeaderLease(ctx, key, "instance-a", firstToken)
	require.NoError(t, err)
	require.True(t, released)

	secondToken, acquired, err := cache.TryAcquireFencedLeaderLease(ctx, key, "instance-b", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Greater(t, secondToken, firstToken)
}
