package gateway

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

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
				sendError(conn, "INVALID_FORMAT", "Message must be valid JSON")
				continue
			}

			router.Route(r.Context(), playerID, msg, conn)
		}
	}
}

func sendError(conn *websocket.Conn, code, message string) {
	conn.WriteJSON(OutgoingMessage{
		Type: "ERROR",
		Payload: ErrorPayload{
			Code:    code,
			Message: message,
		},
	})
}
