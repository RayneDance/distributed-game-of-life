package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
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

// Route processes an incoming message.
func (r *Router) Route(ctx context.Context, playerID string, msg IncomingMessage, conn *websocket.Conn) {
	switch msg.Type {
	case "SPAWN":
		// 1. Rate Limit Check
		allowed, err := r.limiter.AllowMutation(ctx, playerID, time.Now().Unix())
		if err != nil {
			sendError(conn, "INTERNAL_ERROR", "Rate limiter unavailable")
			return
		}
		if !allowed {
			sendError(conn, "RATE_LIMITED", "You are sending commands too quickly")
			return
		}

		// 2. Parse Payload
		payloadBytes, _ := json.Marshal(msg.Payload)
		var cmd SpawnCommand
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			sendError(conn, "INVALID_PAYLOAD", "Invalid spawn command format")
			return
		}

		// 3. Route to Chunk Actor
		chunkX := cmd.X / simulation.ChunkSize
		chunkY := cmd.Y / simulation.ChunkSize
		if cmd.X < 0 {
			chunkX--
		}
		if cmd.Y < 0 {
			chunkY--
		}

		localX := uint8(cmd.X % simulation.ChunkSize)
		localY := uint8(cmd.Y % simulation.ChunkSize)
		if cmd.X < 0 {
			localX = uint8(simulation.ChunkSize + (cmd.X % simulation.ChunkSize))
		}
		if cmd.Y < 0 {
			localY = uint8(simulation.ChunkSize + (cmd.Y % simulation.ChunkSize))
		}

		actor := r.registry.GetOrCreate(simulation.ChunkID{X: chunkX, Y: chunkY})
		actor.ProcessSpawn(ctx, localX, localY)

		// Acknowledge success
		conn.WriteJSON(OutgoingMessage{
			Type: "SPAWN_ACK",
			Payload: cmd,
		})

	default:
		sendError(conn, "UNKNOWN_COMMAND", "Command type not supported")
	}
}
