package simulation

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/RayneDance/distributed-game-of-life/metrics"
)

// hibernationThreshold is the number of idle ticks before an empty, unsubscribed
// chunk actor serializes its state and shuts down its goroutine.
const hibernationThreshold = 100

// RegistryConfig holds dependency-injected callbacks to avoid circular imports
// between the simulation, storage, and gateway packages.
type RegistryConfig struct {
	// OnTick is called after every chunk tick with the new cell snapshot.
	// Implemented by gateway.PubSub.Broadcast.
	OnTick func(id ChunkID, cells []uint16)

	// Persist saves a chunk to durable storage before hibernation.
	// Implemented as a closure over storage.RedisEngine in main.go.
	Persist func(chunk *Chunk) error

	// Load hydrates a chunk from durable storage.
	// Called inside GetOrCreate before the actor is started, so a chunk
	// that was previously hibernated resumes from its persisted state.
	Load func(id ChunkID) (*Chunk, error)

	// HasSubscribers reports whether any client is viewing the chunk.
	// Used to block hibernation of visible-but-empty chunks.
	HasSubscribers func(id ChunkID) bool

	// Metrics provides Prometheus instrumentation. May be nil.
	Metrics *metrics.Metrics
}

// Registry manages active chunk actors.
type Registry struct {
	mu     sync.RWMutex
	cfg    RegistryConfig
	chunks map[ChunkID]*chunkActorImpl
}

// NewRegistry creates a new chunk actor registry with the given config.
func NewRegistry(cfg RegistryConfig) *Registry {
	return &Registry{
		cfg:    cfg,
		chunks: make(map[ChunkID]*chunkActorImpl),
	}
}

// chunkActorImpl wraps a Chunk as an independent actor goroutine.
type chunkActorImpl struct {
	chunk     *Chunk
	spawnChan chan spawnReq
	haloChan  chan haloReq
	tickChan  chan struct{}
	registry  *Registry
}

type spawnReq struct{ x, y uint8 }
type haloReq struct {
	neighborID ChunkID
	haloData   []uint16
}

