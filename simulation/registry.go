package simulation

import (
	"context"
	"sync"
)

// Registry manages active chunk actors.
// Pattern Adherence: Registry / Mediator pattern. Decouples the network layer
// from the individual goroutines processing chunk logic.
type Registry struct {
	mu     sync.RWMutex
	chunks map[ChunkID]*chunkActorImpl
}

// NewRegistry creates a new ChunkActorRegistry.
func NewRegistry() *Registry {
	return &Registry{
		chunks: make(map[ChunkID]*chunkActorImpl),
	}
}

// chunkActorImpl wraps a Chunk to run as an independent actor.
type chunkActorImpl struct {
	chunk *Chunk
	// Channels for message passing
	spawnChan chan spawnReq
	haloChan  chan haloReq
	tickChan  chan struct{}
}

type spawnReq struct {
	x, y uint8
}

type haloReq struct {
	neighborID ChunkID
	haloData   []uint16
}

// GetOrCreate returns an existing actor or spawns a new one.
func (r *Registry) GetOrCreate(id ChunkID) ChunkActor {
	r.mu.RLock()
	actor, exists := r.chunks[id]
	r.mu.RUnlock()

	if exists {
		return actor
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check locking
	if actor, exists = r.chunks[id]; exists {
		return actor
	}

	actor = &chunkActorImpl{
		chunk:     NewChunk(id.X, id.Y),
		spawnChan: make(chan spawnReq, 100),
		haloChan:  make(chan haloReq, 8), // 8 neighbors
		tickChan:  make(chan struct{}, 1),
	}
	r.chunks[id] = actor

	go actor.run()

	return actor
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

// run is the actor's main event loop. It processes spawns, halo exchanges, and
// tick events sequentially, which means no internal locking is needed for the
// chunk state that the actor owns exclusively.
func (a *chunkActorImpl) run() {
	// pendingHalos stores the latest halo data received from each neighbour,
	// keyed by the neighbour's ChunkID. Overwritten on each receipt; cleared
	// after each tick so stale data cannot pollute a future generation.
	pendingHalos := make(map[ChunkID][]uint16)

	chunkBaseX := a.chunk.ID.X * ChunkSize
	chunkBaseY := a.chunk.ID.Y * ChunkSize

	for {
		select {
		case req := <-a.spawnChan:
			a.chunk.AddCell(req.x, req.y)

		case req := <-a.haloChan:
			// Overwrite with the freshest data from this neighbour.
			pendingHalos[req.neighborID] = req.haloData

		case <-a.tickChan:
			// Build the superset of all relevant cells in absolute coordinates.
			// The engine.Tick() function requires halo cells to be included so
			// that boundary cells interact correctly across chunk borders.
			allCells := make(map[Point]struct{})

			// 1. Add this chunk's own living cells.
			for _, offset := range a.chunk.Snapshot() {
				lx := int64(offset % ChunkSize)
				ly := int64(offset / ChunkSize)
				allCells[Point{X: chunkBaseX + lx, Y: chunkBaseY + ly}] = struct{}{}
			}

			// 2. Add halo cells received from each neighbouring chunk.
			for neighborID, haloOffsets := range pendingHalos {
				neighborBaseX := neighborID.X * ChunkSize
				neighborBaseY := neighborID.Y * ChunkSize
				for _, offset := range haloOffsets {
					lx := int64(offset % ChunkSize)
					ly := int64(offset / ChunkSize)
					allCells[Point{X: neighborBaseX + lx, Y: neighborBaseY + ly}] = struct{}{}
				}
			}

			// 3. Run the Game of Life rules engine.
			nextGen := Tick(allCells)

			// 4. Filter results: keep only cells that belong to this chunk.
			a.chunk.mu.Lock()
			a.chunk.ActiveCells = make(map[uint16]struct{})
			for pt := range nextGen {
				lx := pt.X - chunkBaseX
				ly := pt.Y - chunkBaseY
				if lx >= 0 && lx < ChunkSize && ly >= 0 && ly < ChunkSize {
					offset := uint16(ly)*ChunkSize + uint16(lx)
					a.chunk.ActiveCells[offset] = struct{}{}
				}
			}
			a.chunk.mu.Unlock()

			// 5. Reset pending halos; the next tick will collect fresh data.
			pendingHalos = make(map[ChunkID][]uint16)

			// TODO: Broadcast updated chunk state to subscribed viewport clients.
		}
	}
}
