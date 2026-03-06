package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
)

// Router dispatches incoming WebSocket commands to the simulation or PubSub.
type Router struct {
	limiter  *ratelimit.Limiter
	registry *simulation.Registry
	pubsub   *PubSub
}

// NewRouter creates a new command router.
func NewRouter(limiter *ratelimit.Limiter, registry *simulation.Registry, pubsub *PubSub) *Router {
	return &Router{limiter: limiter, registry: registry, pubsub: pubsub}
}

// worldToChunkLocal converts an absolute world coordinate to (chunk, local)
// using floor-division semantics so negative coords work correctly.
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

// spawnWorldCell routes a single absolute world coordinate to the correct actor.
func (r *Router) spawnWorldCell(ctx context.Context, worldX, worldY int64) {
	cx, lx := worldToChunkLocal(worldX)
	cy, ly := worldToChunkLocal(worldY)
	r.registry.GetOrCreate(simulation.ChunkID{X: cx, Y: cy}).ProcessSpawn(ctx, lx, ly)
}

// Route processes one incoming message from a connected client.
func (r *Router) Route(ctx context.Context, playerID string, msg IncomingMessage, client *Client) {
	switch msg.Type {

	// SPAWN — single cell, rate-limited.
	case "SPAWN":
		if !r.checkRateLimit(ctx, playerID, client) {
			return
		}
		var cmd SpawnCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		r.spawnWorldCell(ctx, cmd.X, cmd.Y)
		client.WriteJSON(OutgoingMessage{Type: "SPAWN_ACK", Payload: cmd})

	// PLACE_SHAPE — named pattern, rate-limited once per placement regardless of size.
	// The server looks up the offsets; clients cannot supply arbitrary coordinates.
	case "PLACE_SHAPE":
		if !r.checkRateLimit(ctx, playerID, client) {
			return
		}
		var cmd PlaceShapeCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		cells, ok := GetShape(cmd.Shape)
		if !ok {
			sendError(client, "UNKNOWN_SHAPE", "Unknown shape: "+cmd.Shape)
			return
		}
		for _, c := range cells {
			r.spawnWorldCell(ctx, cmd.X+c.X, cmd.Y+c.Y)
		}
		client.WriteJSON(OutgoingMessage{Type: "PLACE_SHAPE_ACK", Payload: cmd})

	case "SUBSCRIBE":
		var cmd SubscribeCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		for _, ref := range cmd.Chunks {
			id := simulation.ChunkID{X: ref.X, Y: ref.Y}
			r.pubsub.Subscribe(id, client)
			// Immediately flush current state so the client doesn't have to wait
			// for the next tick to see already-live cells. GetOrCreate also
			// hydrates hibernated chunks from Redis at this point.
			r.registry.GetOrCreate(id) // ensure actor exists and is loaded
			if cells := r.registry.SnapshotChunk(id); len(cells) > 0 {
				client.WriteJSON(OutgoingMessage{
					Type:    "CHUNK_STATE",
					Payload: ChunkStatePayload{X: ref.X, Y: ref.Y, Cells: cells},
				})
			}
		}

	case "UNSUBSCRIBE":
		var cmd SubscribeCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		for _, ref := range cmd.Chunks {
			r.pubsub.Unsubscribe(simulation.ChunkID{X: ref.X, Y: ref.Y}, client)
		}

	case "PING":
		// Keep-alive — no response needed.

	default:
		sendError(client, "UNKNOWN_COMMAND", "Command type not supported: "+msg.Type)
	}
}

// checkRateLimit returns true if the request is allowed, false (+ sends error) if not.
func (r *Router) checkRateLimit(ctx context.Context, playerID string, client *Client) bool {
	allowed, err := r.limiter.AllowMutation(ctx, playerID, time.Now().Unix())
	if err != nil {
		sendError(client, "INTERNAL_ERROR", "Rate limiter unavailable")
		return false
	}
	if !allowed {
		sendError(client, "RATE_LIMITED", "Slow down!")
		return false
	}
	return true
}

// parsePayload marshals msg.Payload to JSON then unmarshals into dst.
func parsePayload(payload interface{}, dst interface{}, client *Client) bool {
	b, _ := json.Marshal(payload)
	if err := json.Unmarshal(b, dst); err != nil {
		sendError(client, "INVALID_PAYLOAD", "Malformed payload")
		return false
	}
	return true
}