// GetOrCreate returns an existing actor or lazily instantiates a new one.
// If a Load callback is configured and the chunk has persisted state, the actor
// is hydrated from storage before its goroutine starts.
//
// Load (Redis I/O) is called with NO locks held to avoid blocking the entire
// registry during a network round-trip.
func (r *Registry) GetOrCreate(id ChunkID) ChunkActor {
	r.mu.RLock()
	actor, exists := r.chunks[id]
	r.mu.RUnlock()
	if exists {
		return actor
	}

	// Load from storage without holding any lock.
	chunk := NewChunk(id.X, id.Y)
	if r.cfg.Load != nil {
		if loaded, err := r.cfg.Load(id); err != nil {
			log.Printf("chunk (%d,%d): load from storage failed, starting blank: %v", id.X, id.Y, err)
		} else if loaded != nil {
			chunk = loaded
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Another goroutine may have created the actor while we were loading.
	if actor, exists = r.chunks[id]; exists {
		return actor
	}

	actor = &chunkActorImpl{
		chunk:     chunk,
		spawnChan: make(chan spawnReq, 100),
		haloChan:  make(chan haloReq, 16), // 16: headroom for all 8 neighbours × 2 rounds
		tickChan:  make(chan struct{}, 1),
		registry:  r,
	}
	r.chunks[id] = actor

	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveChunkActors.Inc()
	}

	go actor.run()
	return actor
}

// PeekChunk returns the current cell snapshot for a chunk without side-effects.
//
// If an actor is already running its live state is returned. Otherwise the
// configured Load callback is called to read persisted state directly from
// storage — no actor goroutine is created. Returns nil for empty/unknown chunks.
//
// Use this from the subscribe handler so that viewing an empty chunk does not
// spin up a goroutine that will only idle and then log a spurious hibernation.
func (r *Registry) PeekChunk(id ChunkID) []uint16 {
	r.mu.RLock()
	actor, exists := r.chunks[id]
	r.mu.RUnlock()
	if exists {
		return actor.chunk.Snapshot()
	}
	if r.cfg.Load == nil {
		return nil
	}
	chunk, err := r.cfg.Load(id)
	if err != nil || chunk == nil {
		return nil
	}
	return chunk.Snapshot()
}

// hasCellsNearEdge reports whether any cell in cells is within reach (≤ 2 rows/cols)
// of the face of this chunk that borders the neighbour at offset (dx, dy).
// This avoids waking hibernated neighbours that could not possibly be affected.
func hasCellsNearEdge(cells []uint16, dx, dy int64) bool {
	const near = int64(2) // GoL birth range is 1; +1 for safety
	for _, offset := range cells {
		x := int64(offset % ChunkSize)
		y := int64(offset / ChunkSize)
		xOk := (dx == -1 && x <= near) || (dx == 1 && x >= ChunkSize-1-near) || dx == 0
		yOk := (dy == -1 && y <= near) || (dy == 1 && y >= ChunkSize-1-near) || dy == 0
		if xOk && yOk {
			return true
		}
	}
	return false
}

// TickAll advances every active chunk by one generation.
// It must be called by an external timer (e.g. time.Ticker in main.go).
//
// The sequence is:
//  1. Snapshot every active chunk's cells.
//  2. For each chunk that has cells near a boundary, wake (GetOrCreate) the
//     neighbouring chunk and send it halo data so cross-boundary births work.
//  3. Send a tick signal to every actor goroutine.
func (r *Registry) TickAll(ctx context.Context) {
	r.mu.RLock()
	type entry struct {
		id    ChunkID
		actor *chunkActorImpl
		cells []uint16
	}
	entries := make([]entry, 0, len(r.chunks))
	for id, a := range r.chunks {
		entries = append(entries, entry{id, a, a.chunk.Snapshot()})
	}
	r.mu.RUnlock()

	// Neighbour offsets for the 8 surrounding chunks.
	neighbourOffsets := [8][2]int64{
		{-1, -1}, {0, -1}, {1, -1},
		{-1, 0}, {1, 0},
		{-1, 1}, {0, 1}, {1, 1},
	}

	// Distribute halo data to neighbours.
	// GetOrCreate is called only when the sending chunk actually has cells
	// near the shared face — this prevents unconditionally waking all 8
	// neighbours of every active chunk and avoids the hibernation spam.
	for _, e := range entries {
		if len(e.cells) == 0 {
			continue
		}
		for _, off := range neighbourOffsets {
			if !hasCellsNearEdge(e.cells, off[0], off[1]) {
				continue
			}
			nid := ChunkID{X: e.id.X + off[0], Y: e.id.Y + off[1]}
			neighbour := r.GetOrCreate(nid)
			neighbour.ReceiveHalo(ctx, e.id, e.cells) //nolint:errcheck
		}
	}

	// Tick every actor (the set may have grown from GetOrCreate calls above).
	r.mu.RLock()
	allActors := make([]*chunkActorImpl, 0, len(r.chunks))
	for _, a := range r.chunks {
		allActors = append(allActors, a)
	}
	r.mu.RUnlock()
	for _, a := range allActors {
		a.Tick(ctx) //nolint:errcheck
	}
}

// evict removes the actor from the registry (called by the actor itself on hibernation).
func (r *Registry) evict(id ChunkID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chunks, id)
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveChunkActors.Dec()
	}
}

