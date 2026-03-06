package gateway

import (
	"context"
	"encoding/json"
	"fmt"
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

	// PLACE_SHAPE — named pattern.
	// Rate-limit cost = number of cells in the shape, so a 36-cell Gosper Gun
	// costs 36 tokens rather than 1. This prevents flooding the board with large
	// patterns as cheaply as spawning single cells.
	case "PLACE_SHAPE":
		var cmd PlaceShapeCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		cells, ok := GetShape(cmd.Shape)
		if !ok {
			sendError(client, "UNKNOWN_SHAPE", "Unknown shape: "+cmd.Shape)
			return
		}
		// Charge one token per cell; bail on first rate-limit refusal.
		if !r.chargeN(ctx, playerID, client, len(cells)) {
			return
		}
		for _, c := range cells {
			r.spawnWorldCell(ctx, cmd.X+c.X, cmd.Y+c.Y)
		}
		client.WriteJSON(OutgoingMessage{Type: "PLACE_SHAPE_ACK", Payload: cmd})

	// PLACE_CUSTOM — arbitrary client-defined pattern.
	// Cells are (dx, dy) offsets from the root (X, Y) — the same format used
	// by the piece editor on the client.  The server validates:
	//   • Every offset is within the 100×100 custom-piece grid (0–99 inclusive).
	//   • The de-duplicated cell count does not exceed 10 000.
	// Unlike PLACE_SHAPE, custom pieces are ALWAYS placed regardless of whether
	// the player's token bucket is full; the bucket is drained as much as
	// possible (clamped at 0) to impose a cooldown. Only the global server-wide
	// cap can hard-reject a request, protecting server load.
	// Pieces exceeding LargeCustomThreshold cells incur an additional penalty.
	case "PLACE_CUSTOM":
		const LargeCustomPenalty = 25 // extra tokens drained for pieces ≥ LargeCustomThreshold

		var cmd PlaceCustomCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}

		// Validate and de-duplicate cells.
		validCells, errCode := ValidateCustomCells(cmd.Cells)
		if errCode != "" {
			sendError(client, errCode, fmt.Sprintf(
				"Custom piece validation failed: %s (max %d×%d, max %d cells)",
				errCode, MaxCustomDim+1, MaxCustomDim+1, MaxCustomCells,
			))
			return
		}

		// Compute total desired penalty: 1 token per cell + flat penalty for
		// large pieces. DrainN will consume as many as are available (≥ 0),
		// always allowing placement. Returns 0 only if the global bucket is
		// exhausted, in which case we reject to protect server-wide load.
		tokensWanted := len(validCells)
		if len(validCells) > LargeCustomThreshold {
			tokensWanted += LargeCustomPenalty
		}
		consumed, err := r.limiter.DrainN(ctx, playerID, time.Now().Unix(), tokensWanted)
		if err != nil {
			sendError(client, "INTERNAL_ERROR", "Rate limiter unavailable")
			return
		}
		if consumed == 0 && tokensWanted > 0 {
			// Global bucket exhausted — hard reject to protect server load.
			sendError(client, "RATE_LIMITED", "Server is busy — try again shortly")
			return
		}

		// Spawn all validated cells.
		for _, c := range validCells {
			r.spawnWorldCell(ctx, cmd.X+c.X, cmd.Y+c.Y)
		}
		client.WriteJSON(OutgoingMessage{Type: "PLACE_CUSTOM_ACK", Payload: cmd})

	case "SUBSCRIBE":
		var cmd SubscribeCommand
		if !parsePayload(msg.Payload, &cmd, client) {
			return
		}
		for _, ref := range cmd.Chunks {
			id := simulation.ChunkID{X: ref.X, Y: ref.Y}
			r.pubsub.Subscribe(id, client)
			// PeekChunk reads state without creating an actor (no side effects for
			// empty chunks). If cells exist we must also call GetOrCreate so the
			// actor is registered with TickAll — otherwise the world is visible
			// but permanently frozen after a server restart.
			if cells := r.registry.PeekChunk(id); len(cells) > 0 {
				r.registry.GetOrCreate(id) // start actor so simulation runs
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

// chargeN consumes n tokens from the rate limiter for playerID, sending an
// error and returning false on the first refusal.  n == 0 is a no-op.
func (r *Router) chargeN(ctx context.Context, playerID string, client *Client, n int) bool {
	for i := 0; i < n; i++ {
		if !r.checkRateLimit(ctx, playerID, client) {
			return false
		}
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
