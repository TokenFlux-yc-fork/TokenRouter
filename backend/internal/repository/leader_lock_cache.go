package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/redis/go-redis/v9"
)

const leaderLockKeyPrefix = "leader:lock:"
const fencedLeaderLeaseKeyPrefix = "leader:fenced:"

// leaderLockReleaseScript 只释放仍由当前 owner 持有的锁。
// 这样可以避免旧持有者的延迟释放误删已经被其他实例重新获取的锁。
var leaderLockReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var fencedLeaderLeaseAcquireScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  local token = redis.call("INCR", KEYS[2])
  redis.call("HSET", KEYS[1], "owner", ARGV[1], "token", token)
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
  return {"1", tostring(token)}
end
local token = redis.call("HGET", KEYS[1], "token") or "0"
return {"0", tostring(token)}
`)

var fencedLeaderLeaseRenewScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "owner") == ARGV[1]
  and redis.call("HGET", KEYS[1], "token") == ARGV[2] then
  redis.call("PEXPIRE", KEYS[1], ARGV[3])
  return 1
end
return 0
`)

var fencedLeaderLeaseReleaseScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "owner") == ARGV[1]
  and redis.call("HGET", KEYS[1], "token") == ARGV[2] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var fencedLeaderLeaseInspectScript = redis.NewScript(`
local owner = redis.call("HGET", KEYS[1], "owner")
if not owner then
  return {"", "0", "-2"}
end
local token = redis.call("HGET", KEYS[1], "token") or "0"
local ttl = redis.call("PTTL", KEYS[1])
return {owner, tostring(token), tostring(ttl)}
`)

type leaderLockCache struct {
	rdb *redis.Client
}

// NewLeaderLockCache 创建缓存版周期任务主实例锁。
func NewLeaderLockCache(rdb *redis.Client) service.LeaderLockCache {
	return &leaderLockCache{rdb: rdb}
}

// NewFencedLeaderLeaseCache creates the renewable lease backend used by
// long-lived runners that require explicit handoff and fencing.
func NewFencedLeaderLeaseCache(rdb *redis.Client) service.FencedLeaderLeaseCache {
	return &leaderLockCache{rdb: rdb}
}

func (c *leaderLockCache) TryAcquireLeaderLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, leaderLockKeyPrefix+key, owner, ttl).Result()
}

func (c *leaderLockCache) ReleaseLeaderLock(ctx context.Context, key, owner string) error {
	return leaderLockReleaseScript.Run(ctx, c.rdb, []string{leaderLockKeyPrefix + key}, owner).Err()
}

func (c *leaderLockCache) TryAcquireFencedLeaderLease(ctx context.Context, key, owner string, ttl time.Duration) (int64, bool, error) {
	leaseKey, tokenKey, err := fencedLeaderLeaseRedisKeys(key)
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(owner) == "" || ttl.Milliseconds() <= 0 {
		return 0, false, fmt.Errorf("fenced leader lease owner and ttl must be set")
	}
	values, err := fencedLeaderLeaseAcquireScript.Run(ctx, c.rdb, []string{leaseKey, tokenKey}, owner, ttl.Milliseconds()).Slice()
	if err != nil {
		return 0, false, err
	}
	if len(values) != 2 {
		return 0, false, fmt.Errorf("unexpected fenced leader lease acquire result length %d", len(values))
	}
	acquiredValue, err := fencedLeaderLeaseInt64(values[0])
	if err != nil {
		return 0, false, err
	}
	token, err := fencedLeaderLeaseInt64(values[1])
	if err != nil {
		return 0, false, err
	}
	return token, acquiredValue == 1, nil
}

func (c *leaderLockCache) RenewFencedLeaderLease(ctx context.Context, key, owner string, token int64, ttl time.Duration) (bool, error) {
	leaseKey, _, err := fencedLeaderLeaseRedisKeys(key)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(owner) == "" || token <= 0 || ttl.Milliseconds() <= 0 {
		return false, fmt.Errorf("fenced leader lease owner, token, and ttl must be set")
	}
	result, err := fencedLeaderLeaseRenewScript.Run(
		ctx,
		c.rdb,
		[]string{leaseKey},
		owner,
		strconv.FormatInt(token, 10),
		ttl.Milliseconds(),
	).Int()
	return result == 1, err
}

func (c *leaderLockCache) ReleaseFencedLeaderLease(ctx context.Context, key, owner string, token int64) (bool, error) {
	leaseKey, _, err := fencedLeaderLeaseRedisKeys(key)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(owner) == "" || token <= 0 {
		return false, fmt.Errorf("fenced leader lease owner and token must be set")
	}
	result, err := fencedLeaderLeaseReleaseScript.Run(
		ctx,
		c.rdb,
		[]string{leaseKey},
		owner,
		strconv.FormatInt(token, 10),
	).Int()
	return result == 1, err
}

func (c *leaderLockCache) GetFencedLeaderLease(ctx context.Context, key string) (*service.FencedLeaderLease, error) {
	leaseKey, _, err := fencedLeaderLeaseRedisKeys(key)
	if err != nil {
		return nil, err
	}
	values, err := fencedLeaderLeaseInspectScript.Run(ctx, c.rdb, []string{leaseKey}).Slice()
	if err != nil {
		return nil, err
	}
	if len(values) != 3 {
		return nil, fmt.Errorf("unexpected fenced leader lease inspect result length %d", len(values))
	}
	owner := fmt.Sprint(values[0])
	if owner == "" {
		return nil, nil
	}
	token, err := fencedLeaderLeaseInt64(values[1])
	if err != nil {
		return nil, err
	}
	ttlMillis, err := fencedLeaderLeaseInt64(values[2])
	if err != nil {
		return nil, err
	}
	if ttlMillis <= 0 {
		return nil, nil
	}
	return &service.FencedLeaderLease{
		Owner:        owner,
		FencingToken: token,
		TTL:          time.Duration(ttlMillis) * time.Millisecond,
	}, nil
}

func fencedLeaderLeaseRedisKeys(key string) (string, string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "{}\r\n\t ") {
		return "", "", fmt.Errorf("invalid fenced leader lease key %q", key)
	}
	prefix := fencedLeaderLeaseKeyPrefix + "{" + key + "}:"
	return prefix + "lease", prefix + "token", nil
}

func fencedLeaderLeaseInt64(value any) (int64, error) {
	parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse fenced leader lease integer %q: %w", value, err)
	}
	return parsed, nil
}
