# Proof of Concept (POC) Tasks: Distributed Game of Life

## Phase 1: Core Simulation (The Engine)
- [x] **Task 1.1: Chunk Data Structure.** Define the `Chunk` struct. Support sparse coordinate representation for 64x64 chunks.
- [x] **Task 1.2: Simulation Actor Interface.** Scaffold the Actor model for chunk processing. Define local tick calculation and neighbor halo exchange.
- [x] **Task 1.3: Game of Life Rules Engine.** Implement the core algorithm optimized for sparse cell iteration.

## Phase 2: State Storage & Synchronization
- [x] **Task 2.1: Redis Storage Engine.** Implement save/load for chunk state using Redis (hot state).
- [x] **Task 2.2: Tick Manager.** Implement a deterministic lockstep clock to trigger chunk ticks and broadcast halo edge updates.
- [ ] **Task 2.3: Chunk Actor Registry.** Manage active chunk goroutines in memory and route ticks/events to them.

## Phase 3: Network & API (The Gateway)
- [ ] **Task 3.1: WebSocket Server.** Scaffold a generic WebSocket handler for client connections.
- [x] **Task 3.2: Command Router & Rate Limiting.** Integrate the existing `ratelimit` package. Route `SpawnCell` commands to the correct Chunk Actor.

## Phase 4: Minimal Client (The Viewport)
- [ ] **Task 4.1: Web Canvas.** Create a minimal HTML5/JS canvas to render a 3x3 chunk viewport.
- [ ] **Task 4.2: Client Sync.** Implement WebSocket client to receive state and send mutations.