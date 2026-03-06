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
const shapePanel = document.getElementById('shape-panel');
const panelToggle = document.getElementById('panel-toggle');

// ─── Panel Collapse ───────────────────────────────────────────────────────────
function setPanelCollapsed(collapsed) {
    shapePanel.classList.toggle('collapsed', collapsed);
    panelToggle.textContent = collapsed ? '▶' : '◀';
    panelToggle.title = collapsed ? 'Expand shape panel' : 'Collapse shape panel';
}

panelToggle.addEventListener('click', () => {
    setPanelCollapsed(!shapePanel.classList.contains('collapsed'));
});

// Default: collapsed on narrow screens (mobile)
setPanelCollapsed(window.innerWidth <= 600);



// ─── State ───────────────────────────────────────────────────────────────────
// localCells maps "x,y" key → CSS color string (hsl(...))
let localCells = new Map();

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
// Tracks catalog keys that were created locally (via the piece editor)
// and are not known to the server. These are sent as PLACE_CUSTOM.
const customPieces = new Set();
let ghostCells = [];
let mouseWorldX = 0;
let mouseWorldY = 0;
let activeCategory = 'All';

// Chunk subscriptions
let subscribedChunks = new Set(); // "cx,cy" strings

// Pending command queue — each entry holds the optimistically-added cell keys
// for one outgoing WS message, in send order.
// ACK  → shift() and discard (cells already confirmed / will reconcile).
// ERROR → shift() and delete those keys from localCells (rollback).
const pendingCommands = [];

// Piece editor — exposed here so populateShapes() can call openEditor(key).
// Assigned by the editor IIFE below.
let openEditor = () => { };

// ─── Rate Limit Budget Tracker ───────────────────────────────────────────────
// Mirrors server config: player bucket = 50 max, 5 tokens/sec refill.
// Tracked client-side so the bar is instant with no extra server round-trips.
const RL_MAX = 50;
const RL_REFILL = 5;   // tokens per second
let rlTokens = RL_MAX;
let rlLastMs = Date.now();

const rlFill = document.getElementById('rl-fill');
const rlCount = document.getElementById('rl-count');

function rlRefill() {
    const now = Date.now();
    const elapsed = (now - rlLastMs) / 1000;
    const added = elapsed * RL_REFILL;   // fractional tokens
    if (added >= 0.01) {
        rlTokens = Math.min(RL_MAX, rlTokens + added);
        rlLastMs = now;
    }
}

function rlConsume(n) {
    rlRefill();
    rlTokens = Math.max(0, rlTokens - n);
    rlDraw();
}

function rlDraw() {
    const pct = rlTokens / RL_MAX;
    // Green (full) → Yellow (half) → Red (empty)
    const hue = Math.round(pct * 120);   // 120=green, 60=yellow, 0=red
    const color = `hsl(${hue}, 90%, 55%)`;
    if (rlFill) {
        rlFill.style.width = `${Math.round(pct * 100)}%`;
        rlFill.style.backgroundColor = color;
    }
    if (rlCount) rlCount.textContent = `${Math.floor(rlTokens)} / ${RL_MAX}`;
}

// Animate refill smoothly at ~200ms intervals.
setInterval(() => { rlRefill(); rlDraw(); }, 200);

