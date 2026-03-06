package gateway

// IncomingMessage represents a raw WebSocket message from a client.
type IncomingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// SpawnCommand requests that a cell be toggled at absolute world coordinates.
type SpawnCommand struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// SubscribeCommand requests a diff of viewport subscriptions.
// Chunks lists the chunk coordinates to add or remove.
type SubscribeCommand struct {
	Chunks []ChunkRef `json:"chunks"`
}

// ChunkRef is a lightweight coordinate pair used in subscribe/unsubscribe payloads.
type ChunkRef struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// OutgoingMessage is the envelope for all server-to-client messages.
type OutgoingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ErrorPayload standardizes error reporting to the client.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
