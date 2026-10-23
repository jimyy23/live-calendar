# Live Calendar — design plan

## Components
| Part | Tech | Notes |
|---|---|---|
| Server | Go stdlib `net/http` + `html/template`, `modernc.org/sqlite` (pure Go, CGO off) | single static binary, port 8933 |
| Web UI | embedded vanilla JS/CSS, no build step | week view, drag-create, pixel-mosaic avatar |
| Windows client | Go, `-H windowsgui`, stdlib syscalls + `x/sys/windows/registry` | ~3 MB, single instance, no console |
| Deploy | multi-stage Dockerfile (builds server + windows exe), docker-compose, `/data` volume | Easypanel: "Dockerfile" app, port 8933, mount `/data` |

## Access model
* `/` — admin password (`ADMIN_PASSWORD`) → HMAC-signed cookie. Create/delete users, rotate tokens, copy links, download client.
* `/view/{uuid}?team_token=…` — read-only calendar + live status + "send a message" box.
* `/edit/{uuid}` — token form; `?token=…` sets an HttpOnly cookie and redirects (token removed from URL). Admin cookie can edit too.
* `/ical/{uuid}.ics` — read-only subscription feed; the random uuid is the secret.
* `/api/client/*` — HTTP Basic `username:token`.

## Data (SQLite, unix seconds UTC)
users(id, username, uuid, token, cal_version, sync_cursor, sync_at) ·
events(id, user_id, start_at, end_at, status working|unavailable, description, auto) ·
devices(user_id, device_id random, active, last_seen, last_active) ·
messages(id, user_id, sender, body, created_at, first_delivered_at) · deliveries(message_id, device_id)

## Live status
Client pings every 60 s (immediately when idle state flips) with `{device_id, active}` only.
`active` = keyboard/mouse input within last 5 min (`GetLastInputInfo`).
A device is *running* if last_seen ≤ 150 s ago. User is **online** if any running device is active.

## Working-block sync (server side, on every ping)
Updated behavior: online filling now uses complete 15-minute slots. Automatic Working
blocks with gaps of at most 30 minutes merge transitively, keeping the oldest event ID,
with no duration cap. This intentionally smooths short idle/disconnected gaps and rounds
the first activity back to the containing quarter-hour. Manual events are not merged.
Offline eating now touches only manually scheduled Working blocks; credited automatic
slots are retained. The original cursor behavior below still applies to offline eating.

Per user cursor `C` = end of the time range already decided.
* If no ping within 150 s before this one (client wasn't running) → `C = now` (never touch the gap).
* Decide range `[min(C, now), now + 60s]`:
  * online → fill gaps between existing Working blocks: extend a block that ends at the gap start,
    else pull a block that starts at the gap end earlier, else insert an auto Working block.
  * offline → "eat" Working blocks in the range (trim / split / delete).
* `C = now + 60s`. Past ranges are never revisited, so blocks before the client started are untouched;
  future planned blocks are only eaten progressively as time reaches them.
* Unavailable blocks are never touched by sync.
* Operator switches (env, default `true`; accepts true/false/1/0/yes/no/on/off):
  * `SYNC_FILL_ONLINE` — when `false`, online pings never create/extend Working blocks.
  * `SYNC_EAT_OFFLINE` — when `false`, offline pings never trim/delete Working blocks.
  * Live status (avatar), device tracking and messages work regardless. The cursor still advances
    when a switch is off, so turning it back on never retroactively changes past time.

## Messages
Sanitized plain text (≤ 500 chars, control chars stripped), optional name, rate limited per IP.
Delivered on the next ping as a native `MessageBoxW` (topmost) — not a toast.
Delivery: to *active* PCs only; if no PC is active, to any running PC. Expires after 1 h.

## Windows client
* `install.cmd` (server URL baked in): asks username + token → downloads exe to `%LOCALAPPDATA%\LiveCalendar`
  → `livecal.exe setup …` validates creds, writes config, adds `HKCU\…\Run` → starts it silently.
* `uninstall.cmd`: kill process, remove Run value, delete folder.
* Self-uninstall: server failing continuously ≥ 14 days (only counted while the PC has network,
  and only after ≥ 30 consecutive failures in the current run so a boot after a long sleep can't trigger it).
* Sends nothing besides the ping (random device id + active flag + client version).