// ─── Toast Notification ───────────────────────────────────────────────────────
function showToast(msg, color = '#ff4455') {
    const t = document.createElement('div');
    t.textContent = msg;
    t.style.cssText = [
        'position:fixed', 'bottom:60px', 'left:50%', 'transform:translateX(-50%)',
        `background:${color}22`, `border:1px solid ${color}66`, `color:${color}`,
        'padding:6px 16px', 'border-radius:6px', 'font-size:11px',
        'font-family:monospace', 'pointer-events:none',
        'transition:opacity 0.4s ease', 'opacity:1', 'z-index:999'
    ].join(';');
    document.body.appendChild(t);
    setTimeout(() => { t.style.opacity = '0'; }, 1800);
    setTimeout(() => t.remove(), 2300);
}

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
        case 'CHUNK_STATE':
            reconcileChunk(msg.payload);
            break;
        case 'SPAWN_ACK':
        case 'PLACE_SHAPE_ACK':
        case 'PLACE_CUSTOM_ACK':
            // Server confirmed the placement — discard the pending entry.
            // localCells already has the cells; server reconciliation will
            // correct any drift within one tick.
            pendingCommands.shift();
            break;
        case 'ERROR': {
            // Roll back the optimistic cells for this command so phantom
            // cells don't linger (especially in chunks with no active actor
            // that would never receive a reconciling CHUNK_STATE).
            const pending = pendingCommands.shift();
            if (pending) {
                for (const key of pending) localCells.delete(key);
            }
            if (msg.payload.code === 'RATE_LIMITED') {
                // Server confirmed we're empty — snap tokens to 0.
                rlTokens = 0; rlDraw();
                showToast('⚡ Rate limited — slow down!');
            } else {
                console.warn('Server error:', msg.payload.code, msg.payload.message);
            }
            break;
        }
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
        const isCustom = customPieces.has(key);

        const row = document.createElement('div');
        row.className = 'shape-row';

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
        row.appendChild(btn);

        if (isCustom) {
            const editBtn = document.createElement('button');
            editBtn.className = 'shape-action-btn edit';
            editBtn.title = 'Edit piece';
            editBtn.textContent = '✏';
            editBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                openEditor(key);
            });

            const delBtn = document.createElement('button');
            delBtn.className = 'shape-action-btn delete';
            delBtn.title = 'Delete piece';
            delBtn.textContent = '✕';
            delBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                if (selectedShape === key) selectShape(null);
                delete catalog[key];
                customPieces.delete(key);
                buildPanel();
                showToast(`"${def.label}" deleted`, '#ff6666');
            });

            row.appendChild(editBtn);
            row.appendChild(delBtn);
        }

        shapeList.appendChild(row);
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

    // Collect keys inside this chunk that were already live (keep their colors).
    const existing = new Map();
    for (const [key, color] of localCells) {
        const i = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        if (wx >= baseX && wx < maxX && wy >= baseY && wy < maxY) {
            existing.set(key, color);
            localCells.delete(key);
        }
    }

    // Re-add server cells: reuse existing color if the cell was already live,
    // otherwise assign a new random pastel (server-originated, so no neighbors known).
    for (const offset of (cells || [])) {
        const lx = offset % CHUNK_SIZE;
        const ly = Math.floor(offset / CHUNK_SIZE);
        const key = `${baseX + lx},${baseY + ly}`;
        localCells.set(key, existing.get(key) ?? randomPastelColor());
    }
}

// ─── Color Helpers ───────────────────────────────────────────────────────────

/** Generate a random vivid-pastel color in HSL. */
function randomPastelColor() {
    const h = Math.floor(Math.random() * 360);
    const s = Math.floor(Math.random() * 30) + 60;   // 60–90%
    const l = Math.floor(Math.random() * 20) + 60;   // 60–80%
    return `hsl(${h},${s}%,${l}%)`;
}

/**
 * Parse an `hsl(H,S%,L%)` string into [h, s, l] numbers.
 * Returns null if the string can't be parsed.
 */
function parseHSL(str) {
    const m = str.match(/hsl\((\d+(?:\.\d+)?),(\d+(?:\.\d+)?)%,(\d+(?:\.\d+)?)%\)/);
    return m ? [parseFloat(m[1]), parseFloat(m[2]), parseFloat(m[3])] : null;
}

/**
 * Average an array of HSL triples [[h,s,l], ...].
 * Hue is averaged circularly to avoid the 0°/360° wrap artefact.
 */
