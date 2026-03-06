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
func (r *Registry) GetOrCreate(id ChunkID) ChunkActor {
	r.mu.RLock()
	actor, exists := r.chunks[id]
	r.mu.RUnlock()
	if exists {
		return actor
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after acquiring write lock.
	if actor, exists = r.chunks[id]; exists {
		return actor
	}

	actor = &chunkActorImpl{
		chunk:     NewChunk(id.X, id.Y),
		spawnChan: make(chan spawnReq, 100),
		haloChan:  make(chan haloReq, 8),
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
	log.Printf("chunk (%d,%d): hibernating (idle for %d ticks)",
		a.chunk.ID.X, a.chunk.ID.Y, hibernationThreshold)
	a.registry.evict(a.chunk.ID)
}
