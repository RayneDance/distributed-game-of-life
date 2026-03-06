const canvas = document.getElementById('grid');
const ctx = canvas.getContext('2d');
const statusEl = document.getElementById('status');

// Configuration
const CELL_SIZE = 4;
const CHUNK_SIZE = 64;

let activeCells = new Set();

// Resize canvas
function resize() {
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
}
window.addEventListener('resize', resize);
resize();

// WebSocket Connection
const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
const ws = new WebSocket(`${wsProtocol}//${location.host}/ws`);

ws.onopen = () => {
    statusEl.innerText = "Connected";
    statusEl.style.color = "#00ff00";
};

ws.onclose = () => {
    statusEl.innerText = "Disconnected";
    statusEl.style.color = "#ff0000";
};

ws.onmessage = (event) => {
    const msg = JSON.parse(event.data);
    if (msg.type === "ERROR") {
        console.error("Server Error:", msg.payload.message);
    } else if (msg.type === "SPAWN_ACK") {
        const key = `${msg.payload.x},${msg.payload.y}`;
        activeCells.add(key);
    }
};

// Interaction
canvas.addEventListener('click', (e) => {
    const worldX = Math.floor((e.clientX - canvas.width / 2) / CELL_SIZE);
    const worldY = Math.floor((e.clientY - canvas.height / 2) / CELL_SIZE);

    spawnCell(worldX, worldY);
});

function spawnCell(x, y) {
    if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({
            type: "SPAWN",
            payload: { x, y }
        }));
    }
}

function drawCell(x, y) {
    const screenX = (x * CELL_SIZE) + (canvas.width / 2);
    const screenY = (y * CELL_SIZE) + (canvas.height / 2);
    
    ctx.fillStyle = '#00ff00';
    ctx.fillRect(screenX, screenY, CELL_SIZE, CELL_SIZE);
}

// Initial draw loop
function loop() {
    ctx.fillStyle = 'rgba(18, 18, 18, 0.2)';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    
    for (const cell of activeCells) {
        const [x, y] = cell.split(',').map(Number);
        drawCell(x, y);
    }
    
    requestAnimationFrame(loop);
}
loop();
