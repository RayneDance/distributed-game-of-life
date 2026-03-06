'use strict';

// ─── Config ──────────────────────────────────────────────────────────────────
const CELL_SIZE = 8;    // pixels per cell
const CHUNK_SIZE = 64;   // cells per chunk edge
const LOCAL_TICK_MS = 250;  // local prediction interval (approx server tick)
const VIEWPORT_PAD = 1;    // extra chunks to subscribe beyond visible edge

// ─── Canvas / DOM ────────────────────────────────────────────────────────────
const canvas = document.getElementById('grid');
const ctx = canvas.getContext('2d');
const statusEl = document.getElementById('status');
const posEl = document.getElementById('pos');
const modeBadge = document.getElementById('mode-badge');
const catTabsEl = document.getElementById('category-tabs');
const shapeList = document.getElementById('shape-list');
const clearBtn = document.getElementById('clear-btn');

// ─── State ───────────────────────────────────────────────────────────────────
let localCells = new Set();   // "x,y" strings — authoritative local world

// Camera: world-pixel offset of the canvas top-left corner.
// Initialised so world-origin (0,0) is at the canvas centre.
let camX = 0;
let camY = 0;

// Drag tracking
let dragAnchorX = 0;
let dragAnchorY = 0;
let isPanning = false;

// Shape-placement state
let selectedShape = null;   // key string, e.g. "glider"
let catalog = {};     // filled by fetchCatalog()
let ghostCells = [];     // [{wx, wy}] for the hover ghost, in world coords
let mouseWorldX = 0;
let mouseWorldY = 0;
let activeCategory = 'All';

// Track which chunks we have told the server we are watching.
let subscribedChunks = new Set(); // "cx,cy" strings

// ─── WebSocket ───────────────────────────────────────────────────────────────
const wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
const ws = new WebSocket(`${wsProto}//${location.host}/ws`);

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
        case 'SPAWN_ACK': break; // already optimistically added
        case 'PLACE_SHAPE_ACK': optimisticShape(msg.payload); break;
        case 'ERROR': console.warn('Server:', msg.payload.code, msg.payload.message); break;
    }
};

// ─── Catalog / Shape Panel ───────────────────────────────────────────────────
async function fetchCatalog() {
    try {
        const res = await fetch('/catalog');
        catalog = await res.json();
        buildPanel();
    } catch (e) {
        console.error('Failed to fetch shape catalog:', e);
    }
}

// Derive ordered list of categories (with "All" first).
function categories() {
    const cats = new Set(['All']);
    for (const def of Object.values(catalog)) cats.add(def.category);
    return [...cats];
}

// Build the category tabs and shape buttons.
function buildPanel() {
    // --- Category tabs ---
    catTabsEl.innerHTML = '';
    for (const cat of categories()) {
        const btn = document.createElement('button');
        btn.className = 'cat-tab' + (cat === activeCategory ? ' active' : '');
        btn.textContent = cat;
        btn.dataset.cat = cat;
        btn.addEventListener('click', () => {
            activeCategory = cat;
            document.querySelectorAll('.cat-tab').forEach(el =>
                el.classList.toggle('active', el.dataset.cat === cat));
            populateShapes();
        });
        catTabsEl.appendChild(btn);
    }
    populateShapes();
}

// Render the shape buttons for the active category.
function populateShapes() {
    shapeList.innerHTML = '';
    const entries = Object.entries(catalog)
        .filter(([, def]) => activeCategory === 'All' || def.category === activeCategory)
        .sort(([, a], [, b]) => a.label.localeCompare(b.label));

    for (const [key, def] of entries) {
        const btn = document.createElement('button');
        btn.className = 'shape-btn' + (key === selectedShape ? ' selected' : '');
        btn.id = `shape-btn-${key}`;
        btn.dataset.shape = key;

        // Mini preview canvas
        const preview = document.createElement('canvas');
        preview.className = 'shape-preview';
        preview.width = 40;
        preview.height = 40;
        drawPreview(preview, def.cells);

        const label = document.createElement('span');
        label.className = 'shape-label';
        label.innerHTML = `${def.label}<small>${def.category}</small>`;

        btn.appendChild(preview);
        btn.appendChild(label);

        btn.addEventListener('click', () => selectShape(key));
        shapeList.appendChild(btn);
    }
}

