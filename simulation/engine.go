package simulation

import "math/bits"

// TickScratch is a reusable scratch buffer for neighbor counting.
// Allocated once per worker goroutine and reused across ticks — zero heap
// allocations on the hot path.
//
// Layout: a 66×66 uint8 grid with a 1-cell padding border around the 64×64
// chunk. Cell (x, y) in chunk-local coordinates maps to index (y+1)*66+(x+1).
// The border slots accumulate neighbor counts from halo cells.
type TickScratch struct {
	counts [66 * 66]uint8
}

// TickBitset computes the next generation for one chunk.
//
//   - chunkID:   the chunk being computed (used to locate neighbours in halos)
//   - chunkRows: pre-snapshotted current cell state
//   - halos:     neighbour chunk rows, keyed by their ChunkID
//   - scratch:   a reusable buffer (must be zeroed on entry; zeroed on exit)
//
// Returns the new row state. No heap allocations.
func TickBitset(
	id ChunkID,
	chunkRows [ChunkSize]uint64,
	halos map[ChunkID][ChunkSize]uint64,
	scratch *TickScratch,
) [ChunkSize]uint64 {
	// 1. Accumulate neighbor counts.
	//    Own cells are at padded offset (1,1); each live cell increments the
	//    8 surrounding scratch entries.
	addToScratch(&scratch.counts, chunkRows, 1, 1)

	for nid, nrows := range halos {
		// Map the neighbor's local (0,0) to our padded space.
		offX := 1 + int(nid.X-id.X)*ChunkSize
		offY := 1 + int(nid.Y-id.Y)*ChunkSize
		addToScratch(&scratch.counts, nrows, offX, offY)
	}

	// 2. Apply Game of Life rules.
	var next [ChunkSize]uint64
	for y := 0; y < ChunkSize; y++ {
		srcRow := chunkRows[y]
		var newRow uint64
		base := (y + 1) * 66
		for x := 0; x < ChunkSize; x++ {
			count := scratch.counts[base+(x+1)]
			if count == 3 || (count == 2 && (srcRow>>x)&1 == 1) {
				newRow |= 1 << x
			}
		}
		next[y] = newRow
	}

	// 3. Zero the scratch buffer for next use.
	//    runtime.memclr on 4356 bytes is a single SIMD sweep — effectively free.
	scratch.counts = [66 * 66]uint8{}

	return next
}

// addToScratch adds neighbor-count contributions from a bitset placed at
// (offX, offY) in the 66×66 padded scratch space.
// Positions outside [0,65]×[0,65] are silently skipped, which naturally
// trims halo data to the 1-cell border this chunk actually needs.
func addToScratch(counts *[66 * 66]uint8, rows [ChunkSize]uint64, offX, offY int) {
	for localY := 0; localY < ChunkSize; localY++ {
		row := rows[localY]
		if row == 0 {
			continue
		}
		padY := offY + localY
		for row != 0 {
			localX := bits.TrailingZeros64(row)
			row &^= 1 << localX
			padX := offX + localX
			// Increment the 8 Moore-neighbourhood positions.
			for dy := -1; dy <= 1; dy++ {
				ny := padY + dy
				if ny < 0 || ny >= 66 {
					continue
				}
				base := ny * 66
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					nx := padX + dx
					if nx >= 0 && nx < 66 {
						counts[base+nx]++
					}
				}
			}
		}
	}
}

// hasCellsNearEdge reports whether rows has any live cells within ≤2 cells
// of the face that borders the neighbour at offset (dx, dy).
// Used to skip halo exchange with neighbours that cannot be affected.
func hasCellsNearEdge(rows [ChunkSize]uint64, dx, dy int64) bool {
	const near = 2

	// Build the column mask for the X dimension.
	var xMask uint64
	switch dx {
	case -1:
		xMask = (1 << (near + 1)) - 1 // bits 0..2
	case 1:
		shift := uint(ChunkSize - 1 - near) // evaluated at runtime, not constant-folded
		xMask = ^uint64(0) << shift         // bits (ChunkSize-1-near)..63
	default:
		xMask = ^uint64(0) // all columns
	}

	// Determine the row range for the Y dimension.
	yMin, yMax := 0, ChunkSize-1
	switch dy {
	case -1:
		yMax = near
	case 1:
		yMin = ChunkSize - 1 - near
	}

	for y := yMin; y <= yMax; y++ {
		if rows[y]&xMask != 0 {
			return true
		}
	}
	return false
}