func (a *chunkActorImpl) ProcessSpawn(ctx context.Context, x, y uint8) error {
	select {
	case a.spawnChan <- spawnReq{x, y}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *chunkActorImpl) ReceiveHalo(ctx context.Context, neighborID ChunkID, haloData []uint16) error {
	select {
	case a.haloChan <- haloReq{neighborID, haloData}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *chunkActorImpl) Tick(ctx context.Context) error {
	select {
	case a.tickChan <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the actor's main event loop. It is the only goroutine that writes
// to a.chunk, so internal locking is only needed on read paths shared with
// the network layer (Snapshot / MarshalJSON).
func (a *chunkActorImpl) run() {
	// pendingHalos holds the latest edge data from each neighbouring chunk.
	// Overwritten on every receipt; flushed after each tick.
	pendingHalos := make(map[ChunkID][]uint16)

	chunkBaseX := a.chunk.ID.X * ChunkSize
	chunkBaseY := a.chunk.ID.Y * ChunkSize

	idleTicks := 0

	for {
		select {
		case req := <-a.spawnChan:
			a.chunk.AddCell(req.x, req.y)
			idleTicks = 0

		case req := <-a.haloChan:
			// Overwrite with the freshest data from this neighbour.
			pendingHalos[req.neighborID] = req.haloData
			idleTicks = 0

		case <-a.tickChan:
			start := time.Now()

			// Eagerly drain any halos that arrived concurrently with this tick
			// signal. TickAll sends all halos before sending any ticks, so every
			// halo for this round is already in the channel, but Go's select is
			// non-deterministic — the tick case can fire before haloChan cases
			// do. Draining here guarantees complete cross-chunk boundary data.
		drainHalos:
			for {
				select {
				case req := <-a.haloChan:
					pendingHalos[req.neighborID] = req.haloData
				default:
					break drainHalos
				}
			}

			// 1. Build the superset of all relevant cells in absolute coords.
			//    Halo cells must be included for correct boundary behaviour.
			allCells := make(map[Point]struct{})

			for _, offset := range a.chunk.Snapshot() {
				lx := int64(offset % ChunkSize)
				ly := int64(offset / ChunkSize)
				allCells[Point{X: chunkBaseX + lx, Y: chunkBaseY + ly}] = struct{}{}
			}
			for neighborID, haloOffsets := range pendingHalos {
				nbx := neighborID.X * ChunkSize
				nby := neighborID.Y * ChunkSize
				for _, offset := range haloOffsets {
					lx := int64(offset % ChunkSize)
					ly := int64(offset / ChunkSize)
					allCells[Point{X: nbx + lx, Y: nby + ly}] = struct{}{}
				}
			}

			// 2. Evaluate Game of Life rules.
			nextGen := Tick(allCells)

			// 3. Write back only cells belonging to this chunk.
			a.chunk.mu.Lock()
			a.chunk.ActiveCells = make(map[uint16]struct{})
			for pt := range nextGen {
				lx := pt.X - chunkBaseX
				ly := pt.Y - chunkBaseY
				if lx >= 0 && lx < ChunkSize && ly >= 0 && ly < ChunkSize {
					a.chunk.ActiveCells[uint16(ly)*ChunkSize+uint16(lx)] = struct{}{}
				}
			}
			a.chunk.mu.Unlock()

			// 4. Observe tick duration.
			if m := a.registry.cfg.Metrics; m != nil {
				m.TickDuration.Observe(float64(time.Since(start).Nanoseconds()) / 1e6)
			}

			// 5. Broadcast authoritative state to subscribed viewport clients.
			if fn := a.registry.cfg.OnTick; fn != nil {
				fn(a.chunk.ID, a.chunk.Snapshot())
			}

			// 6. Reset halo buffers for the next tick cycle.
			pendingHalos = make(map[ChunkID][]uint16)

			// 7. Hibernation: an empty chunk with no subscribers and no pending
			//    work is a candidate for graceful shutdown.
			a.chunk.mu.RLock()
			activeCount := len(a.chunk.ActiveCells)
			a.chunk.mu.RUnlock()

			hasSubs := a.registry.cfg.HasSubscribers != nil &&
				a.registry.cfg.HasSubscribers(a.chunk.ID)
			pendingWork := len(a.spawnChan) > 0 || len(a.haloChan) > 0

			if activeCount == 0 && !hasSubs && !pendingWork {
				idleTicks++
				if idleTicks >= hibernationThreshold {
					a.hibernate()
					return
				}
			} else {
				idleTicks = 0
			}
		}
	}
}

// hibernate persists the chunk to cold storage and removes the actor from the
// registry, freeing its goroutine stack and in-memory state.
func (a *chunkActorImpl) hibernate() {
	if fn := a.registry.cfg.Persist; fn != nil {
		if err := fn(a.chunk); err != nil {
			log.Printf("chunk (%d,%d): persist failed before hibernation: %v",
				a.chunk.ID.X, a.chunk.ID.Y, err)
		}
	}
	// Hibernation is normal lifecycle management — not logged at INFO level.
	a.registry.evict(a.chunk.ID)
}
