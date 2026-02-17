# Team Dev Log (Go + SQLite, no ORM)

Single-binary Go app with:
- API server on `:9173`
- Basic JS UI server on `:9172`
- SQLite storage via `database/sql` + `github.com/mattn/go-sqlite3`
- Token auth (only SHA-256 token hashes stored in DB)
- Admin CLI to create users and generate tokens
- Daily write lock + compaction at 5:00 PM local time into one `daily_compact` entry
- User/admin/system action logging to SQLite and stdout/file

## Files
- `main.go`: full application (single file)
- `go.mod`: module and dependency

## Local Run
1. Build:
```bash
go build -o devlog .
```

2. Create a user/token:
```bash
./devlog admin create-user --username alice --db ./devlog.db --log ./devlog.log
```

3. Start servers:
```bash
./devlog --db ./devlog.db --log ./devlog.log
```

4. Open UI:
- `http://localhost:9172`

Paste token from step 2 into UI, save it (stored in browser `localStorage`), then post/query entries.

## API
### Auth
Use one of:
- `Authorization: Bearer <token>`
- `X-Auth-Token: <token>`

### Endpoints
- `GET /api/health` (no auth)
- `GET /api/me` (auth)
- `POST /api/entries` (auth)
  - Body: `{"content":"did X, fixed Y"}`
- `GET /api/entries?day=YYYY-MM-DD&limit=200` (auth)

Example:
```bash
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9173/api/me
```

## Daily 5 PM Compaction Behavior
At local `17:00` (or first check after 17:00 if service is busy/restarting), the app:
1. Temporarily locks writes (`POST /api/entries` returns `423 Locked` while running).
2. Reads all `normal` entries for the day.
3. Merges them into one `daily_compact` entry.
4. Deletes that day’s original `normal` entries.
5. Marks compaction done in `compactions` table (runs once/day).

## Logging
Every action is logged:
- To DB table: `action_logs`
- To stdout + log file (`--log`, default `./devlog.log`)

Included events:
- user API actions (`create_entry`, `list_entries`, `whoami`)
- admin CLI (`create_user`)
- compaction/system events

## Database Schema
Created automatically on startup:
- `users(id, username, token_hash, created_at)`
- `entries(id, user_id, entry_type, content, created_at)`
- `action_logs(id, actor_type, actor_username, action, metadata, created_at)`
- `compactions(day, ran_at)`

## Deploy on VPS with systemd
Assume Linux target host and binary path `/opt/team-dev-log/devlog`.

1. Create directories/user:
```bash
sudo useradd --system --home /opt/team-dev-log --shell /usr/sbin/nologin devlog || true
sudo mkdir -p /opt/team-dev-log
sudo chown -R devlog:devlog /opt/team-dev-log
```

2. Copy binary and set perms:
```bash
sudo cp ./devlog-linux-amd64 /opt/team-dev-log/devlog
sudo chown devlog:devlog /opt/team-dev-log/devlog
sudo chmod 0755 /opt/team-dev-log/devlog
```

3. Create systemd unit `/etc/systemd/system/team-dev-log.service`:
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

5. Create first API user on host:
```bash
sudo -u devlog /opt/team-dev-log/devlog admin create-user --username alice --db /opt/team-dev-log/devlog.db --log /opt/team-dev-log/devlog.log
```

## Zig Cross-Compile (CGO SQLite)
Because `go-sqlite3` uses CGO, use Zig as C toolchain for cross-compiling.

Prereqs on build machine:
- Go (1.22+)
- Zig (0.11+)

### Linux amd64 (musl)
```bash
CC="zig cc -target x86_64-linux-musl" \
CXX="zig c++ -target x86_64-linux-musl" \
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
go build -ldflags "-s -w" -o devlog-linux-amd64 .
```

### Linux arm64 (musl)
```bash
CC="zig cc -target aarch64-linux-musl" \
CXX="zig c++ -target aarch64-linux-musl" \
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
go build -ldflags "-s -w" -o devlog-linux-arm64 .
```

## Security Notes
- Raw tokens are never stored; only SHA-256 hashes are persisted.
- Tokens are shown only once at user creation.
- Use firewall/security-group rules to expose only needed ports.
- Place app behind HTTPS reverse proxy (Nginx/Caddy) for internet exposure.
