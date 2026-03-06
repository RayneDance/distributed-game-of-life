# Distributed Game of Life

An architecture-first, distributed, infinite Game of Life implementation.

## Architecture

This system uses a distributed spatial chunking strategy with an Actor Model compute engine to simulate an infinite grid.

### Features
- **Spatial Chunking**: The board is partitioned into 64x64 chunks, stored sparsely in a Key-Value store (Redis/ScyllaDB).
- **Deterministic Lockstep & Halo Ghosting**: Clients simulate local viewports, minimizing network synchronization overhead.
- **Robust Rate Limiting**: Token-bucket based global and per-player rate limits implemented atomically via Redis Lua scripting to prevent Denial of Wallet (DoW) and resource exhaustion attacks.
