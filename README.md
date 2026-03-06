# Distributed Game of Life

An architecture-first, infinite Game of Life implementation. 

## Architecture

Despite the name, the system operates on a **Single-Server, Multi-Client** topology. The "distributed" aspect refers to the concurrent Actor Model running within the single Go server and the spatial chunking of the infinite grid, rather than a clustered backend swarm.

### Core Components
- **The Central Server**: A single authoritative Go backend. It manages the global state, enforces rate limits, and runs the simulation engine.
- **Actor Model Engine**: The infinite board is partitioned into 64x64 chunks. Each active chunk is an isolated Actor (a goroutine) within the server, computing its own state and exchanging "halo" (edge) data with neighboring chunk actors.
- **State Storage**: Chunks are stored sparsely in a Key-Value store (Redis) to persist hot state.
- **The Clients**: Multiple users connect via WebSockets. Clients request a specific viewport (a set of chunks) and render them. Clients may simulate their local viewports between server ticks to minimize perceived latency and network synchronization overhead (client-side prediction).
- __Robust Rate Limiting__: Token-bucket based global and per-player rate limits implemented atomically via Redis Lua scripting to prevent Denial of Wallet (DoW) and resource exhaustion attacks from malicious or spammy clients.