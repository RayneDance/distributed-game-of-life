package main

import (
	"log"
	"net/http"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/RayneDance/distributed-game-of-life/gateway"
	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
)

func main() {
	// Redis address can be overridden via environment variable.
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Wire up the core components.
	limiter := ratelimit.NewLimiter(redisClient)
	registry := simulation.NewRegistry()
	router := gateway.NewRouter(limiter, registry)

	// Routes:
	//   /ws  — WebSocket endpoint for game clients
	//   /    — Serves the minimal HTML5 viewport client
	http.Handle("/ws", gateway.HandleWebSocket(router))
	http.Handle("/", http.FileServer(http.Dir("./viewport")))

	addr := ":8080"
	log.Printf("Server listening on %s (Redis: %s)", addr, redisAddr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
