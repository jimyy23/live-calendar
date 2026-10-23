# Live Calendar

A lightweight live-status calendar. It's a single Go binary with SQLite storage and a small Windows client.

* `/`: admin page, protected by `ADMIN_PASSWORD`. Create, rotate and delete users, copy their links and download the Windows client.
* `/view/{user_uuid}?team_token=…`: read-only week view with live status and a "send a pop-up message" box.
* `/edit/{user_uuid}`: asks for the user's token. `?token=…` signs in directly.
* `/ical/{user_uuid}.ics`: subscription feed for Google, Apple or Outlook. The long uuid acts as the secret.

Design notes are in [docs/PLAN.md](docs/PLAN.md).

## Configuration (env)

| Var | Default | |
|---|---|---|
| `BASE_URL` | `http://localhost:8933/` | public URL; baked into the installer and the links |
| `ADMIN_PASSWORD` | required | |
| `TEAM_PING_TOKEN` | required | grants `/view` access |
| `TIMEZONE` | `America/Toronto` | the calendar is shown in this zone |
| `SYNC_FILL_ONLINE` | `true` | while online, create or extend "Working" blocks |
| `SYNC_EAT_OFFLINE` | `true` | while offline, trim or delete "Working" blocks (current and upcoming time only) |
| `DATA_DIR` | `./data` (`/data` in Docker) | SQLite location |

## Run

```sh
cp .env.example .env   # set real secrets
docker compose up -d --build   # → http://localhost:8933
```

**Easypanel:** create an App from this Git repo with build type **Dockerfile**. Set the env vars above, mount a volume at `/data`, and use proxy port **8933**.

**Without Docker** (Go 1.26+):
```sh
CGO_ENABLED=0 GOOS=windows go build -ldflags="-s -w -H windowsgui" -o dist/livecal.exe ./cmd/client
go build -o dist/livecal-server ./cmd/server
ADMIN_PASSWORD=… TEAM_PING_TOKEN=… CLIENT_EXE=dist/livecal.exe ./dist/livecal-server
```

## Windows client

Download the installer from the admin page and run it on each PC. It asks for a username and token, installs to `%LOCALAPPDATA%\LiveCalendar` (no admin rights needed) and adds itself to the per-user startup list. After that it runs with no window.

* Once a minute, and immediately when the state changes, it sends `{device: <random id>, active: <input in last 5 min>}`. It sends nothing else.
* Visitor messages appear as a small always-on-top message box on the PCs that are currently active.
* If the server fails every request for 14 days while the PC has network, the client removes itself.
* To remove it manually, run the uninstaller from the admin page.
