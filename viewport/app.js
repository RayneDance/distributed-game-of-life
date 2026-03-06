'use strict';

// ─── Config ──────────────────────────────────────────────────────────────────
const BASE_CELL_SIZE = 8;    // pixels per cell at zoom 1×
const CHUNK_SIZE = 64;   // cells per chunk edge
const LOCAL_TICK_MS = 250;  // local prediction interval (approx server tick)
const VIEWPORT_PAD = 1;    // extra chunks to subscribe beyond visible edge
const MIN_ZOOM = 0.25;
const MAX_ZOOM = 16;

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
let localCells = new Set(); // "x,y" strings

// Camera: world-pixel offset of the canvas top-left corner (at zoom 1×).
let camX = 0;
let camY = 0;

// Zoom
let zoom = 1.0;

// Returns the current effective pixel size of one cell.
function cellPx() { return BASE_CELL_SIZE * zoom; }

// Mouse / drag state
let dragAnchorX = 0; // camX + clientX at drag start
let dragAnchorY = 0;
let isPanning = false;

// Shape-placement state
let selectedShape = null;
let catalog = {};
let ghostCells = [];
let mouseWorldX = 0;
let mouseWorldY = 0;
let activeCategory = 'All';

// Chunk subscriptions
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
        case 'SPAWN_ACK': break;
        case 'PLACE_SHAPE_ACK': break;
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

function categories() {
    const cats = new Set(['All']);
    for (const def of Object.values(catalog)) cats.add(def.category);
    return [...cats];
}

