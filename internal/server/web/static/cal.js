(function () {
  const body = document.body;
  const UUID = body.dataset.uuid, TZ = body.dataset.tz, EDIT = body.dataset.mode === 'edit';
  const API = '/api/cal/' + UUID;
  const SNAP = 15 * 60;            // seconds
  const $ = (id) => document.getElementById(id);
  const cal = $('cal');
  if (EDIT) body.classList.add('edit');

  // ---- timezone helpers: all dates are shown in the server TIMEZONE ----
  const dtf = new Intl.DateTimeFormat('en-US', { timeZone: TZ, hourCycle: 'h23',
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
  function parts(ts) { // unix seconds -> wall-clock parts in TZ
    const p = {};
    for (const x of dtf.formatToParts(new Date(ts * 1000))) if (x.type !== 'literal') p[x.type] = +x.value;
    return p;
  }
  function offset(ts) { // TZ offset in seconds at ts
    const p = parts(ts);
    return (Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) / 1000) - ts;
  }
  function wallToTs(y, m, d, sec) { // wall time in TZ -> unix seconds
    const guess = Date.UTC(y, m - 1, d) / 1000 + sec;
    let ts = guess - offset(guess);
    ts = guess - offset(ts);
    return ts;
  }
  function dayStart(ts) { const p = parts(ts); return wallToTs(p.year, p.month, p.day, 0); }
  function addDays(ts, n) { const p = parts(ts); const d = new Date(Date.UTC(p.year, p.month - 1, p.day + n)); return wallToTs(d.getUTCFullYear(), d.getUTCMonth() + 1, d.getUTCDate(), 0); }
  const pad = (n) => String(n).padStart(2, '0');
  function hm(ts) { const p = parts(ts); return p.hour + ':' + pad(p.minute); }
  function localInput(ts) { const p = parts(ts); return `${p.year}-${pad(p.month)}-${pad(p.day)}T${pad(p.hour)}:${pad(p.minute)}`; }
  function fromInput(v) { const m = /^(\d+)-(\d+)-(\d+)T(\d+):(\d+)/.exec(v); return m ? wallToTs(+m[1], +m[2], +m[3], (+m[4]) * 3600 + (+m[5]) * 60) : NaN; }
  const now = () => Math.floor(Date.now() / 1000);
  const clockDate = new Intl.DateTimeFormat('en-US', { timeZone: TZ, weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' });
  const clockTime = new Intl.DateTimeFormat('en-US', { timeZone: TZ, hour: '2-digit', minute: '2-digit', second: '2-digit' });
  function updateClock() {
    const date = new Date();
    $('clock-date').textContent = clockDate.format(date);
    $('clock-time').textContent = clockTime.format(date);
    $('clock-time').dateTime = date.toISOString();
  }
  updateClock();
  setInterval(updateClock, 1000);
  const DOW = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  function dow(ts) { const p = parts(ts); return new Date(Date.UTC(p.year, p.month - 1, p.day)).getUTCDay(); }

  // ---- state ----
  let days = window.innerWidth < 600 ? 3 : 7;
  $('days').value = String(days);
  let start = 0, events = [], version = -1, days0 = [];
  function setAnchor(ts) {
    start = days === 7 ? addDays(dayStart(ts), -dow(ts)) : dayStart(ts);
  }
  setAnchor(now());

  async function api(method, path, data) {
    const r = await fetch(API + path, { method, credentials: 'same-origin',
      headers: data ? { 'Content-Type': 'application/json' } : {}, body: data ? JSON.stringify(data) : undefined });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

  // ---- rendering ----
  function hourPx() { return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--hour')) || 44; }

  function build() {
    cal.style.setProperty('--days', days);
    cal.innerHTML = '';
    days0 = [];
    cal.appendChild(document.createElement('div')).className = 'dh';
    const today = dayStart(now());
    for (let i = 0; i < days; i++) {
      const d = addDays(start, i);
      days0.push(d);
      const h = document.createElement('div');
      h.className = 'dh' + (d === today ? ' today' : '');
      h.innerHTML = `${DOW[dow(d)]}<b>${parts(d).day}</b>`;
      cal.appendChild(h);
    }
    const gut = document.createElement('div');
    gut.className = 'gutter';
    for (let hr = 1; hr < 24; hr++) {
      const s = document.createElement('span');
      s.style.top = `calc(var(--hour) * ${hr})`;
      s.textContent = (hr % 12 || 12) + (window.innerWidth < 700 ? (hr < 12 ? 'a' : 'p') : (hr < 12 ? ' AM' : ' PM'));
      gut.appendChild(s);
    }
    cal.appendChild(gut);
    for (let i = 0; i < days; i++) {
      const col = document.createElement('div');
      col.className = 'day';
      col.dataset.i = i;
      cal.appendChild(col);
      if (EDIT) attachDrag(col, i);
    }
    const a = parts(start), b = parts(addDays(start, days - 1));
    $('range').textContent = days === 1 ? `${MON[a.month - 1]} ${a.day}, ${a.year}`
      : a.month === b.month ? `${MON[a.month - 1]} ${a.day} – ${b.day}, ${a.year}`
      : `${MON[a.month - 1]} ${a.day} – ${MON[b.month - 1]} ${b.day}, ${b.year}`;
  }

  // secs since local midnight of day i (handles DST days: day length may be 23/25h)
  function secOf(i, ts) { return ts - days0[i]; }

  function paint() {
    const H = hourPx();
    cal.querySelectorAll('.ev,.now').forEach((n) => n.remove());
    const cols = cal.querySelectorAll('.day');
    for (let i = 0; i < days; i++) {
      const d0 = days0[i], d1 = i + 1 < days ? days0[i + 1] : addDays(d0, 1);
      const evs = events.filter((e) => e.start < d1 && e.end > d0).sort((a, b) => a.start - b.start || a.end - b.end);
      // simple overlap lanes
      const lanes = [];
      const placed = evs.map((e) => {
        let l = lanes.findIndex((end) => end <= e.start);
        if (l < 0) { l = lanes.length; lanes.push(0); }
        lanes[l] = e.end;
        return { e, l };
      });
      for (const { e, l } of placed) {
        const s = Math.max(e.start, d0), en = Math.min(e.end, d1);
        const el = document.createElement('div');
        el.className = `ev ${e.status}${e.auto ? ' auto' : ''}`;
        el.style.top = (secOf(i, s) / 3600) * H + 'px';
        el.style.height = Math.max(14, ((en - s) / 3600) * H - 2) + 'px';
        if (lanes.length > 1) {
          el.style.left = `calc(${(100 / lanes.length) * l}% + 2px)`;
          el.style.right = 'auto';
          el.style.width = `calc(${100 / lanes.length}% - 4px)`;
        }
        const t = document.createElement('div');
        t.className = 't';
        t.textContent = e.status === 'working' ? 'Working' : 'Unavailable';
        const tm = document.createElement('span');
        tm.className = 'tm';
        tm.textContent = ` ${hm(e.start)}–${hm(e.end)}`;
        t.appendChild(tm);
        el.appendChild(t);
        if (e.description) {
          const dd = document.createElement('div');
          dd.className = 'd';
          dd.textContent = e.description;
          el.appendChild(dd);
        }
        el.title = `${t.textContent}${e.description ? '\n' + e.description : ''}${e.auto ? '\n(auto from live status)' : ''}`;
        if (EDIT) el.addEventListener('pointerdown', (ev) => { ev.stopPropagation(); });
        if (EDIT) el.addEventListener('click', (ev) => { ev.stopPropagation(); openDialog(e); });
        cols[i].appendChild(el);
      }
      const n = now();
      if (n >= d0 && n < d1) {
        const line = document.createElement('div');
        line.className = 'now';
        line.style.top = (secOf(i, n) / 3600) * H + 'px';
        cols[i].appendChild(line);
      }
    }
  }

  function setStatus(st) {
    const header = document.querySelector('.live');
    header.classList.toggle('is-online', !!st.online);
    header.classList.toggle('is-offline', !st.online);
    window.setAvatarOnline && window.setAvatarOnline(st.online);
    const s = $('status');
    s.textContent = st.online ? 'Currently Online' : 'Currently Offline';
    s.className = 'status ' + (st.online ? 'on' : 'off');
    let sub = '';
    if (st.online) sub = 'At the computer';
    else if (st.running) sub = 'Away from the computer';
    else if (st.last_active) sub = 'Last active ' + ago(st.last_active);
    $('status-sub').textContent = sub;
  }
  function ago(ts) {
    const s = now() - ts;
    if (s < 3600) return Math.max(1, Math.round(s / 60)) + ' min ago';
    if (s < 86400) return Math.round(s / 3600) + ' h ago';
    return Math.round(s / 86400) + ' d ago';
  }

  async function load() {
    try {
      const j = await api('GET', `/events?from=${start}&to=${addDays(start, days)}`);
      events = j.events; version = j.status.version;
      setStatus(j.status);
      paint();
    } catch (e) { $('status-sub').textContent = 'Connection problem: ' + e.message; }
  }

  // poll live status; reload events only when the calendar changed
  async function poll() {
    if (document.hidden) return;
    try {
      const st = await api('GET', '/status');
      setStatus(st);
      if (st.version !== version && !dragging && !$('dlg').open) await load();
      else paint();
    } catch (e) { /* ignore transient errors */ }
  }
  setInterval(poll, 20000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(); });

  // ---- drag to create ----
  let dragging = null;
  function attachDrag(col, i) {
    col.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      const y0 = yToSec(col, e.clientY);
      const startY = e.clientY, startX = e.clientX;
      let sel = null, active = e.pointerType === 'mouse', timer = null;
      dragging = { col, i, a: Math.floor(y0 / SNAP) * SNAP, b: Math.floor(y0 / SNAP) * SNAP + SNAP };
      // on touch, wait for a long-ish press before dragging so the page can still scroll
      if (!active) timer = setTimeout(() => { active = true; col.setPointerCapture(e.pointerId); show(); }, 250);
      else col.setPointerCapture(e.pointerId);
      function show() {
        if (!sel) { sel = document.createElement('div'); sel.className = 'sel'; col.appendChild(sel); }
        const H = hourPx(), lo = Math.min(dragging.a, dragging.b), hi = Math.max(dragging.a, dragging.b);
        sel.style.top = (lo / 3600) * H + 'px';
        sel.style.height = ((hi - lo) / 3600) * H + 'px';
        sel.textContent = `${hm(days0[i] + lo)}–${hm(days0[i] + hi)}`;
      }
      function move(ev) {
        if (!active) {
          if (Math.abs(ev.clientY - startY) > 8 || Math.abs(ev.clientX - startX) > 8) cancel();
          return;
        }
        ev.preventDefault();
        const y = yToSec(col, ev.clientY);
        const a0 = Math.floor(y0 / SNAP) * SNAP;
        if (y >= y0) { dragging.a = a0; dragging.b = Math.max(a0 + SNAP, Math.ceil(y / SNAP) * SNAP); }
        else { dragging.a = a0 + SNAP; dragging.b = Math.floor(y / SNAP) * SNAP; }
        show();
      }
      function cleanup() {
        clearTimeout(timer);
        col.removeEventListener('pointermove', move);
        col.removeEventListener('pointerup', up);
        col.removeEventListener('pointercancel', cancel);
        if (sel) sel.remove();
      }
      function cancel() { cleanup(); dragging = null; }
      function up() {
        const wasActive = active;
        cleanup();
        const d = dragging; dragging = null;
        if (!d) return;
        let lo = Math.min(d.a, d.b), hi = Math.max(d.a, d.b);
        if (hi - lo <= SNAP && wasActive && e.pointerType === 'mouse') hi = lo + 3600; // single click = 1h
        if (!wasActive) { lo = Math.floor(y0 / 1800) * 1800; hi = lo + 3600; } // tap
        openDialog({ start: days0[i] + lo, end: days0[i] + hi, status: 'working', description: '' });
      }
      col.addEventListener('pointermove', move);
      col.addEventListener('pointerup', up);
      col.addEventListener('pointercancel', cancel);
    });
  }
  function yToSec(col, clientY) {
    const r = col.getBoundingClientRect();
    return Math.min(86400, Math.max(0, ((clientY - r.top) / hourPx()) * 3600));
  }

  // ---- event dialog ----
  let editing = null;
  function openDialog(e) {
    editing = e;
    $('dlg-title').textContent = e.id ? 'Edit block' : 'New block';
    document.querySelector(`input[name=status][value=${e.status}]`).checked = true;
    $('ev-start').value = localInput(e.start);
    $('ev-end').value = localInput(e.end);
    $('ev-desc').value = e.description || '';
    $('ev-del').hidden = !e.id;
    $('ev-err').textContent = e.auto ? 'Created automatically from live status. Editing makes it a normal block.' : '';
    $('dlg').showModal();
    if (!e.id) $('ev-desc').focus();
  }
  if (EDIT) {
    $('evform').addEventListener('submit', async (ev) => {
      ev.preventDefault();
      const data = {
        status: document.querySelector('input[name=status]:checked').value,
        start: fromInput($('ev-start').value), end: fromInput($('ev-end').value),
        description: $('ev-desc').value,
      };
      if (!(data.end > data.start)) { $('ev-err').textContent = 'End must be after start.'; return; }
      try {
        if (editing.id) await api('PUT', '/events/' + editing.id, data);
        else await api('POST', '/events', data);
        $('dlg').close();
        load();
      } catch (e) { $('ev-err').textContent = e.message; }
    });
    $('ev-del').addEventListener('click', async () => {
      if (!editing.id || !confirm('Delete this block?')) return;
      try { await api('DELETE', '/events/' + editing.id); $('dlg').close(); load(); }
      catch (e) { $('ev-err').textContent = e.message; }
    });
    $('ev-cancel').addEventListener('click', () => $('dlg').close());
  }

  // ---- toolbar ----
  const step = () => (days === 7 ? 7 : days);
  $('today').onclick = () => { setAnchor(now()); build(); load(); scrollToNow(); };
  $('prev').onclick = () => { start = addDays(start, -step()); build(); load(); };
  $('next').onclick = () => { start = addDays(start, step()); build(); load(); };
  $('days').onchange = () => { days = +$('days').value; setAnchor(start + 43200); build(); load(); };
  $('subscribe').onclick = () => {
    const url = $('ics').textContent.trim();
    $('webcal').href = url.replace(/^https?:/, 'webcal:');
    $('subdlg').showModal();
  };
  document.addEventListener('keydown', (e) => {
    if (document.querySelector('dialog[open]') || /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) return;
    if (e.key === 'ArrowLeft') $('prev').click();
    if (e.key === 'ArrowRight') $('next').click();
    if (e.key === 't') $('today').click();
  });

  function scrollToNow() {
    const p = parts(now());
    cal.scrollTop = Math.max(0, (p.hour - 2) * hourPx());
  }

  // ---- visitor message ----
  const form = $('msg');
  if (form) {
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const note = $('msg-note');
      const text = $('msg-text').value.trim();
      if (!text) return;
      form.querySelector('button').disabled = true;
      try {
        const j = await api('POST', '/message', { from: $('msg-from').value.trim(), text });
        $('msg-text').value = '';
        note.textContent = j.delivering ? 'Sent — it will pop up on their PC within a minute.'
          : 'Saved — it will pop up if their PC comes online within the next hour.';
      } catch (err) { note.textContent = err.message; }
      form.querySelector('button').disabled = false;
    });
  }

  build();
  scrollToNow();
  load();
  setInterval(paint, 60000); // move the "now" line
})();
