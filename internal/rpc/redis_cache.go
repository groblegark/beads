package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// redisCachePrefix namespaces all cached query results in Redis.
	redisCachePrefix = "bd:qcache:"

	// redisCacheDefaultTTL is the default TTL for cached graph results.
	// Intentionally longer than the in-memory QueryCache (10s) because:
	// 1. Graph is a visualization endpoint — a few seconds of staleness is fine.
	// 2. Redis cache is NOT invalidated by writes, unlike the in-memory cache.
	// 3. The beads3d frontend polls every 10s, so 30s means ~2/3 of requests are cache hits.
	redisCacheDefaultTTL = 30 * time.Second
)

// RedisQueryCache provides a Redis-backed cache for expensive read queries.
// Unlike the in-memory QueryCache, this cache is NOT invalidated by writes
// and uses a longer TTL, making it suitable for visualization endpoints
// where eventual consistency is acceptable (bd-xlv1i).
type RedisQueryCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisQueryCache creates a Redis-backed query cache using an existing
// Redis client connection. Returns nil if client is nil.
func NewRedisQueryCache(client *redis.Client, ttl time.Duration) *RedisQueryCache {
	if client == nil {
		return nil
	}
	if ttl == 0 {
		ttl = redisCacheDefaultTTL
	}
	return &RedisQueryCache{
		client: client,
		ttl:    ttl,
	}
}

// redisCacheKey builds a cache key from the operation name and args.
func redisCacheKey(op string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(op))
	h.Write([]byte(":"))
	h.Write(args)
	return redisCachePrefix + hex.EncodeToString(h.Sum(nil))[:24]
}

// Get attempts to retrieve a cached Response from Redis.
// Returns the cached response and true on hit, or zero Response and false on miss.
func (c *RedisQueryCache) Get(ctx context.Context, op string, args json.RawMessage) (Response, bool) {
	key := redisCacheKey(op, args)
	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		return Response{}, false
	}

	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return Response{}, false
	}
	return resp, true
}

// Set stores a Response in Redis with the configured TTL.
func (c *RedisQueryCache) Set(ctx context.Context, op string, args json.RawMessage, resp Response) {
	if !resp.Success {
		return
	}
	key := redisCacheKey(op, args)
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	c.client.Set(ctx, key, data, c.ttl)
}
