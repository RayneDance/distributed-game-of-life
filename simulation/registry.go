package simulation

import (
	"context"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/RayneDance/distributed-game-of-life/metrics"
)

// hibernationThreshold is the number of consecutive idle ticks before an
// empty, unsubscribed chunk is persisted and removed from memory.
const hibernationThreshold = 100

// RegistryConfig holds dependency-injected callbacks.
type RegistryConfig struct {
	// OnTick is called after every chunk tick with the new cell snapshot.
	OnTick func(id ChunkID, cells []uint16)

	// Persist saves a chunk to durable storage before hibernation.
	Persist func(chunk *Chunk) error

	// Load hydrates a chunk from durable storage.
	Load func(id ChunkID) (*Chunk, error)

	// HasSubscribers reports whether any client is viewing the chunk.
	HasSubscribers func(id ChunkID) bool

	// Metrics provides Prometheus instrumentation. May be nil.
	Metrics *metrics.Metrics
}

// chunkState is the registry's bookkeeping for one active chunk.
type chunkState struct {
	chunk     *Chunk
	idleTicks int
}

// chunkHandle is the thin public wrapper returned by GetOrCreate.
// It implements ChunkActor so the gateway can spawn cells without knowing
// anything about the registry's internal tick machinery.
type chunkHandle struct {
	chunk *Chunk
}

func (h *chunkHandle) ProcessSpawn(_ context.Context, x, y uint8) error {
	h.chunk.AddCell(x, y)
	return nil
}

// tickJob is the unit of work sent to a worker goroutine each tick.
type tickJob struct {
	state     *chunkState
	chunkRows [ChunkSize]uint64 // pre-snapshotted — safe to read without lock
	halos     map[ChunkID][ChunkSize]uint64
	wg        *sync.WaitGroup
}

// Registry manages active chunks and drives the simulation via a worker pool.
type Registry struct {
	mu     sync.RWMutex
	cfg    RegistryConfig
	chunks map[ChunkID]*chunkState

	jobCh chan tickJob // unbuffered — workers block until TickAll sends
}

// NewRegistry creates a Registry and starts its worker pool.
// Workers == runtime.NumCPU(), giving genuine parallelism without
// over-subscribing the scheduler on low-vCPU environments like Cloud Run.
func NewRegistry(cfg RegistryConfig) *Registry {
	r := &Registry{
		cfg:    cfg,
		chunks: make(map[ChunkID]*chunkState),
		jobCh:  make(chan tickJob, runtime.NumCPU()*2),
	}
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		go r.runWorker()
	}
	return r
}

// runWorker is the body of each pooled worker goroutine.
// Each worker owns its own TickScratch, eliminating per-tick allocations.
func (r *Registry) runWorker() {
	var scratch TickScratch
	for job := range r.jobCh {
		r.processJob(job, &scratch)
	}
}

// processJob computes one tick for one chunk and handles post-tick bookkeeping.
func (r *Registry) processJob(job tickJob, scratch *TickScratch) {
	defer job.wg.Done()

	chunk := job.state.chunk
	start := time.Now()

	// Compute next generation using the pre-snapshotted rows and halos.
	nextRows := TickBitset(chunk.ID, job.chunkRows, job.halos, scratch)
	chunk.SetRows(nextRows)

	// Metrics.
	if m := r.cfg.Metrics; m != nil {
		m.TickDuration.Observe(float64(time.Since(start).Nanoseconds()) / 1e6)
	}

	// Broadcast to subscribed viewport clients.
	if fn := r.cfg.OnTick; fn != nil {
		fn(chunk.ID, chunk.Snapshot())
	}

	// Hibernation: an empty, unwatched chunk with no pending work hibernates
	// after hibernationThreshold consecutive idle ticks.
	empty := chunk.PopCount() == 0
	hasSubs := r.cfg.HasSubscribers != nil && r.cfg.HasSubscribers(chunk.ID)

	if empty && !hasSubs {
		job.state.idleTicks++
		if job.state.idleTicks >= hibernationThreshold {
			r.hibernate(chunk)
		}
	} else {
		job.state.idleTicks = 0
	}
}

// GetOrCreate returns a ChunkActor for id, creating and optionally hydrating
// it from storage if it does not already exist.
func (r *Registry) GetOrCreate(id ChunkID) ChunkActor {
	r.mu.RLock()
	state, exists := r.chunks[id]
	r.mu.RUnlock()
	if exists {
		return &chunkHandle{chunk: state.chunk}
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
	// Re-check: another goroutine may have created it while we were loading.
	if state, exists = r.chunks[id]; exists {
		return &chunkHandle{chunk: state.chunk}
	}

	state = &chunkState{chunk: chunk}
	r.chunks[id] = state

	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveChunkActors.Inc()
	}

	return &chunkHandle{chunk: chunk}
}

