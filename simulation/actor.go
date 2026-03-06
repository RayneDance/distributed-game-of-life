package simulation

import (
	"context"
	"sync"
)

// ChunkActor defines the interface for an Actor managing a specific Chunk.
// Pattern Adherence: Actor Model. Each chunk processes its state independently
// and communicates via message passing (channels or method calls).
type ChunkActor interface {
	// ProcessSpawn handles user-initiated cell creation.
	ProcessSpawn(ctx context.Context, x, y uint8) error

	// ReceiveHalo receives edge data from adjacent chunks for the upcoming tick.
	ReceiveHalo(ctx context.Context, neighborID ChunkID, haloData []uint16) error

	// Tick advances the chunk's local simulation by one generation.
	// wg must be signalled via wg.Done() when the generation computation is
	// complete, releasing the lockstep barrier in TickAll.
	Tick(ctx context.Context, wg *sync.WaitGroup) error
}
