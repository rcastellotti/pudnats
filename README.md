# Team Dev Log (Go + SQLite, no ORM)

Single-binary Go application for team development logs.

## Workflow Change
This changes the order from a standup-first process.

New flow:
- During the day, each person posts short updates to the board.
- Later, the team reviews the board entries together.

This keeps updates lightweight in real time and makes review asynchronous-first.

## Features
- API server on `:9173`
- Web UI server on `:9172`
- Query-only web view on `:9172/entries-view`
- SQLite via `database/sql` + `github.com/mattn/go-sqlite3` (no ORM)
- Token auth with SHA-256 token hashes stored in DB
- Admin CLI for user creation + token generation
- Daily compaction at 5:00 PM local time with temporary write lock
- Action logging to SQLite and stdout/file
- Oat-based UI (`oat.min.css` / `oat.min.js`) served locally from the binary

## Project Layout
- `main.go`: process startup, server wiring, DB schema, compaction loop
- `api.go`: API handlers/auth/middleware
- `admin.go`: admin CLI commands and token generation
- `webui.go`: embedded UI assets and UI handlers
- `templates/base.html`: shared base layout template
- `templates/index.html`: main UI template for `/`
- `templates/entries-view.html`: query-only template for `/entries-view`
- `oat.min.css`, `oat.min.js`: locally served Oat assets
- `api_test.go`: API tests
- `mise.toml`: tool + task config

## Requirements
- Go 1.22+
- SQLite C toolchain support (CGO) for `go-sqlite3`

If using mise:
```bash
mise install
```

## Build / Lint / Test
Via mise tasks:
```bash
mise run build
mise run lint
mise run test
```

Or directly:
```bash
go build .
go vet ./...
go test ./...
```

## Run
Start servers:
```bash
./team-dev-log --db ./devlog.db --log ./devlog.log
```

You can also run explicitly with subcommand:
```bash
./team-dev-log serve --db ./devlog.db --log ./devlog.log
```

## Admin CLI
Top-level help:
```bash
./team-dev-log help
```

Admin help:
```bash
./team-dev-log admin --help
./team-dev-log admin create-user --help
```

Create user/token:
```bash
./team-dev-log admin create-user --username alice --db ./devlog.db --log ./devlog.log
```

Token format:
- `PUD` + 9 uppercase slug chars (12 chars total)
- raw token is shown once; DB stores only SHA-256 hash

## Web UI
- Main UI: `http://localhost:9172/`
- Query-only view: `http://localhost:9172/entries-view`
  - Optional query params: `day=YYYY-MM-DD`, `token=PUDXXXXXXXXX`

The main UI stores token in browser `localStorage` under `devlog_token`.

## API
Full curl-first API usage.

### Setup shell variables
```bash
export API="http://127.0.0.1:9173"
export TOKEN="PUDXXXXXXXXX"
export TODAY="$(date +%F)"
```

### Auth header options
Use either:

```bash
-H "Authorization: Bearer $TOKEN"
```

or:

```bash
-H "X-Auth-Token: $TOKEN"
```

### Health check
```bash
curl -i "$API/api/health"
```
Expected: `200` and `{"status":"ok"}`

### Current authenticated user
```bash
curl -i \
  -H "Authorization: Bearer $TOKEN" \
  "$API/api/me"
```
Expected: `200` and:
```json
{"id":1,"username":"alice"}
```

Unauthorized example:
```bash
curl -i "$API/api/me"
```
Expected: `401` and `{"error":"unauthorized"}`

### Create entry
```bash
curl -i -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"implemented API docs and tests"}' \
  "$API/api/entries"
```
Expected: `201` and:
```json
{"id":123,"status":"created"}
```

Create entry using `X-Auth-Token`:
```bash
curl -i -X POST \
  -H "X-Auth-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"using alternate auth header"}' \
  "$API/api/entries"
```

Create entry error cases:

Invalid JSON:
```bash
curl -i -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":' \
  "$API/api/entries"
```
Expected: `400` `{"error":"invalid json"}`

Missing content:
```bash
curl -i -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"   "}' \
  "$API/api/entries"
```
Expected: `400` `{"error":"content is required"}`

Compaction write lock window:
```bash
curl -i -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"may fail if compaction lock is active"}' \
  "$API/api/entries"
```
Possible during lock: `423` `{"error":"writes are temporarily locked for daily compaction"}`

### List entries (default day, default limit)
```bash
curl -i \
  -H "Authorization: Bearer $TOKEN" \
  "$API/api/entries"
```

### List entries for specific day and limit
```bash
curl -i \
  -H "Authorization: Bearer $TOKEN" \
  "$API/api/entries?day=$TODAY&limit=100"
```

