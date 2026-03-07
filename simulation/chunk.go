package simulation

import (
	"encoding/json"
	"math/bits"
	"sync"
)

// ChunkSize defines both the edge length of a square chunk and the width of
// a uint64 row — 64 bits maps exactly one row of cells with no padding.
const ChunkSize = 64

// ChunkID is the chunk's position in the infinite chunk grid.
type ChunkID struct {
	X int64
	Y int64
}

// Chunk stores cell state as a compact bitset: rows[y] has bit x set iff
// cell (x, y) within this chunk is alive.
//
// Benefits over map[uint16]struct{}:
//   - 512 bytes total (64 rows × 8 bytes), fits in L1 cache
//   - Cell read/write is a single bit operation — no hashing
//   - Population count via hardware POPCNT (bits.OnesCount64)
//   - Edge extraction for halo exchange is a bitmask, not a cell scan
type Chunk struct {
	mu   sync.Mutex
	ID   ChunkID
	rows [ChunkSize]uint64 // rows[y] bit x set → cell (x,y) alive
}

// NewChunk initializes an empty chunk at chunk-grid position (x, y).
func NewChunk(x, y int64) *Chunk {
	return &Chunk{ID: ChunkID{X: x, Y: y}}
}

// AddCell sets cell (x, y) alive. Safe to call concurrently.
func (c *Chunk) AddCell(x, y uint8) {
	if int(x) >= ChunkSize || int(y) >= ChunkSize {
		return
	}
	c.mu.Lock()
	c.rows[y] |= 1 << x
	c.mu.Unlock()
}

// RemoveCell clears cell (x, y). Safe to call concurrently.
func (c *Chunk) RemoveCell(x, y uint8) {
	if int(x) >= ChunkSize || int(y) >= ChunkSize {
		return
	}
	c.mu.Lock()
	c.rows[y] &^= 1 << x
	c.mu.Unlock()
}

// Rows returns a copy of the bitset for tick computation.
// The copy is taken under the lock so the caller never races with AddCell.
func (c *Chunk) Rows() [ChunkSize]uint64 {
	c.mu.Lock()
	r := c.rows
	c.mu.Unlock()
	return r
}

// SetRows replaces the cell state after a tick completes.
func (c *Chunk) SetRows(rows [ChunkSize]uint64) {
	c.mu.Lock()
	c.rows = rows
	c.mu.Unlock()
}

// PopCount returns the number of live cells (used for hibernation checks).
func (c *Chunk) PopCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, row := range c.rows {
		n += bits.OnesCount64(row)
	}
	return n
}

// Snapshot returns a []uint16 sparse encoding of live cells.
// Format: offset = y*ChunkSize + x. Compatible with the wire protocol and
// Redis storage — no changes needed in the gateway or storage packages.
func (c *Chunk) Snapshot() []uint16 {
	c.mu.Lock()
	rows := c.rows
	c.mu.Unlock()
	return RowsToOffsets(rows)
}

// RowsToOffsets converts a [ChunkSize]uint64 bitset to a []uint16 offset slice.
// Exported so storage.LoadChunk can reconstruct a Chunk from persisted data.
func RowsToOffsets(rows [ChunkSize]uint64) []uint16 {
	total := 0
	for _, row := range rows {
		total += bits.OnesCount64(row)
	}
	out := make([]uint16, 0, total)
	for y, row := range rows {
		for row != 0 {
			x := bits.TrailingZeros64(row)
			row &^= 1 << x
			out = append(out, uint16(y)*ChunkSize+uint16(x))
		}
	}
	return out
}

// OffsetsToRows converts a []uint16 offset slice to a [ChunkSize]uint64 bitset.
// Used by storage.LoadChunk to reconstruct persisted state.
func OffsetsToRows(offsets []uint16) [ChunkSize]uint64 {
	var rows [ChunkSize]uint64
	for _, off := range offsets {
		x := off % ChunkSize
		y := off / ChunkSize
		if int(y) < ChunkSize {
			rows[y] |= 1 << x
		}
	}
	return rows
}

// MarshalJSON provides the sparse JSON representation for the network layer.
func (c *Chunk) MarshalJSON() ([]byte, error) {
	cells := c.Snapshot()
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