// Draw a miniature pattern preview into a <canvas> element.
function drawPreview(canvas, cells) {
    if (!cells || cells.length === 0) return;
    const c = canvas.getContext('2d');
    c.fillStyle = '#0a0a18';
    c.fillRect(0, 0, canvas.width, canvas.height);

    // Compute bounding box of cells.
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const cell of cells) {
        minX = Math.min(minX, cell.x); minY = Math.min(minY, cell.y);
        maxX = Math.max(maxX, cell.x); maxY = Math.max(maxY, cell.y);
    }
    const pw = maxX - minX + 1;
    const ph = maxY - minY + 1;
    const scale = Math.max(1, Math.min(
        Math.floor((canvas.width - 4) / pw),
        Math.floor((canvas.height - 4) / ph)
    ));
    const offX = Math.floor((canvas.width - pw * scale) / 2);
    const offY = Math.floor((canvas.height - ph * scale) / 2);

    c.fillStyle = '#00ff88';
    for (const cell of cells) {
        c.fillRect(
            offX + (cell.x - minX) * scale,
            offY + (cell.y - minY) * scale,
            Math.max(1, scale - 1),
            Math.max(1, scale - 1)
        );
    }
}

// Select / deselect a shape.
function selectShape(key) {
    selectedShape = (selectedShape === key) ? null : key;
    ghostCells = [];

    // Update button highlight.
    document.querySelectorAll('.shape-btn').forEach(btn =>
        btn.classList.toggle('selected', btn.dataset.shape === selectedShape));

    if (selectedShape) {
        modeBadge.textContent = `✦ SHAPE: ${catalog[selectedShape].label}`;
        canvas.style.cursor = 'none'; // ghost preview replaces cursor
    } else {
        modeBadge.textContent = '✦ CELL MODE';
        canvas.style.cursor = 'crosshair';
    }
}

// ─── Ghost Preview ───────────────────────────────────────────────────────────
// Recompute the list of ghost cells relative to the current mouse world position.
function updateGhost() {
    ghostCells = [];
    if (!selectedShape || !catalog[selectedShape]) return;
    for (const c of catalog[selectedShape].cells) {
        ghostCells.push({ wx: mouseWorldX + c.x, wy: mouseWorldY + c.y });
    }
}

// ─── Optimistic Local State ───────────────────────────────────────────────────
// When a PLACE_SHAPE_ACK arrives, the cells are not sent back individually,
// so we need to paint them ourselves. We already do this below in the click
// handler, but ACK re-applies in case the optimistic paint was skipped.
function optimisticShape(payload) {
    // payload = { x, y, shape } — already reflected locally on click, nothing extra needed
}

// ─── Subscription Management ─────────────────────────────────────────────────
function worldToChunk(n) { return Math.floor(n / CHUNK_SIZE); }

