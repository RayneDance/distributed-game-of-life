package gateway

// IncomingMessage is the envelope for all client-to-server messages.
type IncomingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// SpawnCommand spawns a single cell at absolute world coordinates.
type SpawnCommand struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// PlaceShapeCommand places a named pattern rooted at (X, Y).
// The server looks up the shape — clients cannot supply arbitrary offsets.
type PlaceShapeCommand struct {
	X     int64  `json:"x"`
	Y     int64  `json:"y"`
	Shape string `json:"shape"`
}

// CellOffset is a relative (dx, dy) offset used in PlaceCustomCommand.
type CellOffset struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// PlaceCustomCommand places an arbitrary client-defined pattern rooted at (X, Y).
// Cells are (dx, dy) offsets from that root. The server validates bounds and
// cell count before accepting the command.
type PlaceCustomCommand struct {
	X     int64        `json:"x"`
	Y     int64        `json:"y"`
	Cells []CellOffset `json:"cells"`
}

// SubscribeCommand lists chunks to add or remove from a client's viewport subscription.
type SubscribeCommand struct {
	Chunks []ChunkRef `json:"chunks"`
}

// ChunkRef is a coordinate pair used in subscribe/unsubscribe payloads.
type ChunkRef struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// OutgoingMessage is the envelope for all server-to-client messages.
type OutgoingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ErrorPayload standardizes error responses to the client.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
