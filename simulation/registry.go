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

func (a *chunkActorImpl) run() {
	// Actor loop
	for {
		select {
		case req := <-a.spawnChan:
			a.chunk.AddCell(req.x, req.y)
		case <-a.haloChan:
			// Store halo data for the next tick
		case <-a.tickChan:
			// Run simulation.Tick()
			// Update local chunk state
			// Broadcast new halos to neighbors
		}
	}
}
