# Patch Notes

---

## Unreleased — staging (targeting v1.1.0)

### ✨ New Features

#### Custom Piece Editor
You can now design and save your own pieces directly in the browser — no server reload required.

- Click **"Create New Piece"** in the shape panel to open the editor.
- Draw cells on a 100×100 grid using click/drag. Toggle between draw and erase mode.
- Name your piece and assign it a category, then click **Accept** to save it to your local catalog.
- Saved pieces appear in the shape panel alongside built-in shapes and can be placed like any other pattern.
- Click the **✏ edit** button next to any custom piece to re-open it in the editor and modify it.
- Custom pieces can be **deleted** with the ✕ button in the shape panel.
- Custom pieces are placed via the new `PLACE_CUSTOM` server command, which validates offsets and de-duplicates cells before spawning them.

---

### 🐛 Bug Fixes

#### Simulation no longer freezes after extended play
The server-side simulation was permanently stopping after the board grew large or ran for a long time. Under heavy load, computing a generation took longer than the 250 ms tick interval. The next tick would try to signal a still-busy actor — blocking forever and halting all future simulation steps. Fixed with a proper lockstep barrier: the tick loop now waits for every chunk actor to finish its current generation before advancing to the next one.

> **Notable side effect:** tick rate now adapts to load rather than deadlocking. On a very dense board the effective tick rate may drop below 4 Hz, but the simulation will always be correct and will never freeze.

#### Edit Piece button now opens correctly
Clicking **✏** on a custom piece in the shape panel was throwing a console error (`openEditor is not defined`) and doing nothing. Fixed.

#### Create New Piece no longer re-edits the last saved piece
Clicking **"Create New Piece"** immediately after saving a custom piece would re-open the editor pre-populated with that piece's cells instead of starting blank. Fixed.

---

### ⚖️ Balance Changes

#### Large custom pieces are now always placed
Previously, placing a custom piece with more cells than your remaining rate-limit budget (50 tokens) was silently rejected — even a piece with 51 cells would fail at full budget. This was unintentional.

**New behaviour:**
- Custom pieces are **always placed**, regardless of budget.
- Placement still *drains* as many tokens as possible (up to the piece's cell count, clamped at zero), imposing a proportional cooldown before your next placement.
- Pieces over **50 cells** incur an additional **25-token penalty** on top of the per-cell cost.
- The only hard rejection is if the **global server-wide** capacity is exhausted — this protects everyone, not just the individual player.
- Single-cell spawns (`SPAWN`) and named catalog shapes (`PLACE_SHAPE`) are **unchanged** — they still require available tokens.

