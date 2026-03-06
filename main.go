package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/RayneDance/distributed-game-of-life/gateway"
	"github.com/RayneDance/distributed-game-of-life/metrics"
	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
	"github.com/RayneDance/distributed-game-of-life/storage"
)

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})

	// Verify Redis is reachable before starting.
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Redis unreachable at %s: %v", redisAddr, err)
	}

	m := metrics.New()
	store := storage.NewRedisEngine(redisClient)
	pubsub := gateway.NewPubSub(m)

	registry := simulation.NewRegistry(simulation.RegistryConfig{
		// OnTick: broadcast authoritative chunk state to all viewport subscribers.
		OnTick: pubsub.Broadcast,

		// Persist: called by actors before they hibernate. Instrumented here so
		// the storage package stays free of Prometheus imports.
		Persist: func(chunk *simulation.Chunk) error {
			start := time.Now()
			err := store.SaveChunk(context.Background(), chunk)
			m.RedisSaveLatency.Observe(float64(time.Since(start).Nanoseconds()) / 1e6)
			return err
		},

		// HasSubscribers: prevents hibernation of chunks actively being viewed.
		HasSubscribers: pubsub.HasSubscribers,

		Metrics: m,
	})

	limiter := ratelimit.NewLimiter(redisClient)
	router := gateway.NewRouter(limiter, registry, pubsub)

	http.Handle("/ws", gateway.HandleWebSocket(router, pubsub))
	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/", http.FileServer(http.Dir("./viewport")))

	addr := ":8080"
	log.Printf("Server listening on %s  (Redis: %s)", addr, redisAddr)
	log.Printf("Prometheus metrics at http://localhost%s/metrics", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
