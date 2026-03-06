# Deployment Guide — Google Cloud

## Table of Contents

1. [Architecture](#architecture)
2. [Prerequisites](#prerequisites)
3. [First-Time Setup](#first-time-setup)
4. [Deploying](#deploying)
5. [Environment Variables](#environment-variables)
6. [Observability — Google Cloud Monitoring](#observability--google-cloud-monitoring)
7. [WebSocket Behaviour on Cloud Run](#websocket-behaviour-on-cloud-run)
8. [Scaling Constraints](#scaling-constraints)
9. [Hardening Checklist](#hardening-checklist)

---

## Architecture

```
Client Browser
      │  wss://   (TLS handled by Cloud Run's managed certificate)
      ▼
┌─────────────────────────────────────────────────┐
│  Cloud Run  (us-central1, min/max = 1 instance) │
│                                                 │
│  Go binary                                      │
│   ├─ /        → viewport static files           │
│   ├─ /ws      → WebSocket gateway               │
│   └─ /metrics → Prometheus (VPC-internal only)  │
└─────────────────┬───────────────────────────────┘
                  │  Private IP via Serverless VPC Connector
                  ▼
┌─────────────────────────────────────────────────┐
│  Memorystore for Redis 7  (STANDARD_HA)         │
│   • chunk hot-state (key: chunk:{x}:{y})        │
│   • token-bucket rate limiting (Lua script)     │
└─────────────────────────────────────────────────┘
```

| GCP Service | Purpose |
|---|---|
| **Cloud Run** | Hosts the Go server. Managed TLS, custom domain, auto-restart. |
| **Artifact Registry** | Stores Docker images (`golive-repo`). |
| **Memorystore for Redis** | Managed Redis 7 in the same region. |
| **Serverless VPC Connector** | Lets Cloud Run reach the Memorystore private IP without a public endpoint. |
| **Cloud Monitoring** | Ingests Prometheus metrics via Managed Service for Prometheus. |

---

## Prerequisites

| Tool | Install |
|---|---|
| `gcloud` CLI | [cloud.google.com/sdk](https://cloud.google.com/sdk/docs/install) |
| Docker Desktop | [docker.com](https://www.docker.com/products/docker-desktop/) |
| PowerShell 7+ | Already available on Windows; `winget install Microsoft.PowerShell` |
| Billing account | Linked to your GCP project |

Log in and set defaults:

```powershell
gcloud auth login
gcloud auth application-default login
gcloud config set project YOUR_PROJECT_ID
gcloud config set compute/region us-central1
```

---

## First-Time Setup

Run the deploy script with `-Init` **once** per project:

```powershell
./deploy.ps1 -ProjectId YOUR_PROJECT_ID -Init
```

This performs four steps in order:

1. **Enables APIs** — `artifactregistry`, `run`, `redis`, `vpcaccess`
2. **Creates Artifact Registry repo** — `golive-repo` in your chosen region
3. **Creates Memorystore instance** — `golive-redis`, Redis 7, STANDARD_HA tier, ~5 min
4. **Creates Serverless VPC Connector** — `golive-connector` on `10.8.0.0/28`

> **Memorystore is a private-IP-only service.** The VPC Connector is what lets
> Cloud Run (serverless) reach it without a public Redis endpoint.

---

## Deploying

Every subsequent code deploy is a single command:

```powershell
./deploy.ps1
```

Or with explicit inputs:

```powershell
./deploy.ps1 -ProjectId my-project -Region us-central1 -ServiceName golive
```

**What it does:**

1. Looks up the Memorystore private IP (`gcloud redis instances describe`)
2. Builds the Docker image tagged with the current git SHA (`linux/amd64`)
3. Pushes to Artifact Registry
4. Deploys (or re-deploys) the Cloud Run service

**Key Cloud Run flags set by the script:**

| Flag | Value | Reason |
|---|---|---|
| `--min-instances=1` | `1` | Prevents cold-starts that would wipe in-memory chunk actor state |
| `--max-instances=1` | `1` | The in-memory Registry cannot be shared across replicas — see [Scaling Constraints](#scaling-constraints) |
| `--timeout=3600` | 3600 s | Extends the max WebSocket session to 1 hour |
| `--concurrency=1000` | 1000 | Cloud Run Gen2 can handle this many simultaneous WebSocket connections per instance |
| `--session-affinity` | on | Ready for when multi-instance scaling is implemented |
| `--vpc-egress=private-ranges-only` | — | Only private traffic goes through the VPC connector; public traffic (CDN, etc.) uses the normal internet path |

After deploy the script prints the service URL:

```
✅ Deploy complete!
   URL      : https://golive-xxxxxxxx-uc.a.run.app
   WebSocket: wss://golive-xxxxxxxx-uc.a.run.app/ws
```

---

## Environment Variables

Set on the Cloud Run service via `--set-env-vars` in the deploy script.

| Variable | Set by | Description |
|---|---|---|
| `REDIS_ADDR` | deploy script (Memorystore IP) | `host:port` of the Redis instance |
| `PORT` | Cloud Run (automatic) | Port the server must listen on — already handled in `main.go` |

To update env vars without rebuilding the image:

```powershell
gcloud run services update golive `
  --region us-central1 `
  --set-env-vars="REDIS_ADDR=10.x.x.x:6379"
```

---

## Observability — Google Cloud Monitoring

### Option A: Managed Service for Prometheus (recommended)

This is the zero-ops path — no Prometheus server to run.

```powershell
# Enable the API
gcloud services enable monitoring.googleapis.com

# Install the Prometheus sidecar in your Cloud Run service
# Cloud Run currently requires a custom metric sidecar; follow:
# https://cloud.google.com/stackdriver/docs/managed-prometheus/setup-unmanaged
```

Until managed scraping is configured, metrics are reachable inside the VPC at
`http://SERVICE_IP:8080/metrics`. The endpoint is **not exposed publicly** —
Cloud Run's HTTPS load balancer does not forward `/metrics` unless you
explicitly allow it (don't).

### Option B: Quick manual check

```powershell
# Proxy a one-off metrics scrape through Cloud Run's internal network
gcloud run services proxy golive --region us-central1 --port 8080
# Then in another terminal:
curl http://localhost:8080/metrics
```

### Key metrics to watch

| Metric | Alert if… |
|---|---|
| `golive_active_chunk_actors` | Sustained growth → memory leak in actor lifecycle |
| `golive_tick_processing_duration_milliseconds{quantile="0.99"}` | > 50 ms → simulation load |
| `golive_redis_save_latency_milliseconds{quantile="0.95"}` | > 25 ms → Memorystore saturation |
| `golive_websocket_dropped_messages_total` (rate) | > 0/min → client backpressure |

---

## WebSocket Behaviour on Cloud Run

Cloud Run Gen2 supports WebSockets natively. Some limits to know:

| Limit | Value | Notes |
|---|---|---|
| Max session duration | **3600 s** | Set by `--timeout`. Clients must reconnect after 1 hour. |
| Idle connection timeout | **~800 s** | The load balancer drops silent connections. Implement a client-side ping every 5 minutes. |
| Max concurrent connections | **~1000 per instance** | Tunable via `--concurrency`. RAM is the practical ceiling. |
| WebSocket frame size | 32 MB | gorilla/websocket default — more than enough. |

**Recommended client-side keep-alive** (add to `viewport/app.js`):

```js
setInterval(() => {
    if (ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'PING' }));
}, 5 * 60 * 1000); // every 5 minutes
```

Add a `case "PING"` no-op handler in `gateway/router.go` to avoid an
`UNKNOWN_COMMAND` error response.

---

## Scaling Constraints

> **This is a single-server architecture by design.**

The in-memory `simulation.Registry` holds all live `chunkActorImpl` goroutines.
There is no shared-state layer between replicas — adding a second Cloud Run
instance would create two independent simulations that clients are split across,
not one shared world.

**`--max-instances=1` is therefore required** until one of these is implemented:

| Path | What it involves |
|---|---|
| **Vertical scale** | Upgrade Cloud Run CPU/RAM. A 4 vCPU / 4 GiB instance can hold 100k+ chunk goroutines. Easiest. |
| **Actor sharding** | Partition the infinite grid into zones, each served by a dedicated Cloud Run service. Clients connect to the service responsible for their viewport. |
| **External actor framework** | Migrate chunk actors to a distributed system (e.g. Akka, Orleans, or a custom Redis-streams-based approach) so multiple Go instances share one actor graph. |

For the current POC, vertical scaling is the right answer. The entire simulation
state can be reconstructed from Redis on a restart, so there is no data loss on
redeploy.

---

## Hardening Checklist

Before sharing the URL publicly:

- [ ] **WebSocket CORS** — replace `CheckOrigin: func() { return true }` in
  `gateway/websocket.go` with the specific Cloud Run URL (or your custom domain).
- [ ] **`/metrics` access** — confirm it is not publicly reachable by running
  `curl https://your-service.run.app/metrics` from outside GCP. It should return 404
  or connection refused. If not, add a `http.HandleFunc("/metrics", ...)` guard.
- [ ] **Redis auth** — Memorystore supports AUTH in Redis 6+. Add a password via
  `--auth-enabled` on the instance and set `REDIS_PASSWORD` in Cloud Run env vars.
- [ ] **Player ID** — `r.RemoteAddr` is spoofable at the load balancer layer.
  Read the `X-Forwarded-For` header (Cloud Run sets it) or issue session tokens.
- [ ] **Rate limit config** — move the hardcoded bucket values in
  `ratelimit/rate_limiter.go` to env vars so they can be tuned without a rebuild.
- [ ] **Custom domain** — map a domain via Cloud Run Domain Mappings; GCP
  provisions and auto-renews the TLS cert.
- [ ] **Cloud Armor** — attach a WAF policy to the Cloud Run backend to block
  known bad IPs and limit request rates at the CDN layer before they hit Go.
