package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RayneDance/distributed-game-of-life/simulation"
	"github.com/redis/go-redis/v9"
)

// Engine defines the contract for chunk state persistence.
// Pattern: Dependency Inversion Principle (SOLID).
type Engine interface {
	SaveChunk(ctx context.Context, chunk *simulation.Chunk) error
	LoadChunk(ctx context.Context, id simulation.ChunkID) (*simulation.Chunk, error)
}

// RedisEngine implements Engine using Redis as a hot state store.
// Architecture Note: Redis is chosen for its low-latency GET/SET capabilities,
// which is critical for the deterministic lockstep engine's strict tick deadlines.
type RedisEngine struct {
	client *redis.Client
}

// NewRedisEngine initializes the Redis storage adapter.
func NewRedisEngine(client *redis.Client) *RedisEngine {
	return &RedisEngine{client: client}
}

// chunkKey generates a deterministic key for a chunk in Redis.
func chunkKey(id simulation.ChunkID) string {
	return fmt.Sprintf("chunk:%d:%d", id.X, id.Y)
}

// SaveChunk serializes and persists the chunk state to Redis.
func (r *RedisEngine) SaveChunk(ctx context.Context, chunk *simulation.Chunk) error {
	data, err := chunk.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal chunk: %w", err)
	}

	key := chunkKey(chunk.ID)
	// SET without expiry for hot state.
	// Future optimization: Add TTL based on chunk activity to evict idle chunks.
	if err := r.client.Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to save chunk to redis: %w", err)
	}
	return nil
}

// LoadChunk retrieves and reconstructs a chunk from Redis.
// Returns an empty chunk if no state exists for the given ID.
func (r *RedisEngine) LoadChunk(ctx context.Context, id simulation.ChunkID) (*simulation.Chunk, error) {
	key := chunkKey(id)

	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			// Sparse representation: no data means an empty chunk.
			return simulation.NewChunk(id.X, id.Y), nil
		}
		return nil, fmt.Errorf("failed to load chunk from redis: %w", err)
	}

	// Unmarshal using an alias that matches chunk.MarshalJSON
	type Alias struct {
		X     int64    `json:"x"`
		Y     int64    `json:"y"`
		Cells []uint16 `json:"cells"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return nil, fmt.Errorf("failed to unmarshal chunk data: %w", err)
	}

	// Reconstruct the chunk from the persisted offset list.
	chunk := simulation.NewChunk(alias.X, alias.Y)
	chunk.SetRows(simulation.OffsetsToRows(alias.Cells))
	return chunk, nil
}
