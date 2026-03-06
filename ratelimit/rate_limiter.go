package ratelimit

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// luaTokenBucket evaluates the token bucket for both global and player limits
// atomically. Returns 1 if allowed, 0 if rate limited.
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

// luaDrainN drains up to n tokens from the player bucket, clamping at zero
// (the player can never go below zero tokens). It always returns the number of
// tokens actually consumed so the caller knows how much penalty was applied.
// The global bucket is charged 1 token and its hard cap is still enforced —
// this prevents server-wide flooding while letting a single player overdraft
// their personal budget.
//
// KEYS[1] = global key  KEYS[2] = player key
// ARGV[1] = now (unix)  ARGV[2] = g_cap  ARGV[3] = g_rate
// ARGV[4] = p_cap       ARGV[5] = p_rate  ARGV[6] = n (tokens requested)
// Returns the number of tokens consumed from the player bucket (0 if global
// bucket rejected the request).
const luaDrainN = `
local function refill(key, capacity, refill_rate, now)
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
        if added > 0 then last_refill = now end
    end
    return tokens, last_refill
end

local global_key = KEYS[1]
local player_key = KEYS[2]
local now        = tonumber(ARGV[1])
local g_cap      = tonumber(ARGV[2])
local g_rate     = tonumber(ARGV[3])
local p_cap      = tonumber(ARGV[4])
local p_rate     = tonumber(ARGV[5])
local n          = tonumber(ARGV[6])

-- Charge one token from the global bucket; still block if global is exhausted.
local g_tokens, g_refill = refill(global_key, g_cap, g_rate, now)
if g_tokens < 1 then
    return 0  -- global limit hit; reject entirely
end
redis.call("HMSET", global_key, "tokens", g_tokens - 1, "last_refill", g_refill)
redis.call("EXPIRE", global_key, math.ceil(g_cap / g_rate) + 1)

-- Drain up to n tokens from the player bucket, clamping at 0.
local p_tokens, p_refill = refill(player_key, p_cap, p_rate, now)
local consumed = math.min(n, p_tokens)   -- may be 0 if already empty
p_tokens = p_tokens - consumed
redis.call("HMSET", player_key, "tokens", p_tokens, "last_refill", p_refill)
redis.call("EXPIRE", player_key, math.ceil(p_cap / p_rate) + 1)

return consumed
`

type Limiter struct {
	client *redis.Client
}

// NewLimiter creates a new Limiter instance.
func NewLimiter(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

// AllowMutation verifies if a player can place a single cell.
// Returns false (and sends an error to the client) if either the global or
// the player bucket is exhausted.
func (l *Limiter) AllowMutation(ctx context.Context, playerID string, nowUnix int64) (bool, error) {
	keys := []string{"rate_limit:global", "rate_limit:player:" + playerID}

	// Global: 10,000 max burst, 2,000 per second refill
	// Player: 50 max burst, 5 per second refill
	args := []interface{}{nowUnix, 10000, 2000, 50, 5}

	result, err := l.client.Eval(ctx, luaTokenBucket, keys, args...).Int()
	if err != nil {
		return false, err
	}

	return result == 1, nil
}

// DrainN atomically consumes up to n tokens from the player's bucket, clamping
// at zero — the player never goes negative. The global bucket is still charged
// exactly 1 token and its hard cap is enforced (returns 0 consumed if the
// global bucket is exhausted, which the caller should treat as a hard reject).
//
// Use this for operations that must always succeed for the player but should
// still impose a meaningful cooldown (e.g. placing a large custom piece).
func (l *Limiter) DrainN(ctx context.Context, playerID string, nowUnix int64, n int) (int, error) {
	keys := []string{"rate_limit:global", "rate_limit:player:" + playerID}
	args := []interface{}{nowUnix, 10000, 2000, 50, 5, n}

	consumed, err := l.client.Eval(ctx, luaDrainN, keys, args...).Int()
	if err != nil {
		return 0, err
	}
	return consumed, nil
}
