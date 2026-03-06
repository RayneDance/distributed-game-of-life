package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus instrumentation for the game server.
type Metrics struct {
	// ActiveChunkActors tracks the number of live chunk goroutines.
	ActiveChunkActors prometheus.Gauge

	// TickDuration measures how long each chunk tick computation takes (ms).
	TickDuration prometheus.Histogram

	// RedisSaveLatency measures persistence round-trip time (ms).
	RedisSaveLatency prometheus.Histogram

	// WebSocketDropped counts messages that could not be delivered.
	WebSocketDropped prometheus.Counter
}

// New registers and returns all game-server metrics.
func New() *Metrics {
	return &Metrics{
		ActiveChunkActors: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "golive_active_chunk_actors",
			Help: "Number of currently active chunk actor goroutines.",
		}),
		TickDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "golive_tick_processing_duration_milliseconds",
			Help:    "Duration of a single chunk tick computation in milliseconds.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 25, 50, 100},
		}),
		RedisSaveLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "golive_redis_save_latency_milliseconds",
			Help:    "Round-trip latency of Redis chunk save operations in milliseconds.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 25},
		}),
		WebSocketDropped: promauto.NewCounter(prometheus.CounterOpts{
			Name: "golive_websocket_dropped_messages_total",
			Help: "Total WebSocket messages dropped due to send errors.",
		}),
	}
}