// PeekChunk returns the current cell snapshot without creating an actor.
// Used by the subscribe handler so that viewing an empty chunk doesn't
// start a pointless entry in the registry.
func (r *Registry) PeekChunk(id ChunkID) []uint16 {
	r.mu.RLock()
	state, exists := r.chunks[id]
	r.mu.RUnlock()
	if exists {
		return state.chunk.Snapshot()
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

// TickAll advances every active chunk by one generation and blocks until all
// chunks have finished computing. This is the lockstep barrier: the external
// ticker cannot fire again until every worker calls wg.Done().
//
// Sequence:
//  1. Snapshot every active chunk's bitset rows.
//  2. Distribute halo rows to neighbours that need them.
//  3. Fan out one tickJob per chunk to the worker pool.
//  4. Block on WaitGroup until all workers complete.
func (r *Registry) TickAll(ctx context.Context) {
	r.mu.RLock()
	type entry struct {
		id    ChunkID
		state *chunkState
		rows  [ChunkSize]uint64
	}
	entries := make([]entry, 0, len(r.chunks))
	rowsByID := make(map[ChunkID][ChunkSize]uint64, len(r.chunks))
	for id, state := range r.chunks {
		rows := state.chunk.Rows()
		entries = append(entries, entry{id, state, rows})
		rowsByID[id] = rows
	}
	r.mu.RUnlock()

	// Neighbour offsets for the 8 surrounding chunks.
	neighbourOffsets := [8][2]int64{
		{-1, -1}, {0, -1}, {1, -1},
		{-1, 0}, {1, 0},
		{-1, 1}, {0, 1}, {1, 1},
	}

	// Build per-chunk halo maps from the pre-snapshotted rows.
	// Only neighbours with cells near the shared face are included.
	halosByChunk := make(map[ChunkID]map[ChunkID][ChunkSize]uint64, len(entries))
	for _, e := range entries {
		allZero := true
		for _, row := range e.rows {
			if row != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			continue
		}
		for _, off := range neighbourOffsets {
			if !hasCellsNearEdge(e.rows, off[0], off[1]) {
				continue
			}
			nid := ChunkID{X: e.id.X + off[0], Y: e.id.Y + off[1]}
			// Wake the neighbour if needed so it is ticked this round.
			r.GetOrCreate(nid) //nolint:errcheck
			// Provide this chunk's rows as halo data for the neighbour.
			if halosByChunk[nid] == nil {
				halosByChunk[nid] = make(map[ChunkID][ChunkSize]uint64)
			}
			halosByChunk[nid][e.id] = e.rows
		}
	}

	// Re-read the (possibly grown) chunk list after GetOrCreate calls.
	r.mu.RLock()
	allStates := make([]*struct {
		id    ChunkID
		state *chunkState
		rows  [ChunkSize]uint64
	}, 0, len(r.chunks))
	for id, state := range r.chunks {
		rows, ok := rowsByID[id]
		if !ok {
			// Newly created this round — snapshot now.
			rows = state.chunk.Rows()
		}
		allStates = append(allStates, &struct {
			id    ChunkID
			state *chunkState
			rows  [ChunkSize]uint64
		}{id, state, rows})
	}
	r.mu.RUnlock()

	// Fan out jobs to the worker pool and wait for all to complete.
	var wg sync.WaitGroup
	wg.Add(len(allStates))
	for _, s := range allStates {
		select {
		case <-ctx.Done():
			wg.Done()
			continue
		case r.jobCh <- tickJob{
			state:     s.state,
			chunkRows: s.rows,
			halos:     halosByChunk[s.id],
			wg:        &wg,
		}:
		}
	}
	wg.Wait()
}

// evict removes a chunk from the registry (called on hibernation).
func (r *Registry) evict(id ChunkID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chunks, id)
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveChunkActors.Dec()
	}
}

// hibernate persists the chunk to cold storage and evicts it from memory.
func (r *Registry) hibernate(chunk *Chunk) {
	if fn := r.cfg.Persist; fn != nil {
		if err := fn(chunk); err != nil {
			log.Printf("chunk (%d,%d): persist failed before hibernation: %v",
				chunk.ID.X, chunk.ID.Y, err)
		}
	}
	r.evict(chunk.ID)
}
