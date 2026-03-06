package ratelimit

import (
	"context"
	"github.com/redis/go-redis/v9"
)

// Minimum Viable Comments:
// Evaluates Token Bucket for both Global and Player limits atomically.
// Returns 1 if allowed, 0 if rate limited.
const luaTokenBucket = `
local function check_and_decrement(key, capacity, refill_rate, now)
    local bucket = redis.call("HMGET", key, "tokens", "last_refill")
    local tokens = tonumber(bucket[1])
    local last_refill = tonumber(bucket[2])

    if not tokens then
        tokens = capacity
        last_refill = now
    else
        local elapsed = math.max(0, now - last_refill)
        local added = math.floor(elapsed * refill_rate)
        tokens = math.min(capacity, tokens + added)
        if added > 0 then
            last_refill = now
        end
    end

    if tokens >= 1 then
        redis.call("HMSET", key, "tokens", tokens - 1, "last_refill", last_refill)
        redis.call("EXPIRE", key, math.ceil(capacity / refill_rate) + 1)
        return true
    end
    return false
end

local global_key = KEYS[1]
local player_key = KEYS[2]
local now = tonumber(ARGV[1])

-- Global params
local g_cap = tonumber(ARGV[2])
local g_rate = tonumber(ARGV[3])

-- Player params
local p_cap = tonumber(ARGV[4])
local p_rate = tonumber(ARGV[5])

-- Check global first (Fail fast)
if not check_and_decrement(global_key, g_cap, g_rate, now) then
    return 0 -- Global limit hit
end

-- Check player
if not check_and_decrement(player_key, p_cap, p_rate, now) then
    -- Rollback global token since player failed
    redis.call("HINCRBY", global_key, "tokens", 1)
    return 0 -- Player limit hit
end

return 1 -- Allowed
`

type Limiter struct {
	client *redis.Client
}

// NewLimiter creates a new Limiter instance.
func NewLimiter(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

// AllowMutation verifies if a player can place a cell.
// Dependency Verification: Ensure redis client is initialized and reachable.
func (l *Limiter) AllowMutation(ctx context.Context, playerID string, nowUnix int64) (bool, error) {
	keys := []string{"rate_limit:global", "rate_limit:player:" + playerID}
	
	// Example configuration: 
	// Global: 10,000 max burst, 2,000 per second refill
	// Player: 50 max burst, 5 per second refill
	args := []interface{}{nowUnix, 10000, 2000, 50, 5}

	result, err := l.client.Eval(ctx, luaTokenBucket, keys, args...).Int()
	if err != nil {
		return false, err
	}

	return result == 1, nil
}