function visibleChunks() {
    const minWX = Math.floor(camX / CELL_SIZE);
    const minWY = Math.floor(camY / CELL_SIZE);
    const maxWX = Math.floor((camX + canvas.width) / CELL_SIZE);
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

function updateSubscriptions() {
    if (ws.readyState !== WebSocket.OPEN) return;

    const needed = visibleChunks();
    const toSub = [];
    const toUnsub = [];

    for (const k of needed) if (!subscribedChunks.has(k)) toSub.push(k);
    for (const k of subscribedChunks) if (!needed.has(k)) toUnsub.push(k);

    const parse = k => { const [x, y] = k.split(',').map(Number); return { x, y }; };

    if (toSub.length) ws.send(JSON.stringify({ type: 'SUBSCRIBE', payload: { chunks: toSub.map(parse) } }));
    if (toUnsub.length) ws.send(JSON.stringify({ type: 'UNSUBSCRIBE', payload: { chunks: toUnsub.map(parse) } }));

    toSub.forEach(k => subscribedChunks.add(k));
    toUnsub.forEach(k => subscribedChunks.delete(k));

    updateHUD();
}

// ─── Server Reconciliation ───────────────────────────────────────────────────
function reconcileChunk({ x, y, cells }) {
    const baseX = x * CHUNK_SIZE;
    const baseY = y * CHUNK_SIZE;
    const maxX = baseX + CHUNK_SIZE;
    const maxY = baseY + CHUNK_SIZE;

    for (const key of localCells) {
        const i = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        if (wx >= baseX && wx < maxX && wy >= baseY && wy < maxY)
            localCells.delete(key);
    }

    for (const offset of (cells || [])) {
        const lx = offset % CHUNK_SIZE;
        const ly = Math.floor(offset / CHUNK_SIZE);
        localCells.add(`${baseX + lx},${baseY + ly}`);
    }
}

// ─── Local Prediction ────────────────────────────────────────────────────────
const DIRS = [[-1, -1], [0, -1], [1, -1], [-1, 0], [1, 0], [-1, 1], [0, 1], [1, 1]];

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
canvas.addEventListener('mousemove', e => {
    mouseWorldX = Math.floor((e.clientX + camX) / CELL_SIZE);
    mouseWorldY = Math.floor((e.clientY + camY) / CELL_SIZE);

    if (e.buttons === 1) {
        // Panning
        const nx = dragAnchorX - e.clientX;
        const ny = dragAnchorY - e.clientY;
        if (!isPanning && (Math.abs(nx - camX) > 3 || Math.abs(ny - camY) > 3))
            isPanning = true;
        camX = nx;
        camY = ny;
        updateHUD();
    } else if (selectedShape) {
        updateGhost();
    }
});

canvas.addEventListener('mousedown', e => {
    isPanning = false;
    dragAnchorX = e.clientX + camX;
    dragAnchorY = e.clientY + camY;
    if (!selectedShape) canvas.style.cursor = 'grabbing';
});

canvas.addEventListener('mouseup', () => {
    if (isPanning) updateSubscriptions();
    canvas.style.cursor = selectedShape ? 'none' : 'crosshair';
    setTimeout(() => { isPanning = false; }, 0);
});

canvas.addEventListener('click', e => {
    if (isPanning) return;
    const worldX = Math.floor((e.clientX + camX) / CELL_SIZE);
    const worldY = Math.floor((e.clientY + camY) / CELL_SIZE);

    if (selectedShape) {
        // Optimistic: paint all shape cells locally.
        if (catalog[selectedShape]) {
            for (const c of catalog[selectedShape].cells) {
                localCells.add(`${worldX + c.x},${worldY + c.y}`);
            }
        }
        if (ws.readyState === WebSocket.OPEN)
            ws.send(JSON.stringify({
                type: 'PLACE_SHAPE',
                payload: { x: worldX, y: worldY, shape: selectedShape }
            }));
    } else {
        // Single-cell spawn
        localCells.add(`${worldX},${worldY}`);
        if (ws.readyState === WebSocket.OPEN)
            ws.send(JSON.stringify({ type: 'SPAWN', payload: { x: worldX, y: worldY } }));
    }
});

// Keyboard: Escape deselects shape.
window.addEventListener('keydown', e => {
    if (e.key === 'Escape') selectShape(null);
});

// Clear-button in the panel.
clearBtn.addEventListener('click', () => selectShape(null));

// ─── Resize ───────────────────────────────────────────────────────────────────
function resize() {
    const prevW = canvas.width, prevH = canvas.height;
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
    if (prevW === 0) {
        camX = -Math.floor(canvas.width / 2);
        camY = -Math.floor(canvas.height / 2);
    }
    updateSubscriptions();
}
window.addEventListener('resize', resize);
canvas.width = 0;
canvas.height = 0;
resize();

// ─── HUD ─────────────────────────────────────────────────────────────────────
function updateHUD() {
    const wx = Math.floor((canvas.width / 2 + camX) / CELL_SIZE);
    const wy = Math.floor((canvas.height / 2 + camY) / CELL_SIZE);
    if (posEl) posEl.textContent =
        `Center: (${wx}, ${wy})  |  Chunks: ${subscribedChunks.size}`;
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
    for (let x = gx0; x < canvas.width; x += chunkPx) {
        ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, canvas.height); ctx.stroke();
    }
    for (let y = gy0; y < canvas.height; y += chunkPx) {
        ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(canvas.width, y); ctx.stroke();
    }

    // Living cells.
    ctx.fillStyle = '#00ff88';
    for (const key of localCells) {
        const i = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        const sx = wx * CELL_SIZE - camX;
        const sy = wy * CELL_SIZE - camY;
        if (sx > -CELL_SIZE && sx < canvas.width + CELL_SIZE &&
            sy > -CELL_SIZE && sy < canvas.height + CELL_SIZE)
            ctx.fillRect(sx, sy, CELL_SIZE - 1, CELL_SIZE - 1);
    }

    // Ghost preview for selected shape.
    if (selectedShape && ghostCells.length > 0) {
        ctx.fillStyle = 'rgba(0,255,136,0.35)';
        ctx.strokeStyle = 'rgba(0,255,136,0.6)';
        ctx.lineWidth = 0.5;
        for (const { wx, wy } of ghostCells) {
            const sx = wx * CELL_SIZE - camX;
            const sy = wy * CELL_SIZE - camY;
            if (sx > -CELL_SIZE && sx < canvas.width + CELL_SIZE &&
                sy > -CELL_SIZE && sy < canvas.height + CELL_SIZE) {
                ctx.fillRect(sx, sy, CELL_SIZE - 1, CELL_SIZE - 1);
                ctx.strokeRect(sx + 0.5, sy + 0.5, CELL_SIZE - 2, CELL_SIZE - 2);
            }
        }
    }

    requestAnimationFrame(draw);
}
draw();

// Kick off catalog fetch last so the WS connection is already set up.
fetchCatalog();
