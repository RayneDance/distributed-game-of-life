package gateway

// IncomingMessage represents a raw WebSocket message from a client.
type IncomingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// SpawnCommand represents a user requesting to flip a cell state.
type SpawnCommand struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

// OutgoingMessage represents a message sent to the client.
type OutgoingMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// ErrorPayload standardizes error reporting to the client.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
