'use strict';

// ─── Config ──────────────────────────────────────────────────────────────────
const CELL_SIZE      = 8;    // pixels per cell
const CHUNK_SIZE     = 64;   // cells per chunk edge
const LOCAL_TICK_MS  = 250;  // local prediction interval (approx server tick)
const VIEWPORT_PAD   = 1;    // extra chunks to subscribe beyond visible edge

// ─── Canvas ──────────────────────────────────────────────────────────────────
const canvas   = document.getElementById('grid');
const ctx      = canvas.getContext('2d');
const statusEl = document.getElementById('status');
const posEl    = document.getElementById('pos');

// ─── State ───────────────────────────────────────────────────────────────────
// localCells: predicted world state as a Set of "x,y" strings.
// On spawn: add immediately (optimistic). On CHUNK_STATE: reconcile.
let localCells = new Set();

// Camera: world-pixel offset for the top-left corner of the canvas.
// Initialised so that world-origin (0,0) appears at the canvas centre.
let camX = 0;
let camY = 0;

// Drag tracking
let dragAnchorX = 0;
let dragAnchorY = 0;
let isPanning   = false;

// Track which chunks we have told the server we are watching.
let subscribedChunks = new Set(); // "cx,cy" strings

// ─── WebSocket ───────────────────────────────────────────────────────────────
const wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
const ws      = new WebSocket(`${wsProto}//${location.host}/ws`);

ws.onopen = () => {
    statusEl.textContent = 'Connected';
    statusEl.style.color = '#00ff88';
    updateSubscriptions();
};
ws.onclose = () => {
    statusEl.textContent = 'Disconnected';
    statusEl.style.color = '#ff4455';
};
ws.onmessage = ({ data }) => {
    const msg = JSON.parse(data);
    switch (msg.type) {
        case 'CHUNK_STATE': reconcileChunk(msg.payload); break;
        case 'SPAWN_ACK':   break; // already optimistically added
        case 'ERROR':       console.warn('Server:', msg.payload.code, msg.payload.message); break;
    }
};

// ─── Subscription Management ─────────────────────────────────────────────────
// Converts a world coordinate to its chunk index (floor division).
function worldToChunk(n) { return Math.floor(n / CHUNK_SIZE); }

// Determines the set of chunk IDs that should be subscribed given camera pos.
function visibleChunks() {
    const minWX = Math.floor(camX / CELL_SIZE);
    const minWY = Math.floor(camY / CELL_SIZE);
    const maxWX = Math.floor((camX + canvas.width)  / CELL_SIZE);
    const maxWY = Math.floor((camY + canvas.height) / CELL_SIZE);

    const set = new Set();
    const x0 = worldToChunk(minWX) - VIEWPORT_PAD;
    const y0 = worldToChunk(minWY) - VIEWPORT_PAD;
    const x1 = worldToChunk(maxWX) + VIEWPORT_PAD;
    const y1 = worldToChunk(maxWY) + VIEWPORT_PAD;
    for (let cx = x0; cx <= x1; cx++)
        for (let cy = y0; cy <= y1; cy++)
            set.add(`${cx},${cy}`);
    return set;
}

// Diffs the needed set against current subscriptions and sends delta messages.
function updateSubscriptions() {
    if (ws.readyState !== WebSocket.OPEN) return;

    const needed  = visibleChunks();
    const toSub   = [];
    const toUnsub = [];

    for (const k of needed)             if (!subscribedChunks.has(k)) toSub.push(k);
    for (const k of subscribedChunks)  if (!needed.has(k))           toUnsub.push(k);

    const parse = k => { const [x, y] = k.split(',').map(Number); return { x, y }; };

    if (toSub.length)   ws.send(JSON.stringify({ type: 'SUBSCRIBE',   payload: { chunks: toSub.map(parse)   } }));
    if (toUnsub.length) ws.send(JSON.stringify({ type: 'UNSUBSCRIBE', payload: { chunks: toUnsub.map(parse) } }));

    toSub.forEach(k   => subscribedChunks.add(k));
    toUnsub.forEach(k => subscribedChunks.delete(k));

    updateHUD();
}

// ─── Server Reconciliation ───────────────────────────────────────────────────
// When a CHUNK_STATE arrives, replace all locally-predicted cells in that chunk
// with the server-authoritative set. This is the "snap-back" step.
function reconcileChunk({ x, y, cells }) {
    const baseX = x * CHUNK_SIZE;
    const baseY = y * CHUNK_SIZE;
    const maxX  = baseX + CHUNK_SIZE;
    const maxY  = baseY + CHUNK_SIZE;

    // Evict local cells that fall within this chunk's world-coordinate range.
    for (const key of localCells) {
        const i  = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        if (wx >= baseX && wx < maxX && wy >= baseY && wy < maxY)
            localCells.delete(key);
    }

    // Insert server-authoritative cells (offset → world coords).
    for (const offset of (cells || [])) {
        const lx = offset % CHUNK_SIZE;
        const ly = Math.floor(offset / CHUNK_SIZE);
        localCells.add(`${baseX + lx},${baseY + ly}`);
    }
}