Expected `200` body shape:
```json
{
  "day":"2026-02-17",
  "entries":[
    {
      "id":123,
      "user":"alice",
      "entry_type":"normal",
      "content":"implemented API docs and tests",
      "created_at":"2026-02-17T20:43:12Z"
    }
  ]
}
```

List entries error cases:

Invalid day format:
```bash
curl -i \
  -H "Authorization: Bearer $TOKEN" \
  "$API/api/entries?day=17-02-2026"
```
Expected: `400` `{"error":"day must be YYYY-MM-DD"}`

No/invalid token:
```bash
curl -i "$API/api/entries"
```
Expected: `401` `{"error":"unauthorized"}`

### CORS preflight
```bash
curl -i -X OPTIONS \
  -H "Origin: http://localhost:9172" \
  -H "Access-Control-Request-Method: POST" \
  -H "Access-Control-Request-Headers: Content-Type, Authorization" \
  "$API/api/entries"
```
Expected:
- `204 No Content`
- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Headers: Content-Type, Authorization, X-Auth-Token`
- `Access-Control-Allow-Methods: GET, POST, OPTIONS`

### Endpoint summary
- `GET /api/health` (no auth)
- `GET /api/me` (auth required)
- `POST /api/entries` (auth required)
- `GET /api/entries?day=YYYY-MM-DD&limit=1..1000` (auth required)

## Daily 5 PM Compaction
At local `17:00` (or first scheduler tick after 17:00):
1. New writes are temporarily locked (`POST /api/entries` returns `423 Locked`).
2. Day's `normal` entries are merged into one `daily_compact` entry.
3. Original day `normal` entries are deleted.
4. Run is recorded in `compactions` (once per day).

## Logging
Each action is persisted in `action_logs` and also written to stdout + log file.

Logged actors/actions include:
- API user actions (`create_entry`, `list_entries`, `whoami`)
- Admin CLI actions (`create_user`)
- System compaction events

## Database Schema
Auto-created on startup:
- `users(id, username, token_hash, created_at)`
- `entries(id, user_id, entry_type, content, created_at)`
- `action_logs(id, actor_type, actor_username, action, metadata, created_at)`
- `compactions(day, ran_at)`

## systemd Deployment (VPS)
Assume destination binary path `/opt/team-dev-log/devlog`.

1. Create user + directory:
```bash
sudo useradd --system --home /opt/team-dev-log --shell /usr/sbin/nologin devlog || true
sudo mkdir -p /opt/team-dev-log
sudo chown -R devlog:devlog /opt/team-dev-log
```

2. Copy binary:
```bash
sudo cp ./devlog-linux-amd64 /opt/team-dev-log/devlog
sudo chown devlog:devlog /opt/team-dev-log/devlog
sudo chmod 0755 /opt/team-dev-log/devlog
```

3. Create `/etc/systemd/system/team-dev-log.service`:
```ini
[Unit]
Description=Team Dev Log
After=network.target

[Service]
Type=simple
User=devlog
Group=devlog
WorkingDirectory=/opt/team-dev-log
Environment=TZ=America/New_York
ExecStart=/opt/team-dev-log/devlog --db /opt/team-dev-log/devlog.db --log /opt/team-dev-log/devlog.log
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

4. Enable/start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now team-dev-log
sudo systemctl status team-dev-log
```

5. Create first user on host:
```bash
sudo -u devlog /opt/team-dev-log/devlog admin create-user --username alice --db /opt/team-dev-log/devlog.db --log /opt/team-dev-log/devlog.log
```

## Zig Cross-Compile (CGO SQLite)
Because `go-sqlite3` uses CGO, use Zig as C toolchain.

Linux amd64:
```bash
CC="zig cc -target x86_64-linux-musl" \
CXX="zig c++ -target x86_64-linux-musl" \
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
go build -ldflags "-s -w" -o devlog-linux-amd64 .
```

Linux arm64:
```bash
CC="zig cc -target aarch64-linux-musl" \
CXX="zig c++ -target aarch64-linux-musl" \
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
go build -ldflags "-s -w" -o devlog-linux-arm64 .
```

## Security Notes
- Raw tokens are never stored.
- Token hashes are SHA-256.
- Put API/UI behind HTTPS reverse proxy for internet exposure.
- Restrict exposed ports with firewall/security-group rules.

## TODO
- integrations with slack etcetera
- add webhook support for external systems (GitHub, GitLab, Jira)
- add role-based access control (admin/member/viewer)
- add token rotation and token revoke commands in admin CLI
- add pagination + cursor-based listing for `/api/entries`
- add search/filter endpoints (by user, keyword, entry type)
- add export endpoints (JSON/CSV/Markdown daily summary)
- add email and chat notifications for daily compaction summary
- add OpenAPI spec + generated API client examples
- add optional SSO/OIDC authentication mode
