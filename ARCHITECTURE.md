# Architecture

## Overview
Team Dev Log is a single Go binary that serves:
- API server on `:9173`
- Web UI server on `:9172`

The binary also includes:
- Admin CLI (`admin create-user`)
- SQLite persistence (no ORM)
- Daily compaction scheduler (5 PM local server time)
- Embedded static/web template assets

## High-Level Components
- `main.go`
  - process entrypoint and CLI command routing (`serve`, `admin`, `help`)
  - server startup/shutdown orchestration
  - SQLite connection setup + schema initialization
  - compaction scheduler and compaction transaction logic
  - central logging construction
- `api.go`
  - HTTP API handlers
  - auth middleware and token resolution
  - request validation and JSON response helpers
- `admin.go`
  - admin CLI subcommands
  - user creation and token generation (`PUD` + 9-char uppercase slug)
- `webui.go`
  - embedded templates and Oat assets
  - UI rendering and asset handlers
- `templates/`
  - `base.html`: shared UI layout shell
  - `index.html`: full board UI
  - `entries-view.html`: query-only UI

## Runtime Topology
- API server (`http.Server`) on `:9173`
  - endpoints under `/api/*`
  - CORS enabled for browser UI access
- UI server (`http.Server`) on `:9172`
  - `/` full UI
  - `/entries-view` read-focused UI
  - `/assets/oat.min.css` and `/assets/oat.min.js`

In production, Caddy sits in front and routes:
- `/api/*` -> `127.0.0.1:9173`
- all others -> `127.0.0.1:9172`

## Data Model (SQLite)
Schema is created on startup.

- `users`
  - `id` (PK)
  - `username` (UNIQUE)
  - `token_hash` (UNIQUE, SHA-256 of token)
  - `created_at` (RFC3339 UTC string)
- `entries`
  - `id` (PK)
  - `user_id` (nullable FK -> `users.id`)
  - `entry_type` (`normal` or `daily_compact`)
  - `content`
  - `created_at` (RFC3339 UTC string)
- `action_logs`
  - audit/event log for API/admin/system actions
- `compactions`
  - one row per day when compaction has completed

## Request Flow
### Authenticated API calls
1. UI/client sends bearer token (`Authorization` or `X-Auth-Token`).
2. Middleware hashes token with SHA-256.
3. Hash lookup in `users.token_hash` resolves user.
4. Handler executes, writes data, and appends `action_logs` entry.

### UI calls
1. Browser loads page from UI server (`:9172`).
2. JS reads token from `localStorage`.
3. JS sends requests to API server (`:9173`).

## Daily Compaction Flow
A scheduler loop ticks every 30 seconds and checks if local time is >= 17:00.

Compaction for a day runs once:
1. Acquire compaction mutex.
2. Set write lock flag (`writeLocked=true`) so create-entry returns `423`.
3. Begin DB transaction.
4. Skip if `compactions` already contains that day.
5. Read all `normal` entries for day ordered by time.
6. Merge into one `daily_compact` entry.
7. Delete original `normal` entries for that day.
8. Insert row in `compactions`.
9. Insert system action log row.
10. Commit transaction and release write lock.

## Logging Strategy
- App logger emits to stdout by default (`--log -`), optional file fan-out.
- `action_logs` table persists domain-level actions.
- Production recommendation: stdout/journald for process logs + DB action log for audit trail.

## Concurrency and Safety
- SQLite connection pool restricted to one open connection (`SetMaxOpenConns(1)`), matching SQLite write behavior.
- Compaction guarded by mutex and transactional writes.
- Server shutdown uses graceful shutdown timeout (`10s`).

## Deployment Model
- Single binary process managed by `systemd`.
- SQLite DB on local disk (for example `/var/lib/team-dev-log/devlog.db`).
- Caddy as public edge for TLS termination and reverse proxy.

## Tradeoffs
- Simple deployment and operations due to single binary + SQLite.
- Limited horizontal scaling due to local SQLite architecture.
- Works best as a single-node service with backup discipline.