// ─── Local Prediction (Client-Side) ──────────────────────────────────────────
// Runs a lightweight GoL step on localCells between server ticks so that
// patterns appear to evolve smoothly without waiting for RTT.
const DIRS = [[-1,-1],[0,-1],[1,-1],[-1,0],[1,0],[-1,1],[0,1],[1,1]];

function localTick() {
    const counts = new Map();
    for (const key of localCells) {
        const i = key.indexOf(',');
        const x = +key.slice(0, i), y = +key.slice(i + 1);
        for (const [dx, dy] of DIRS) {
            const nk = `${x + dx},${y + dy}`;
            counts.set(nk, (counts.get(nk) || 0) + 1);
        }
    }
    const next = new Set();
    for (const [key, count] of counts)
        if (count === 3 || (count === 2 && localCells.has(key)))
            next.add(key);
    localCells = next;
}

setInterval(localTick, LOCAL_TICK_MS);

// ─── Input ───────────────────────────────────────────────────────────────────
canvas.addEventListener('click', e => {
    if (isPanning) return;
    const worldX = Math.floor((e.clientX + camX) / CELL_SIZE);
    const worldY = Math.floor((e.clientY + camY) / CELL_SIZE);
    // Optimistic: add to local state immediately for zero perceived latency.
    localCells.add(`${worldX},${worldY}`);
    if (ws.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ type: 'SPAWN', payload: { x: worldX, y: worldY } }));
});

// Pan the camera by dragging.
canvas.addEventListener('mousedown', e => {
    isPanning    = false;
    dragAnchorX  = e.clientX + camX;
    dragAnchorY  = e.clientY + camY;
    canvas.style.cursor = 'grabbing';
});
canvas.addEventListener('mousemove', e => {
    if (e.buttons !== 1) return;
    const nx = dragAnchorX - e.clientX;
    const ny = dragAnchorY - e.clientY;
    if (!isPanning && (Math.abs(nx - camX) > 3 || Math.abs(ny - camY) > 3))
        isPanning = true;
    camX = nx;
    camY = ny;
    updateHUD();
});
canvas.addEventListener('mouseup', () => {
    if (isPanning) updateSubscriptions();
    canvas.style.cursor = 'crosshair';
    setTimeout(() => { isPanning = false; }, 0);
});

// ─── Resize ───────────────────────────────────────────────────────────────────
function resize() {
    const prevW = canvas.width, prevH = canvas.height;
    canvas.width  = window.innerWidth;
    canvas.height = window.innerHeight;
    // Keep the world origin centred after the first resize.
    if (prevW === 0) {
        camX = -Math.floor(canvas.width  / 2);
        camY = -Math.floor(canvas.height / 2);
    }
    updateSubscriptions();
}
window.addEventListener('resize', resize);
// Force canvas dimensions to zero so the first resize() centres the camera.
canvas.width  = 0;
canvas.height = 0;
resize();

// ─── HUD ─────────────────────────────────────────────────────────────────────
function updateHUD() {
    const wx = Math.floor((canvas.width  / 2 + camX) / CELL_SIZE);
    const wy = Math.floor((canvas.height / 2 + camY) / CELL_SIZE);
    if (posEl) posEl.textContent =
        `Center: (${wx}, ${wy})  |  Subscribed chunks: ${subscribedChunks.size}`;
}

// ─── Render Loop ─────────────────────────────────────────────────────────────
function draw() {
    ctx.fillStyle = '#080810';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Faint chunk boundary grid.
    const chunkPx = CHUNK_SIZE * CELL_SIZE;
    const gx0 = (((-camX % chunkPx) + chunkPx) % chunkPx);
    const gy0 = (((-camY % chunkPx) + chunkPx) % chunkPx);
    ctx.strokeStyle = 'rgba(255,255,255,0.05)';
    ctx.lineWidth = 1;
    for (let x = gx0; x < canvas.width;  x += chunkPx) {
        ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, canvas.height); ctx.stroke();
    }
    for (let y = gy0; y < canvas.height; y += chunkPx) {
        ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(canvas.width, y); ctx.stroke();
    }

    // Living cells.
    ctx.fillStyle = '#00ff88';
    for (const key of localCells) {
        const i  = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        const sx = wx * CELL_SIZE - camX;
        const sy = wy * CELL_SIZE - camY;
        // Cull cells outside the viewport.
        if (sx > -CELL_SIZE && sx < canvas.width + CELL_SIZE &&
            sy > -CELL_SIZE && sy < canvas.height + CELL_SIZE)
            ctx.fillRect(sx, sy, CELL_SIZE - 1, CELL_SIZE - 1);
    }

    requestAnimationFrame(draw);
}
draw();
