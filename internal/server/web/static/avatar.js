// Pixel-mosaic avatar on a 32x24 canvas (scaled up with pixelated rendering).
// Online: a person typing at a glowing computer. Offline: an empty dark computer with a spider on a web.
(function () {
  const cv = document.getElementById('avatar');
  if (!cv) return;
  const g = cv.getContext('2d');
  const W = 32, H = 24;
  let online = null, t = 0;

  const px = (x, y, c) => { g.fillStyle = c; g.fillRect(x, y, 1, 1); };
  const rect = (x, y, w, h, c) => { g.fillStyle = c; g.fillRect(x, y, w, h); };

  function desk(lit) {
    rect(0, 20, W, 4, '#b98a5e');            // desk
    rect(0, 20, W, 1, '#d1a77c');
    rect(10, 5, 15, 11, '#3a3f4b');          // monitor frame
    rect(11, 6, 13, 9, lit ? '#9fd4ff' : '#1d222b');
    rect(16, 16, 3, 3, '#3a3f4b');           // stand
    rect(14, 19, 7, 1, '#2c313a');
  }

  function drawOnline() {
    desk(true);
    // scrolling code lines on screen
    for (let i = 0; i < 4; i++) {
      const len = 3 + ((i * 5 + Math.floor(t / 8)) % 8);
      rect(12, 7 + i * 2, len, 1, i % 2 ? '#2b7bd6' : '#ffffff');
    }
    if ((t >> 3) & 1) rect(12 + 3 + ((Math.floor(t / 8) + 3) % 8), 13, 1, 1, '#ffffff'); // cursor blink
    // person (back view, left of monitor)
    const bob = (t >> 4) & 1;
    rect(2, 7 + bob, 6, 6, '#f2c9a0');        // head
    rect(2, 6 + bob, 6, 2, '#5b3a29');        // hair
    rect(1, 7 + bob, 1, 3, '#5b3a29');
    rect(1, 13, 8, 7, '#e85d75');             // shirt
    rect(2, 12, 6, 1, '#f2c9a0');             // neck
    // keyboard + typing hands
    rect(8, 19, 12, 1, '#6b7280');
    const a = (t >> 2) & 1, b = ((t >> 2) + 1) & 1;
    rect(8, 15 + a, 3, 2, '#e85d75'); px(10 + a, 18 - a, '#f2c9a0'); px(11, 17 + a, '#f2c9a0');
    rect(9, 14, 1, 2, '#e85d75');
    px(14 + b, 18, '#f2c9a0');
    // little sparkles
    if (t % 40 < 20) px(27, 4, '#ffd166');
    if ((t + 20) % 40 < 20) px(29, 7, '#ffd166');
  }

  function drawOffline() {
    desk(false);
    px(23, 14, (t >> 5) & 1 ? '#f59e0b' : '#1d222b'); // standby LED
    // cobweb in top-left corner
    const web = '#cfd6e0';
    for (let i = 0; i < 9; i++) { px(i, 0, web); px(0, i, web); px(i, i, web); }
    for (let i = 0; i < 6; i++) { px(i, 4, web); px(4, i, web); }
    for (let i = 0; i < 4; i++) { px(i, 7, web); px(7, i, web); }
    px(2, 2, web); px(5, 5, web);
    // spider climbing its thread up and down over the monitor
    const sx = 27;
    const y = 2 + Math.round((Math.sin(t / 30) + 1) * 6);
    for (let i = 0; i < y; i++) px(sx, i, '#9aa4b2');
    rect(sx - 1, y, 3, 2, '#222');
    const leg = (t >> 3) & 1;
    px(sx - 2, y + leg, '#222'); px(sx + 2, y + 1 - leg, '#222');
    px(sx - 2, y + 2 - leg, '#222'); px(sx + 2, y + 1 + leg, '#222');
    px(sx - 1, y + 2, '#ff4d4d'); px(sx + 1, y + 2, '#ff4d4d');
    // dust "z"
    if (t % 120 < 60) { px(6, 12, '#9aa4b2'); px(7, 12, '#9aa4b2'); px(6, 13, '#9aa4b2'); px(7, 14, '#9aa4b2'); px(6, 14, '#9aa4b2'); }
  }

  function draw() {
    g.clearRect(0, 0, W, H);
    rect(0, 0, W, H, online ? '#eaf6ff' : '#e6e8ec');
    if (online) drawOnline(); else drawOffline();
  }
  const still = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  let last = 0;
  function loop(ts) {
    if (ts - last >= 33) { last = ts; t++; draw(); } // ~30 fps
    requestAnimationFrame(loop);
  }
  window.setAvatarOnline = (v) => { online = !!v; if (still) draw(); };
  online = false;
  if (still) draw(); else requestAnimationFrame(loop);
})();