function averageHSL(hslList) {
    if (hslList.length === 0) return randomPastelColor();

    let sinSum = 0, cosSum = 0, sSum = 0, lSum = 0;
    for (const [h, s, l] of hslList) {
        const rad = (h * Math.PI) / 180;
        sinSum += Math.sin(rad);
        cosSum += Math.cos(rad);
        sSum += s;
        lSum += l;
    }
    const n = hslList.length;
    const hAvg = ((Math.atan2(sinSum / n, cosSum / n) * 180) / Math.PI + 360) % 360;
    const sAvg = sSum / n;
    const lAvg = lSum / n;
    return `hsl(${Math.round(hAvg)},${Math.round(sAvg)}%,${Math.round(lAvg)}%)`;
}

/**
 * Given a cell key "x,y", collect the HSL values of all live neighbors
 * and return their average.  Falls back to a random pastel if none are live.
 */
function neighborAverageColor(x, y) {
    const hslList = [];
    for (const [dx, dy] of DIRS) {
        const color = localCells.get(`${x + dx},${y + dy}`);
        if (color) {
            const hsl = parseHSL(color);
            if (hsl) hslList.push(hsl);
        }
    }
    return averageHSL(hslList);
}

// ─── Local Prediction ────────────────────────────────────────────────────────
const DIRS = [[-1, -1], [0, -1], [1, -1], [-1, 0], [1, 0], [-1, 1], [0, 1], [1, 1]];

function localTick() {
    // Count live neighbors for each candidate cell.
    const counts = new Map();  // key → neighbor count
    for (const key of localCells.keys()) {
        const i = key.indexOf(',');
        const x = +key.slice(0, i), y = +key.slice(i + 1);
        for (const [dx, dy] of DIRS) {
            const nk = `${x + dx},${y + dy}`;
            counts.set(nk, (counts.get(nk) || 0) + 1);
        }
    }

    const next = new Map();
    for (const [key, count] of counts) {
        const survives = count === 3 || (count === 2 && localCells.has(key));
        if (!survives) continue;

        if (localCells.has(key)) {
            // Surviving cell — keep its color.
            next.set(key, localCells.get(key));
        } else {
            // Newly born from neighbors — blend their colors.
            const i = key.indexOf(',');
            const x = +key.slice(0, i), y = +key.slice(i + 1);
            next.set(key, neighborAverageColor(x, y));
        }
    }

    localCells = next;
}

setInterval(localTick, LOCAL_TICK_MS);


