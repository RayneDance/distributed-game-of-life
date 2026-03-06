package gateway

import (
	"encoding/json"
	"net/http"
)

// off is a shorthand for building ShapeOffset literals.
type off struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// ShapeDef is a named Game of Life pattern stored authoritatively on the server.
// Clients send only the key + root coordinate — the server expands the cells.
type ShapeDef struct {
	Label    string `json:"label"`
	Category string `json:"category"`
	Cells    []off  `json:"cells"`
}

// catalog is the single source of truth for all placeable patterns.
// Coordinates are (dx, dy) offsets from the player-supplied root cell.
var catalog = map[string]ShapeDef{
	// ----- Still lifes -----
	"cell": {Label: "Cell", Category: "Still Life", Cells: []off{
		{0, 0},
	}},
	"block": {Label: "Block", Category: "Still Life", Cells: []off{
		{0, 0}, {1, 0},
		{0, 1}, {1, 1},
	}},
	"beehive": {Label: "Beehive", Category: "Still Life", Cells: []off{
		{1, 0}, {2, 0},
		{0, 1}, {3, 1},
		{1, 2}, {2, 2},
	}},

	// ----- Oscillators -----
	"blinker": {Label: "Blinker", Category: "Oscillator", Cells: []off{
		{0, 0}, {1, 0}, {2, 0},
	}},
	"toad": {Label: "Toad (p2)", Category: "Oscillator", Cells: []off{
		{1, 0}, {2, 0}, {3, 0},
		{0, 1}, {1, 1}, {2, 1},
	}},
	"beacon": {Label: "Beacon (p2)", Category: "Oscillator", Cells: []off{
		{0, 0}, {1, 0},
		{0, 1},
		{3, 2},
		{2, 3}, {3, 3},
	}},
	"pulsar": {Label: "Pulsar (p3)", Category: "Oscillator", Cells: []off{
		{2, 0}, {3, 0}, {4, 0}, {8, 0}, {9, 0}, {10, 0},
		{0, 2}, {5, 2}, {7, 2}, {12, 2},
		{0, 3}, {5, 3}, {7, 3}, {12, 3},
		{0, 4}, {5, 4}, {7, 4}, {12, 4},
		{2, 5}, {3, 5}, {4, 5}, {8, 5}, {9, 5}, {10, 5},
		{2, 7}, {3, 7}, {4, 7}, {8, 7}, {9, 7}, {10, 7},
		{0, 8}, {5, 8}, {7, 8}, {12, 8},
		{0, 9}, {5, 9}, {7, 9}, {12, 9},
		{0, 10}, {5, 10}, {7, 10}, {12, 10},
		{2, 12}, {3, 12}, {4, 12}, {8, 12}, {9, 12}, {10, 12},
	}},

	// ----- Spaceships -----
	"glider": {Label: "Glider", Category: "Spaceship", Cells: []off{
		{1, 0},
		{2, 1},
		{0, 2}, {1, 2}, {2, 2},
	}},
	"lwss": {Label: "LWSS", Category: "Spaceship", Cells: []off{
		{1, 0}, {4, 0},
		{0, 1},
		{0, 2}, {4, 2},
		{0, 3}, {1, 3}, {2, 3}, {3, 3},
	}},

	// ----- Methuselahs -----
	"r-pentomino": {Label: "R-Pentomino", Category: "Methuselah", Cells: []off{
		{1, 0}, {2, 0},
		{0, 1}, {1, 1},
		{1, 2},
	}},
	"acorn": {Label: "Acorn", Category: "Methuselah", Cells: []off{
		{1, 0},
		{3, 1},
		{0, 2}, {1, 2}, {4, 2}, {5, 2}, {6, 2},
	}},
	"diehard": {Label: "Diehard", Category: "Methuselah", Cells: []off{
		{6, 0},
		{0, 1}, {1, 1},
		{1, 2}, {5, 2}, {6, 2}, {7, 2},
	}},

	// ----- Guns -----
	"gosper-gun": {Label: "Gosper Gun", Category: "Gun", Cells: []off{
		// Left block
		{0, 4}, {1, 4}, {0, 5}, {1, 5},
		// Left shuttle
		{10, 4}, {10, 5}, {10, 6},
		{11, 3}, {11, 7},
		{12, 2}, {12, 8},
		{13, 2}, {13, 8},
		{14, 5},
		{15, 3}, {15, 7},
		{16, 4}, {16, 5}, {16, 6},
		{17, 5},
		// Right shuttle
		// Starts at 20
		// Front block
		{20, 2}, {21, 2},
		{20, 3}, {21, 3},
		{20, 4}, {21, 4},
		// Hips
		{22, 1}, {22, 5},
		{24, 0}, {24, 1},
		{24, 5}, {24, 6},
		// Right block
		{34, 2}, {34, 3},
		{35, 2}, {35, 3},
	}},
}

// GetShape returns the cell offsets for a named shape, or false if unknown.
func GetShape(name string) ([]off, bool) {
	def, ok := catalog[name]
	if !ok {
		return nil, false
	}
	return def.Cells, true
}

// CatalogForClient returns the catalog in a JSON-serialisable form
// so clients can render previews without duplicating definitions.
func CatalogForClient() map[string]ShapeDef {
	return catalog
}

// HandleCatalog serves the full shape catalog as JSON over HTTP GET.
// The client fetches this once on load to populate the shape picker.
func HandleCatalog() http.HandlerFunc {
	// Serialise once at startup; the catalog is static.
	data, err := json.Marshal(CatalogForClient())
	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "catalog unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		w.Write(data) //nolint:errcheck
	}
}