function buildPanel() {
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

function drawPreview(previewCanvas, cells) {
    if (!cells || cells.length === 0) return;
    const c = previewCanvas.getContext('2d');
    c.fillStyle = '#0a0a18';
    c.fillRect(0, 0, previewCanvas.width, previewCanvas.height);

    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const cell of cells) {
        minX = Math.min(minX, cell.x); minY = Math.min(minY, cell.y);
        maxX = Math.max(maxX, cell.x); maxY = Math.max(maxY, cell.y);
    }
    const pw = maxX - minX + 1;
    const ph = maxY - minY + 1;
    const scale = Math.max(1, Math.min(
        Math.floor((previewCanvas.width - 4) / pw),
        Math.floor((previewCanvas.height - 4) / ph)
    ));
    const offX = Math.floor((previewCanvas.width - pw * scale) / 2);
    const offY = Math.floor((previewCanvas.height - ph * scale) / 2);

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

function selectShape(key) {
    selectedShape = (selectedShape === key) ? null : key;
    ghostCells = [];
    document.querySelectorAll('.shape-btn').forEach(btn =>
        btn.classList.toggle('selected', btn.dataset.shape === selectedShape));
    if (selectedShape) {
        modeBadge.textContent = `✦ SHAPE: ${catalog[selectedShape].label}`;
        canvas.style.cursor = 'none';
    } else {
        modeBadge.textContent = '✦ CELL MODE';
        canvas.style.cursor = 'crosshair';
    }
}

// ─── Ghost Preview ───────────────────────────────────────────────────────────
function updateGhost(wx, wy) {
    mouseWorldX = wx;
    mouseWorldY = wy;
    ghostCells = [];
    if (!selectedShape || !catalog[selectedShape]) return;
    for (const c of catalog[selectedShape].cells) {
        ghostCells.push({ wx: wx + c.x, wy: wy + c.y });
    }
}

// ─── Coordinate Helpers ───────────────────────────────────────────────────────
function screenToWorld(sx, sy) {
    return {
        wx: Math.floor((sx + camX) / cellPx()),
        wy: Math.floor((sy + camY) / cellPx()),
    };
}

// ─── Subscription Management ─────────────────────────────────────────────────
function worldToChunk(n) { return Math.floor(n / CHUNK_SIZE); }

function visibleChunks() {
    const cp = cellPx();
    const minWX = Math.floor(camX / cp);
    const minWY = Math.floor(camY / cp);
    const maxWX = Math.floor((camX + canvas.width) / cp);
    const maxWY = Math.floor((camY + canvas.height) / cp);

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

// ─── Zoom ─────────────────────────────────────────────────────────────────────
// Zoom around a screen point (pivotX, pivotY) so that world coordinate under
// the pivot stays fixed on screen.
function applyZoom(factor, pivotX, pivotY) {
    const oldCp = cellPx();
    zoom = Math.max(MIN_ZOOM, Math.min(MAX_ZOOM, zoom * factor));
    const newCp = cellPx();
    // World coord under the pivot must stay the same:
    // (pivotX + camX) / oldCp === (pivotX + newCamX) / newCp
    camX = (pivotX + camX) / oldCp * newCp - pivotX;
    camY = (pivotY + camY) / oldCp * newCp - pivotY;
    updateSubscriptions();
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

// ─── Place action (shared by mouse click & touch tap) ────────────────────────
function placeAt(screenX, screenY) {
    const { wx, wy } = screenToWorld(screenX, screenY);
    if (selectedShape) {
        if (catalog[selectedShape]) {
            for (const c of catalog[selectedShape].cells)
                localCells.add(`${wx + c.x},${wy + c.y}`);
        }
        if (ws.readyState === WebSocket.OPEN)
            ws.send(JSON.stringify({ type: 'PLACE_SHAPE', payload: { x: wx, y: wy, shape: selectedShape } }));
    } else {
        localCells.add(`${wx},${wy}`);
        if (ws.readyState === WebSocket.OPEN)
            ws.send(JSON.stringify({ type: 'SPAWN', payload: { x: wx, y: wy } }));
    }
}

// ─── Mouse Input ─────────────────────────────────────────────────────────────
canvas.addEventListener('mousedown', e => {
    isPanning = false;
    dragAnchorX = e.clientX + camX;
    dragAnchorY = e.clientY + camY;
    if (!selectedShape) canvas.style.cursor = 'grabbing';
});

canvas.addEventListener('mousemove', e => {
    const { wx, wy } = screenToWorld(e.clientX, e.clientY);
    updateGhost(wx, wy);

    if (e.buttons === 1) {
        const nx = dragAnchorX - e.clientX;
        const ny = dragAnchorY - e.clientY;
        if (!isPanning && (Math.abs(nx - camX) > 3 || Math.abs(ny - camY) > 3))
            isPanning = true;
        camX = nx;
        camY = ny;
        updateHUD();
    }
});

canvas.addEventListener('mouseup', () => {
    if (isPanning) updateSubscriptions();
    canvas.style.cursor = selectedShape ? 'none' : 'crosshair';
    setTimeout(() => { isPanning = false; }, 0);
});

canvas.addEventListener('click', e => {
    if (isPanning) return;
    placeAt(e.clientX, e.clientY);
});

// Mouse wheel zoom
canvas.addEventListener('wheel', e => {
    e.preventDefault();
    const factor = e.deltaY < 0 ? 1.15 : 1 / 1.15;
    applyZoom(factor, e.clientX, e.clientY);
}, { passive: false });

// ─── Touch Input ─────────────────────────────────────────────────────────────
let touches = {};   // pointerId → {x, y}
let lastPinchDist = null;
let touchMoved = false;
let touchAnchorX = 0;
let touchAnchorY = 0;

function touchMidpoint() {
    const pts = Object.values(touches);
    return {
        x: (pts[0].x + pts[1].x) / 2,
        y: (pts[0].y + pts[1].y) / 2,
    };
}

function pinchDist() {
    const pts = Object.values(touches);
    const dx = pts[1].x - pts[0].x;
    const dy = pts[1].y - pts[0].y;
    return Math.sqrt(dx * dx + dy * dy);
}

canvas.addEventListener('touchstart', e => {
    e.preventDefault();
    touchMoved = false;
    for (const t of e.changedTouches) {
        touches[t.identifier] = { x: t.clientX, y: t.clientY };
    }
    if (Object.keys(touches).length === 1) {
        const t = Object.values(touches)[0];
        touchAnchorX = t.x + camX;
        touchAnchorY = t.y + camY;
        lastPinchDist = null;
    } else if (Object.keys(touches).length === 2) {
        lastPinchDist = pinchDist();
    }
}, { passive: false });

canvas.addEventListener('touchmove', e => {
    e.preventDefault();
    for (const t of e.changedTouches) {
        touches[t.identifier] = { x: t.clientX, y: t.clientY };
    }

    const count = Object.keys(touches).length;

    if (count === 1) {
        // Single-finger pan
        const t = Object.values(touches)[0];
        const nx = touchAnchorX - t.x;
        const ny = touchAnchorY - t.y;
        if (Math.abs(nx - camX) > 2 || Math.abs(ny - camY) > 2) touchMoved = true;
        camX = nx;
        camY = ny;
        updateHUD();
    } else if (count === 2) {
        // Pinch-to-zoom
        touchMoved = true;
        const newDist = pinchDist();
        if (lastPinchDist && lastPinchDist > 0) {
            const mid = touchMidpoint();
            applyZoom(newDist / lastPinchDist, mid.x, mid.y);
        }
        lastPinchDist = newDist;
    }
}, { passive: false });

canvas.addEventListener('touchend', e => {
    e.preventDefault();
    for (const t of e.changedTouches) {
        delete touches[t.identifier];
    }
    const remaining = Object.keys(touches).length;

    if (remaining === 0) {
        if (!touchMoved) {
            // Treat as a tap: place at the lifted touch position
            const t = e.changedTouches[0];
            placeAt(t.clientX, t.clientY);
        } else {
            updateSubscriptions();
        }
        lastPinchDist = null;
    } else if (remaining === 1) {
        // Transitioned from pinch back to pan — re-anchor
        const t = Object.values(touches)[0];
        touchAnchorX = t.x + camX;
        touchAnchorY = t.y + camY;
        lastPinchDist = null;
    }
}, { passive: false });

// ─── Keyboard ────────────────────────────────────────────────────────────────
window.addEventListener('keydown', e => {
    if (e.key === 'Escape') selectShape(null);
    // +/- keyboard zoom
    if (e.key === '+' || e.key === '=') applyZoom(1.25, canvas.width / 2, canvas.height / 2);
    if (e.key === '-') applyZoom(1 / 1.25, canvas.width / 2, canvas.height / 2);
});

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
    const cp = cellPx();
    const wx = Math.floor((canvas.width / 2 + camX) / cp);
    const wy = Math.floor((canvas.height / 2 + camY) / cp);
    if (posEl) posEl.textContent =
        `Center: (${wx}, ${wy})  |  Zoom: ${zoom.toFixed(2)}×  |  Chunks: ${subscribedChunks.size}`;
}

// ─── Render Loop ─────────────────────────────────────────────────────────────
function draw() {
    const cp = cellPx();

    ctx.fillStyle = '#080810';
    ctx.fillRect(0, 0, canvas.width, canvas.height);

    // Faint chunk boundary grid.
    const chunkPx = CHUNK_SIZE * cp;
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
    const cs = Math.max(1, cp - 1); // cell draw size with 1px gap (clamp at 1)
    ctx.fillStyle = '#00ff88';
    for (const key of localCells) {
        const i = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        const sx = wx * cp - camX;
        const sy = wy * cp - camY;
        if (sx > -cp && sx < canvas.width + cp &&
            sy > -cp && sy < canvas.height + cp)
            ctx.fillRect(sx, sy, cs, cs);
    }

    // Ghost preview.
    if (selectedShape && ghostCells.length > 0) {
        ctx.fillStyle = 'rgba(0,255,136,0.35)';
        ctx.strokeStyle = 'rgba(0,255,136,0.7)';
        ctx.lineWidth = 0.5;
        for (const { wx, wy } of ghostCells) {
            const sx = wx * cp - camX;
            const sy = wy * cp - camY;
            if (sx > -cp && sx < canvas.width + cp &&
                sy > -cp && sy < canvas.height + cp) {
                ctx.fillRect(sx, sy, cs, cs);
                if (cp > 3) ctx.strokeRect(sx + 0.5, sy + 0.5, cs - 1, cs - 1);
            }
        }
    }

    requestAnimationFrame(draw);
}
draw();

fetchCatalog();