// ─── Place action (shared by mouse click & touch tap) ────────────────────────
function placeAt(screenX, screenY) {
    const { wx, wy } = screenToWorld(screenX, screenY);
    const optimistic = [];

    if (selectedShape && catalog[selectedShape]) {
        // Pick one pastel color for the whole shape.
        const shapeColor = randomPastelColor();
        for (const c of catalog[selectedShape].cells) {
            const key = `${wx + c.x},${wy + c.y}`;
            localCells.set(key, shapeColor);
            optimistic.push(key);
        }
        rlConsume(optimistic.length); // charge tokens before sending
        pendingCommands.push(optimistic);

        if (ws.readyState === WebSocket.OPEN) {
            if (customPieces.has(selectedShape)) {
                // Client-only piece: send inline cell offsets as PLACE_CUSTOM.
                ws.send(JSON.stringify({
                    type: 'PLACE_CUSTOM',
                    payload: {
                        x: wx,
                        y: wy,
                        cells: catalog[selectedShape].cells,
                    },
                }));
            } else {
                // Server-known shape: send only the key.
                ws.send(JSON.stringify({
                    type: 'PLACE_SHAPE',
                    payload: { x: wx, y: wy, shape: selectedShape },
                }));
            }
        }
    } else {
        const key = `${wx},${wy}`;
        localCells.set(key, randomPastelColor());
        optimistic.push(key);
        rlConsume(1);
        pendingCommands.push(optimistic);
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

    // Living cells — each drawn with its individual color.
    const cs = Math.max(1, cp - 1); // cell draw size with 1px gap (clamp at 1)
    for (const [key, color] of localCells) {
        const i = key.indexOf(',');
        const wx = +key.slice(0, i);
        const wy = +key.slice(i + 1);
        const sx = wx * cp - camX;
        const sy = wy * cp - camY;
        if (sx > -cp && sx < canvas.width + cp &&
            sy > -cp && sy < canvas.height + cp) {
            ctx.fillStyle = color;
            ctx.fillRect(sx, sy, cs, cs);
        }
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

// ─── Piece Editor ────────────────────────────────────────────────────────────
(function () {
    // DOM refs
    const overlay = document.getElementById('piece-editor-overlay');
    const edCanvas = document.getElementById('editor-canvas');
    const edCtx = edCanvas.getContext('2d');
    const labelInput = document.getElementById('editor-label');
    const catInput = document.getElementById('editor-category');
    const drawBtn = document.getElementById('ed-draw-btn');
    const eraseBtn = document.getElementById('ed-erase-btn');
    const clearAllBtn = document.getElementById('ed-clear-btn');
    const centerBtn = document.getElementById('ed-center-btn');
    const cellCountEl = document.getElementById('editor-cell-count');
    const acceptBtn = document.getElementById('editor-accept');
    const cancelBtn = document.getElementById('editor-cancel');
    const closeBtn = document.getElementById('editor-close');
    const openBtn = document.getElementById('open-editor-btn');

    // Editor state
    const GRID_W = 100;
    const GRID_H = 100;
    // edCells[y][x] = true | undefined
    let edCells = [];

    // View state: virtual canvas coordinate of the editor canvas top-left
    let edCamX = 0;   // pixels of the 500px display canvas
    let edCamY = 0;
    let edZoom = 5;   // pixels per cell at 1× (display pixels)
    const ED_MIN_ZOOM = 2;
    const ED_MAX_ZOOM = 30;

    let edMode = 'draw'; // 'draw' | 'erase'
    let edPainting = false;
    let edPanAnchorX = 0;
    let edPanAnchorY = 0;
    let edIsPanning = false;
    let edPaintValue = true; // true = place, false = erase

    // ── helpers ──
    function cellPxEd() { return edZoom; }

    function resetEdCells() {
        edCells = [];
        for (let y = 0; y < GRID_H; y++) edCells[y] = [];
    }

    function edCenterView() {
        // Put the 100×100 grid in the center of the 500px canvas
        const cp = cellPxEd();
        edCamX = (500 - GRID_W * cp) / 2;
        edCamY = (500 - GRID_H * cp) / 2;
    }

    function edCountCells() {
        let n = 0;
        for (let y = 0; y < GRID_H; y++)
            for (let x = 0; x < GRID_W; x++)
                if (edCells[y] && edCells[y][x]) n++;
        return n;
    }

    function edUpdateCount() {
        cellCountEl.textContent = `${edCountCells()} cells`;
    }

    // ── render ──
    function edDraw() {
        const cp = cellPxEd();
        const W = edCanvas.width;
        const H = edCanvas.height;

        // Background
        edCtx.fillStyle = '#05050f';
        edCtx.fillRect(0, 0, W, H);

        // Grid boundary glow
        const gx0 = edCamX;
        const gy0 = edCamY;
        const gx1 = edCamX + GRID_W * cp;
        const gy1 = edCamY + GRID_H * cp;

        edCtx.strokeStyle = 'rgba(0, 170, 255, 0.25)';
        edCtx.lineWidth = 1;
        edCtx.strokeRect(gx0, gy0, GRID_W * cp, GRID_H * cp);

        // Cell lines (only when large enough)
        if (cp >= 4) {
            edCtx.strokeStyle = 'rgba(255,255,255,0.04)';
            edCtx.lineWidth = 0.5;
            for (let x = 0; x <= GRID_W; x++) {
                const lx = gx0 + x * cp;
                edCtx.beginPath(); edCtx.moveTo(lx, gy0); edCtx.lineTo(lx, gy1); edCtx.stroke();
            }
            for (let y = 0; y <= GRID_H; y++) {
                const ly = gy0 + y * cp;
                edCtx.beginPath(); edCtx.moveTo(gx0, ly); edCtx.lineTo(gx1, ly); edCtx.stroke();
            }
        }

        // Cells
        const cellDraw = Math.max(1, cp - (cp >= 3 ? 1 : 0));
        edCtx.fillStyle = '#00ff88';
        for (let y = 0; y < GRID_H; y++) {
            if (!edCells[y]) continue;
            for (let x = 0; x < GRID_W; x++) {
                if (!edCells[y][x]) continue;
                const sx = gx0 + x * cp;
                const sy = gy0 + y * cp;
                if (sx + cp < 0 || sx > W || sy + cp < 0 || sy > H) continue;
                edCtx.fillRect(sx, sy, cellDraw, cellDraw);
            }
        }
    }

    // ── coord helpers ──
    function screenToCell(sx, sy) {
        const cp = cellPxEd();
        return {
            cx: Math.floor((sx - edCamX) / cp),
            cy: Math.floor((sy - edCamY) / cp),
        };
    }

    function getCanvasPos(e) {
        const rect = edCanvas.getBoundingClientRect();
        // edCanvas display size = 500×500, but actual canvas resolution may differ
        const scaleX = edCanvas.width / rect.width;
        const scaleY = edCanvas.height / rect.height;
        return {
            x: (e.clientX - rect.left) * scaleX,
            y: (e.clientY - rect.top) * scaleY,
        };
    }

    function edPaintCell(sx, sy) {
        const { cx, cy } = screenToCell(sx, sy);
        if (cx < 0 || cx >= GRID_W || cy < 0 || cy >= GRID_H) return;
        if (!edCells[cy]) edCells[cy] = [];
        const newVal = (edMode === 'erase') ? false : edPaintValue;
        if (!!edCells[cy][cx] === newVal) return; // no change
        edCells[cy][cx] = newVal || undefined;
        edUpdateCount();
    }

    // ── mouse events ──
    edCanvas.addEventListener('mousedown', e => {
        e.preventDefault();
        const { x, y } = getCanvasPos(e);
        if (e.button === 1 || e.button === 2 || e.altKey) {
            // Middle / right / alt = pan
            edIsPanning = true;
            edPanAnchorX = x - edCamX;
            edPanAnchorY = y - edCamY;
        } else {
            edIsPanning = false;
            edPainting = true;
            // Determine if we start on a live cell (for toggle erasing)
            const { cx, cy } = screenToCell(x, y);
            const isLive = cx >= 0 && cx < GRID_W && cy >= 0 && cy < GRID_H
                && edCells[cy] && edCells[cy][cx];
            edPaintValue = (edMode === 'erase') ? false : !isLive;
            edPaintCell(x, y);
        }
        edDraw();
    });

    edCanvas.addEventListener('mousemove', e => {
        e.preventDefault();
        const { x, y } = getCanvasPos(e);
        if (edIsPanning) {
            edCamX = x - edPanAnchorX;
            edCamY = y - edPanAnchorY;
            edDraw();
        } else if (edPainting) {
            edPaintCell(x, y);
            edDraw();
        }
    });

    window.addEventListener('mouseup', () => {
        edPainting = false;
        edIsPanning = false;
    });

    edCanvas.addEventListener('contextmenu', e => e.preventDefault());

    edCanvas.addEventListener('wheel', e => {
        e.preventDefault();
        const { x, y } = getCanvasPos(e);
        const oldZoom = edZoom;
        const factor = e.deltaY < 0 ? 1.2 : 1 / 1.2;
        edZoom = Math.max(ED_MIN_ZOOM, Math.min(ED_MAX_ZOOM, edZoom * factor));
        // Zoom around the cursor
        edCamX = x - (x - edCamX) / oldZoom * edZoom;
        edCamY = y - (y - edCamY) / oldZoom * edZoom;
        edDraw();
    }, { passive: false });

    // ── toolbar buttons ──
    function setEdMode(mode) {
        edMode = mode;
        drawBtn.classList.toggle('active', mode === 'draw');
        eraseBtn.classList.toggle('active', mode === 'erase');
        edCanvas.style.cursor = mode === 'erase' ? 'cell' : 'crosshair';
    }

    drawBtn.addEventListener('click', () => setEdMode('draw'));
    eraseBtn.addEventListener('click', () => setEdMode('erase'));

    clearAllBtn.addEventListener('click', () => {
        resetEdCells();
        edUpdateCount();
        edDraw();
    });

    centerBtn.addEventListener('click', () => {
        edCenterView();
        edDraw();
    });

    // ── open / close ──
    // editKey: if set, pre-populate the canvas with that piece for editing.
    openEditor = function openEditor(editKey = null) {
        resetEdCells();
        edUpdateCount();
        setEdMode('draw');
        overlay.dataset.editKey = editKey || '';

        if (editKey && catalog[editKey]) {
            // Pre-populate fields and canvas from the existing piece.
            const def = catalog[editKey];
            labelInput.value = def.label;
            catInput.value = def.category;
            // Paint the existing cells into the editor grid.
            for (const c of def.cells) {
                if (c.y >= 0 && c.y < GRID_H && c.x >= 0 && c.x < GRID_W) {
                    if (!edCells[c.y]) edCells[c.y] = [];
                    edCells[c.y][c.x] = true;
                }
            }
        } else {
            labelInput.value = '';
            catInput.value = 'Custom';
        }

        overlay.classList.add('open');
        edUpdateCount();
        edCenterView();
        edDraw();
    }

    function closeEditor() {
        overlay.classList.remove('open');
    }

    openBtn.addEventListener('click', () => openEditor());
    closeBtn.addEventListener('click', closeEditor);
    cancelBtn.addEventListener('click', closeEditor);

    // Close on ESC (but don't propagate to main app)
    window.addEventListener('keydown', e => {
        if (!overlay.classList.contains('open')) return;
        if (e.key === 'Escape') {
            e.stopImmediatePropagation();
            closeEditor();
        }
    }, { capture: true });

    // Click backdrop to close
    overlay.addEventListener('click', e => {
        if (e.target === overlay) closeEditor();
    });

    // ── accept ──
    acceptBtn.addEventListener('click', () => {
        const label = labelInput.value.trim();
        const category = catInput.value.trim() || 'Custom';

        if (!label) { showToast('⚠ Display name is required', '#ffaa00'); return; }

        // Build cells array: {x, y} offsets relative to bounding-box top-left.
        const rawCells = [];
        for (let y = 0; y < GRID_H; y++)
            for (let x = 0; x < GRID_W; x++)
                if (edCells[y] && edCells[y][x]) rawCells.push({ x, y });

        if (rawCells.length === 0) { showToast('⚠ Draw at least one cell', '#ffaa00'); return; }

        // Normalize: subtract bounding-box min so offsets start at (0,0).
        const minX = Math.min(...rawCells.map(c => c.x));
        const minY = Math.min(...rawCells.map(c => c.y));
        const cells = rawCells.map(c => ({ x: c.x - minX, y: c.y - minY }));

        // Reuse the existing key when editing; generate a UUID for new pieces.
        const editKey = overlay.dataset.editKey;
        const key = editKey || crypto.randomUUID();

        // Add / overwrite in the client catalog and mark as custom.
        catalog[key] = { label, category, cells };
        customPieces.add(key);

        // If editing the currently selected shape, refresh the ghost.
        if (selectedShape === key) {
            selectShape(key);
        }

        buildPanel();
        showToast(editKey ? `✓ "${label}" updated` : `✓ "${label}" added to catalog`, '#00ff88');
        closeEditor();
    });
})();

fetchCatalog();
