package simulation

// Point represents an absolute coordinate in the infinite grid.
type Point struct {
	X int64
	Y int64
}

// Tick computes the next generation for a given set of active cells.
// This implements the Active Neighbor Algorithm.
// 
// Dependency Verification: 
// The input `activeCells` map must contain ALL living cells required for the calculation,
// including the "Halo" cells from adjacent chunks, to ensure deterministic outcomes.
func Tick(activeCells map[Point]struct{}) map[Point]struct{} {
	// 1. Create a frequency map of all cells adjacent to a living cell.
	neighborCounts := make(map[Point]uint8)

	// Offsets for the 8 neighbors (Moore neighborhood).
	dirs := []Point{
		{-1, -1}, {0, -1}, {1, -1},
		{-1,  0},          {1,  0},
		{-1,  1}, {0,  1}, {1,  1},
	}

	for cell := range activeCells {
		for _, d := range dirs {
			neighbor := Point{X: cell.X + d.X, Y: cell.Y + d.Y}
			neighborCounts[neighbor]++
		}
	}

	// 2. Evaluate rules based on neighbor counts.
	nextGen := make(map[Point]struct{})

	for pt, count := range neighborCounts {
		if count == 3 {
			// Reproduction: Any dead cell with exactly three live neighbors becomes a live cell.
			// Or Survival: Any live cell with exactly three live neighbors lives on.
			nextGen[pt] = struct{}{}
		} else if count == 2 {
			// Survival: Any live cell with two live neighbors lives on.
			if _, alive := activeCells[pt]; alive {
				nextGen[pt] = struct{}{}
			}
		}
		// All other cells die (or remain dead) by not being added to nextGen.
	}

	return nextGen
}
