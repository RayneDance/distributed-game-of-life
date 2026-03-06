package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Client wraps a WebSocket connection with a write mutex.
// gorilla/websocket prohibits concurrent writes; all outgoing messages
// must be serialised through WriteJSON.
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
		return true // Security Consideration: enforce strict CORS in production.
	},
}

// HandleWebSocket upgrades the connection and runs the client read loop.
// On disconnect, the client is removed from all PubSub subscriptions.
func HandleWebSocket(router *Router, pubsub *PubSub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		client := &Client{conn: conn}
		// Guarantee cleanup of all subscriptions when the connection drops.
		defer pubsub.UnsubscribeAll(client)

		// Use remote address as player ID for the POC.
		playerID := r.RemoteAddr

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Client %s disconnected: %v", playerID, err)
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
		Type:    "ERROR",
		Payload: ErrorPayload{Code: code, Message: message},
	})
}
