package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/RayneDance/distributed-game-of-life/gateway"
	"github.com/RayneDance/distributed-game-of-life/metrics"
	"github.com/RayneDance/distributed-game-of-life/ratelimit"
	"github.com/RayneDance/distributed-game-of-life/simulation"
	"github.com/RayneDance/distributed-game-of-life/storage"
)

// readSecret fetches the latest version of a named secret from Cloud Secret Manager.
func readSecret(ctx context.Context, client *secretmanager.Client, project, name string) (string, error) {
	result, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest", project, name),
	})
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", name, err)
	}
	return strings.TrimSpace(string(result.Payload.Data)), nil
}

func main() {
	ctx := context.Background()

	// Sensible defaults for local development.
	redisAddr := os.Getenv("REDIS_ADDR")
	redisPassword := os.Getenv("REDIS_PASSWORD")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	// metadata.OnGCE() contacts the GCP metadata server, which is available on
	// Cloud Run, GCE, GKE, etc. If true, we're on GCP and should read secrets
	// from Secret Manager directly instead of relying on environment variables.
	// This means rotated secrets are picked up on the next process restart
	// without requiring a redeploy.
	if metadata.OnGCE() {
		project, err := metadata.ProjectIDWithContext(ctx)
		if err != nil {
			log.Fatalf("Failed to determine GCP project from metadata server: %v", err)
		}
		log.Printf("Running on GCP (project: %s) — reading secrets from Secret Manager", project)

		smClient, err := secretmanager.NewClient(ctx)
		if err != nil {
			log.Fatalf("Failed to create Secret Manager client: %v", err)
		}
		defer smClient.Close()

		if addr, err := readSecret(ctx, smClient, project, "REDIS_ADDR"); err != nil {
			log.Fatalf("Failed to read REDIS_ADDR from Secret Manager: %v", err)
		} else {
			redisAddr = addr
		}

		if pw, err := readSecret(ctx, smClient, project, "REDIS_PASSWORD"); err != nil {
			log.Fatalf("Failed to read REDIS_PASSWORD from Secret Manager: %v", err)
		} else {
			redisPassword = pw
		}
	} else {
		log.Printf("Not on GCP — using REDIS_ADDR/REDIS_PASSWORD env vars (local dev mode)")
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
	})

	// Verify Redis is reachable before starting.
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis unreachable at %s: %v", redisAddr, err)
	}

	m := metrics.New()
	store := storage.NewRedisEngine(redisClient)
	pubsub := gateway.NewPubSub(m)

	registry := simulation.NewRegistry(simulation.RegistryConfig{
		OnTick: pubsub.Broadcast,
		Persist: func(chunk *simulation.Chunk) error {
			start := time.Now()
			err := store.SaveChunk(context.Background(), chunk)
			m.RedisSaveLatency.Observe(float64(time.Since(start).Nanoseconds()) / 1e6)
			return err
		},
		HasSubscribers: pubsub.HasSubscribers,
		Metrics:        m,
	})

	limiter := ratelimit.NewLimiter(redisClient)
	router := gateway.NewRouter(limiter, registry, pubsub)

	http.Handle("/ws", gateway.HandleWebSocket(router, pubsub))
	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/catalog", gateway.HandleCatalog())
	http.Handle("/", http.FileServer(http.Dir("./viewport")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("Server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
