package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Client wraps a WebSocket connection with a write mutex.
// gorilla/websocket explicitly prohibits concurrent writes, so all outgoing
// messages must be serialised through WriteJSON.
type Client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// WriteJSON serialises v as JSON and writes it to the connection under the lock.
func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Security Consideration: In production, enforce strict CORS.
	},
}

// HandleWebSocket upgrades the HTTP connection to a WebSocket and starts the read loop.
func HandleWebSocket(router *Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		client := &Client{conn: conn}

		// Extract player ID from header or query param. Using remote addr for POC.
		playerID := r.RemoteAddr

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Client disconnected: %v", err)
				break
			}

			var msg IncomingMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				sendError(client, "INVALID_FORMAT", "Message must be valid JSON")
				continue
			}

			router.Route(r.Context(), playerID, msg, client)
		}
	}
}

func sendError(client *Client, code, message string) {
	client.WriteJSON(OutgoingMessage{
		Type: "ERROR",
		Payload: ErrorPayload{
			Code:    code,
			Message: message,
		},
	})
}
