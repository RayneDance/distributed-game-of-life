package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
)

// Router handles incoming WebSocket messages and routes them to the simulation.
type Router struct {
	limiter  *ratelimit.Limiter
	registry *simulation.Registry
}

// NewRouter creates a new command router.
func NewRouter(limiter *ratelimit.Limiter, registry *simulation.Registry) *Router {
	return &Router{
		limiter:  limiter,
		registry: registry,
	}
}

// worldToChunkLocal converts an absolute world coordinate to a (chunk, local) pair
// using floor-division semantics. This correctly handles negative coordinates:
// Go's built-in % returns a negative remainder for negative dividends, so
// e.g. -1 % 64 == -1 (not 63). We detect that case and adjust.
func worldToChunkLocal(abs int64) (chunk int64, local uint8) {
	chunk = abs / simulation.ChunkSize
	rem := abs % simulation.ChunkSize
	if abs < 0 && rem != 0 {
		// Floor the chunk index and shift the remainder into [0, ChunkSize).
		chunk--
		rem += simulation.ChunkSize
	}
	local = uint8(rem)
	return
}

// Route processes an incoming message.
func (r *Router) Route(ctx context.Context, playerID string, msg IncomingMessage, client *Client) {
	switch msg.Type {
	case "SPAWN":
		// 1. Rate Limit Check
		allowed, err := r.limiter.AllowMutation(ctx, playerID, time.Now().Unix())
		if err != nil {
			sendError(client, "INTERNAL_ERROR", "Rate limiter unavailable")
			return
		}
		if !allowed {
			sendError(client, "RATE_LIMITED", "You are sending commands too quickly")
			return
		}

		// 2. Parse Payload
		payloadBytes, _ := json.Marshal(msg.Payload)
		var cmd SpawnCommand
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			sendError(client, "INVALID_PAYLOAD", "Invalid spawn command format")
			return
		}

		// 3. Route to Chunk Actor using correct floor-division coordinate mapping.
		chunkX, localX := worldToChunkLocal(cmd.X)
		chunkY, localY := worldToChunkLocal(cmd.Y)

		actor := r.registry.GetOrCreate(simulation.ChunkID{X: chunkX, Y: chunkY})
		actor.ProcessSpawn(ctx, localX, localY)

		// Acknowledge success
		client.WriteJSON(OutgoingMessage{
			Type:    "SPAWN_ACK",
			Payload: cmd,
		})

	default:
		sendError(client, "UNKNOWN_COMMAND", "Command type not supported")
	}
}
