package simulation

import "context"

// ChunkActor is the external interface for a managed chunk.
// With the worker-pool model, halo exchange and tick dispatch are handled
// internally by the Registry — callers only need to spawn cells.
type ChunkActor interface {
	ProcessSpawn(ctx context.Context, x, y uint8) error
}
