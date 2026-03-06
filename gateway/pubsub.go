package gateway

import (
	"log"
	"sync"

	"github.com/RayneDance/distributed-game-of-life/metrics"
	"github.com/RayneDance/distributed-game-of-life/simulation"
)

// ChunkStatePayload is sent to subscribers after each chunk tick.
type ChunkStatePayload struct {
	X     int64    `json:"x"`
	Y     int64    `json:"y"`
	Cells []uint16 `json:"cells"`
}

// PubSub implements the Observer pattern for spatial subscriptions.
// It maintains a thread-safe map of ChunkID → set of *Client, so that
// post-tick broadcasts only reach clients whose viewport includes that chunk.
type PubSub struct {
	mu          sync.RWMutex
	subscribers map[simulation.ChunkID]map[*Client]struct{}
	m           *metrics.Metrics
}

// NewPubSub creates a new subscription manager.
func NewPubSub(m *metrics.Metrics) *PubSub {
	return &PubSub{
		subscribers: make(map[simulation.ChunkID]map[*Client]struct{}),
		m:           m,
	}
}

// Subscribe registers client as a viewer of chunk id.
func (ps *PubSub) Subscribe(id simulation.ChunkID, client *Client) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.subscribers[id] == nil {
		ps.subscribers[id] = make(map[*Client]struct{})
	}
	ps.subscribers[id][client] = struct{}{}
}

// Unsubscribe removes client from a specific chunk's subscriber set.
func (ps *PubSub) Unsubscribe(id simulation.ChunkID, client *Client) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	subs := ps.subscribers[id]
	delete(subs, client)
	if len(subs) == 0 {
		delete(ps.subscribers, id)
	}
}

// UnsubscribeAll removes client from every chunk it was watching.
// Must be called when a client disconnects.
func (ps *PubSub) UnsubscribeAll(client *Client) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for id, subs := range ps.subscribers {
		delete(subs, client)
		if len(subs) == 0 {
			delete(ps.subscribers, id)
		}
	}
}

// HasSubscribers reports whether any client is currently watching chunk id.
// Used by the simulation to decide whether an idle chunk can hibernate.
func (ps *PubSub) HasSubscribers(id simulation.ChunkID) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.subscribers[id]) > 0
}

// Broadcast delivers the latest chunk state to all subscribers of chunk id.
// Called by the simulation registry after every tick.
func (ps *PubSub) Broadcast(id simulation.ChunkID, cells []uint16) {
	ps.mu.RLock()
	subs := ps.subscribers[id]
	// Snapshot subscriber set before releasing the lock to avoid holding it
	// during potentially-slow network writes.
	clients := make([]*Client, 0, len(subs))
	for c := range subs {
		clients = append(clients, c)
	}
	ps.mu.RUnlock()

	if len(clients) == 0 {
		return
	}

	msg := OutgoingMessage{
		Type:    "CHUNK_STATE",
		Payload: ChunkStatePayload{X: id.X, Y: id.Y, Cells: cells},
	}

	for _, c := range clients {
		if err := c.WriteJSON(msg); err != nil {
			log.Printf("pubsub: broadcast to client failed: %v", err)
			if ps.m != nil {
				ps.m.WebSocketDropped.Inc()
			}
		}
	}
}
