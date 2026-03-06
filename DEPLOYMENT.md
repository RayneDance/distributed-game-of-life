# Deployment Guide

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Prerequisites](#prerequisites)
3. [Environment Variables](#environment-variables)
4. [Local Development](#local-development)
5. [Production: Bare Metal / VPS](#production-bare-metal--vps)
6. [Production: Docker](#production-docker)
7. [Reverse Proxy (nginx)](#reverse-proxy-nginx)
8. [Prometheus & Observability](#prometheus--observability)
9. [Health Checks](#health-checks)
10. [Hardening Checklist](#hardening-checklist)

---

## Architecture Overview

```
Internet
   │
   ▼
[ nginx / TLS termination ]
   │  /       → serves viewport static files (or nginx serves them directly)
   │  /ws     → proxied to Go server (WebSocket upgrade)
   │  /metrics → internal only, not exposed publicly
   ▼
[ Go server  :8080 ]
   │
   ├── gateway   (WebSocket, rate-limit enforcement, pub/sub routing)
   ├── simulation (chunk actors, GoL engine, hibernation)
   └── storage   (Redis hot-state persistence)
   │
   ▼
[ Redis  :6379 ]
```

The Go binary is a single statically-linked executable. The only runtime
dependency is a reachable Redis instance.

---

## Prerequisites

| Tool | Minimum version | Notes |
|------|----------------|-------|
| Go | 1.23 | Set in `go.mod`. `go install` handles the toolchain automatically. |
| Redis | 6.x | Standalone or managed (e.g. Redis Cloud, AWS ElastiCache). |
| nginx (optional) | 1.18 | TLS termination and WebSocket proxy. Any reverse proxy works. |

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost:6379` | Redis host:port. For Redis with auth, see note below. |

**Redis with a password** — the current `storage` and `ratelimit` packages
construct a plain `redis.NewClient`. To add auth, update `main.go`:

```go
redisClient := redis.NewClient(&redis.Options{
    Addr:     redisAddr,
    Password: os.Getenv("REDIS_PASSWORD"), // add this
})
```

---

## Local Development

```sh
# 1. Start Redis (Docker is the easiest path)
docker run -d --name redis -p 6379:6379 redis:7-alpine

# 2. Build and run the server
cd distributed-game-of-life
go run .

# 3. Open the viewport
#    Navigate to http://localhost:8080
```

The server serves the `./viewport` directory at `/`, so `index.html` and
`app.js` are available without a separate file server.

### Running tests (when added)

```sh
go test ./...
```

---

## Production: Bare Metal / VPS

### 1. Build a static binary

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o golive .
```

For ARM64 hosts (e.g. Raspberry Pi, AWS Graviton):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o golive .
```

### 2. Copy files to server

```sh
scp golive user@your-server:/opt/golive/
scp -r viewport/ user@your-server:/opt/golive/viewport/
```

### 3. Create a systemd service

Create `/etc/systemd/system/golive.service`:

```ini
[Unit]
Description=Distributed Game of Life Server
After=network.target redis.service
Requires=redis.service

[Service]
Type=simple
User=golive
WorkingDirectory=/opt/golive
ExecStart=/opt/golive/golive
Restart=on-failure
RestartSec=5s

# Environment
Environment=REDIS_ADDR=127.0.0.1:6379

# Resource limits
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/opt/golive

[Install]
WantedBy=multi-user.target
```

```sh
# Create the service user
sudo useradd -r -s /bin/false golive

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable --now golive

# Check status
sudo systemctl status golive
sudo journalctl -u golive -f
```

---

## Production: Docker

### Dockerfile

Create `Dockerfile` in the project root:

```dockerfile
# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /golive .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /golive /golive
COPY --from=builder /src/viewport /viewport

EXPOSE 8080
ENTRYPOINT ["/golive"]
```

### Build and run

```sh
docker build -t golive:latest .

docker run -d \
  --name golive \
  -p 8080:8080 \
  -e REDIS_ADDR=redis:6379 \
  --link redis:redis \
  golive:latest
```

### docker-compose (recommended for local production testing)

Create `docker-compose.yml`:

```yaml
version: "3.9"

services:
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    volumes:
      - redis-data:/data
    command: redis-server --save 60 1 --loglevel warning

  golive:
    build: .
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      - REDIS_ADDR=redis:6379
    depends_on:
      - redis

volumes:
  redis-data:
```

```sh
docker-compose up -d
docker-compose logs -f golive
```

---

## Reverse Proxy (nginx)

nginx handles TLS termination and forwards WebSocket traffic. WebSocket
connections **require** the `Upgrade` and `Connection` headers to be proxied.

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate     /etc/letsencrypt/live/your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/your-domain.com/privkey.pem;

    # WebSocket endpoint
    location /ws {
        proxy_pass         http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade    $http_upgrade;
        proxy_set_header   Connection "Upgrade";
        proxy_set_header   Host       $host;
        proxy_read_timeout 3600s;   # keep WS connections open for 1 hour
        proxy_send_timeout 3600s;
    }

    # Viewport static files and any other HTTP routes
    location / {
        proxy_pass       http://127.0.0.1:8080;
        proxy_set_header Host $host;
    }

    # /metrics must NOT be publicly accessible
    location /metrics {
        deny all;
    }
}

# Redirect HTTP → HTTPS
server {
    listen 80;
    server_name your-domain.com;
    return 301 https://$host$request_uri;
}
```

**TLS certificates** via Let's Encrypt:

```sh
sudo certbot --nginx -d your-domain.com
```

---

## Prometheus & Observability

The server exposes metrics at `GET /metrics` (Prometheus text format).

### Scrape config (`prometheus.yml`)

```yaml
scrape_configs:
  - job_name: golive
    static_configs:
      - targets: ["localhost:8080"]   # scrape from inside the network only
```

### Key metrics to alert on

| Metric | Alert condition | Likely cause |
|--------|----------------|--------------|
| `golive_active_chunk_actors` | Sustained growth > 1 000 | Clients exploring without hibernation |
| `golive_tick_processing_duration_milliseconds{quantile="0.99"}` | > 50 ms | Dense chunk, GC pressure |
| `golive_redis_save_latency_milliseconds{quantile="0.95"}` | > 25 ms | Redis I/O saturation |
| `golive_websocket_dropped_messages_total` (rate) | > 0/min | Client backpressure or network issue |

### Grafana dashboard (quick start)

1. Add Prometheus as a data source in Grafana.
2. Import the panel JSON or create panels using the metric names above.

---

## Health Checks

There is no dedicated `/healthz` endpoint yet. Until one is added, use the
Prometheus metrics endpoint as a liveness signal:

```sh
# Returns 200 if the server is alive
curl -sf http://localhost:8080/metrics > /dev/null && echo "OK"
```

For Docker / Kubernetes:

```yaml
# docker-compose service stanza
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:8080/metrics"]
  interval: 10s
  timeout: 3s
  retries: 3
```

---

## Hardening Checklist

Before exposing to the internet, work through this list:

- [ ] **CORS**: Replace `CheckOrigin: func() { return true }` in `gateway/websocket.go`
  with a strict allowlist of your frontend origin(s).
- [ ] **Redis auth**: Set a strong password and bind Redis to `127.0.0.1` only (never `0.0.0.0`).
- [ ] **`/metrics` gating**: Block `/metrics` at nginx (shown above) or add bearer-token
  middleware so Prometheus credentials are required.
- [ ] **Player ID**: Replace `r.RemoteAddr` in `websocket.go` with a real session/auth token
  to prevent rate-limit spoofing.
- [ ] **Rate limit config**: Move the hardcoded token-bucket values (`10000, 2000, 50, 5`)
  in `ratelimit/rate_limiter.go` into environment variables.
- [ ] **Chunk TTL**: Add a Redis `EXPIRE` to chunk keys in `storage/redis.go` to
  bound cold-storage growth (currently keys never expire).
- [ ] **TLS**: Always terminate TLS at nginx; never run Go on port 443 directly.
- [ ] **`systemd` sandboxing**: Enable `MemoryMax`, `CPUQuota`, and `RestrictAddressFamilies`
  in the service unit for additional OS-level isolation.
