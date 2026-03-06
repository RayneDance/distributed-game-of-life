package simulation

import (
	"encoding/json"
	"sync"
)

// ChunkSize defines the dimensions of a square chunk.
// 64x64 aligns with cache lines and network MTU optimizations.
const ChunkSize = 64

// ChunkID represents the spatial coordinates of the chunk in the infinite grid.
type ChunkID struct {
	X int64
	Y int64
}

// Chunk manages the state of a 64x64 region of the Game of Life board.
type Chunk struct {
	mu sync.RWMutex

	ID ChunkID
	
	// ActiveCells is a sparse representation.
	// The key is the 1D offset (0 to 4095) for a 64x64 chunk.
	// Offset = (y * ChunkSize) + x
	ActiveCells map[uint16]struct{}
}

// NewChunk initializes an empty chunk.
func NewChunk(x, y int64) *Chunk {
	return &Chunk{
		ID:          ChunkID{X: x, Y: y},
		ActiveCells: make(map[uint16]struct{}),
	}
}

// AddCell adds a living cell at local coordinates (x, y).
func (c *Chunk) AddCell(x, y uint8) {
	if x >= ChunkSize || y >= ChunkSize {
		return // Out of bounds
	}
	offset := uint16(y)*ChunkSize + uint16(x)
	
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ActiveCells[offset] = struct{}{}
}

// RemoveCell removes a living cell at local coordinates (x, y).
func (c *Chunk) RemoveCell(x, y uint8) {
	if x >= ChunkSize || y >= ChunkSize {
		return
	}
	offset := uint16(y)*ChunkSize + uint16(x)
	
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ActiveCells, offset)
}

// Snapshot returns a thread-safe copy of the active cells for serialization or halo processing.
func (c *Chunk) Snapshot() []uint16 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	cells := make([]uint16, 0, len(c.ActiveCells))
	for offset := range c.ActiveCells {
		cells = append(cells, offset)
	}
	return cells
}

// MarshalJSON provides the sparse JSON representation for the network layer.
func (c *Chunk) MarshalJSON() ([]byte, error) {
	cells := c.Snapshot()
	
	// Data structure aligns with our architectural design for sparse transmission.
	type Alias struct {
		X           int64    `json:"x"`
		Y           int64    `json:"y"`
		ActiveCount int      `json:"active_count"`
		Cells       []uint16 `json:"cells"`
	}
	
	return json.Marshal(&Alias{
		X:           c.ID.X,
		Y:           c.ID.Y,
		ActiveCount: len(cells),
		Cells:       cells,
	})
}
