package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
)

// Router handles incoming WebSocket messages and dispatches them to the
// simulation engine or subscription manager.
type Router struct {
	limiter  *ratelimit.Limiter
	registry *simulation.Registry
	pubsub   *PubSub
}

// NewRouter creates a new command router.
func NewRouter(limiter *ratelimit.Limiter, registry *simulation.Registry, pubsub *PubSub) *Router {
	return &Router{limiter: limiter, registry: registry, pubsub: pubsub}
}

// worldToChunkLocal converts an absolute world coordinate to a (chunk, local)
// pair using floor-division semantics. Go's % returns negative remainders for
// negative dividends (e.g. -1 % 64 == -1, not 63), so we adjust explicitly.
func worldToChunkLocal(abs int64) (chunk int64, local uint8) {
	chunk = abs / simulation.ChunkSize
	rem := abs % simulation.ChunkSize
	if abs < 0 && rem != 0 {
		chunk--
		rem += simulation.ChunkSize
	}
	local = uint8(rem)
	return
}

// Route processes a single incoming message from a connected client.
func (r *Router) Route(ctx context.Context, playerID string, msg IncomingMessage, client *Client) {
	switch msg.Type {

	case "SPAWN":
		allowed, err := r.limiter.AllowMutation(ctx, playerID, time.Now().Unix())
		if err != nil {
			sendError(client, "INTERNAL_ERROR", "Rate limiter unavailable")
			return
		}
		if !allowed {
			sendError(client, "RATE_LIMITED", "You are sending commands too quickly")
			return
		}

		payloadBytes, _ := json.Marshal(msg.Payload)
		var cmd SpawnCommand
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			sendError(client, "INVALID_PAYLOAD", "Invalid spawn command format")
			return
		}

		chunkX, localX := worldToChunkLocal(cmd.X)
		chunkY, localY := worldToChunkLocal(cmd.Y)

		actor := r.registry.GetOrCreate(simulation.ChunkID{X: chunkX, Y: chunkY})
		actor.ProcessSpawn(ctx, localX, localY)

		client.WriteJSON(OutgoingMessage{Type: "SPAWN_ACK", Payload: cmd})

	case "SUBSCRIBE":
		payloadBytes, _ := json.Marshal(msg.Payload)
		var cmd SubscribeCommand
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			sendError(client, "INVALID_PAYLOAD", "Invalid subscribe command format")
			return
		}
		for _, ref := range cmd.Chunks {
			r.pubsub.Subscribe(simulation.ChunkID{X: ref.X, Y: ref.Y}, client)
		}

	case "UNSUBSCRIBE":
		payloadBytes, _ := json.Marshal(msg.Payload)
		var cmd SubscribeCommand
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			sendError(client, "INVALID_PAYLOAD", "Invalid unsubscribe command format")
			return
		}
		for _, ref := range cmd.Chunks {
			r.pubsub.Unsubscribe(simulation.ChunkID{X: ref.X, Y: ref.Y}, client)
		}

	default:
		sendError(client, "UNKNOWN_COMMAND", "Command type not supported")
	}
}
