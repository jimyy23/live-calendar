# Live Calendar

A lightweight live-status calendar. It's a single Go binary with SQLite storage and a small Windows client.

* `/`: admin page, protected by `ADMIN_PASSWORD`. Create, rotate and delete users, copy their links and download the Windows client.
* `/view/{user_uuid}?team_token=…`: read-only week view with live status and a "send a pop-up message" box.
* `/edit/{user_uuid}`: asks for the user's token. `?token=…` signs in directly.
* `/ical/{user_uuid}.ics`: subscription feed for Google, Apple or Outlook. The long uuid acts as the secret.

The ICS feed includes live, auto-created Working blocks and their updated end times on every fetch.
It advertises a one-minute refresh interval, but Google/Apple/Outlook decide when to poll;
subscriptions are not push-based and can lag by hours. Use the web UI for real-time status.

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

Copy the per-user **Install / update** command from the admin page into PowerShell or Command Prompt on each PC. The one-line command downloads the installer and passes the username and token automatically. It validates credentials, stops the existing client, overwrites its EXE, and updates its startup entry. It installs to `%LOCALAPPDATA%\LiveCalendar` (no admin rights needed). After that it runs with no window. The command contains credentials (Base64 is not encryption); keep it private and be mindful of shell history.

* Once a minute, and immediately when the state changes, it sends `{device: <random id>, active: <input in last 5 min>}`. It sends nothing else.
* Visitor messages appear as a small always-on-top message box on the PCs that are currently active.
  The box is centered horizontally and vertically in the primary screen's usable desktop area. It requests foreground activation and stays topmost; secure desktops and exclusive fullscreen applications can still prevent normal desktop windows from appearing.
* If the server fails every request for 14 days while the PC has network, the client removes itself.
* To remove it manually, run the uninstaller from the admin page.

After updating, test positioning without sending a server message (PowerShell):
```powershell
& "$env:LOCALAPPDATA\LiveCalendar\livecal.exe" test-popup
```

## Optional ASN allowlist

Set `ALLOWED_ASNS=AS13335,15169` (example only; use your own ASNs). Empty means disabled.
The app fetches each allowed ASN's IPv4+IPv6 `aggregated.json` from [ipverse/as-ip-blocks](https://github.com/ipverse/as-ip-blocks), caches it in `/data/asn-ipverse-{ASN}.json`, and refreshes daily. No API key or bulk database download is needed. The provider includes ASNs with announcements within the last 90 days; these ranges are not proof of current ownership or trustworthiness.
Unknown IPs and non-allowed networks receive HTTP 403 on **all** public routes, including ICS and client APIs.
If the download fails, the last good cache remains in use; without usable data all public traffic is denied.
Daily refresh failures are logged, including rate-limited denial diagnostics showing the connection peer and evaluated visitor IP. Persist `/data` to retain cached ranges across redeploys. Old OXL caches are not used.

Behind Easypanel/Traefik, set `TRUSTED_PROXY_CIDRS` to the **actual proxy IPs/CIDRs**. Without this, the proxy's IP—not the visitor's—is checked. Never set `0.0.0.0/0` or `::/0`, or trust a network containing arbitrary clients. The proxy must append/replace `X-Forwarded-For` correctly. Do not publish a bypass route to the app. Loopback-only `/healthz` is exempt for the Docker healthcheck.

**This is defense in depth, not a guarantee against exploitation.** Go's HTTP stack accepts/parses requests before the middleware runs. To reject connections before they reach Go (including protection from Go vulnerabilities), enforce equivalent source-network restrictions at your firewall/reverse proxy, with no direct public access to port 8933. ASN membership identifies networks, not trustworthy people. Google/Apple fetchers and PCs outside your allowed networks are also blocked.

IP address data: [ipverse/as-ip-blocks](https://github.com/ipverse/as-ip-blocks), CC0 1.0.
