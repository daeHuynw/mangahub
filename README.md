# MangaHub

Manga discovery, personal reading library, live progress sync, new-chapter alerts and room chat.
Three independent Go programs: **Catalog Service** (gRPC + SQLite), **Backend** (Gin HTTP,
TCP, UDP, WebSocket + SQLite) and **TUI** (tview).

```
TUI ──HTTP/JSON, TCP, UDP, WebSocket──► Backend (Gin) ──gRPC──► Catalog Service
                                        backend.db             catalog.db
                                        (optional Redis cache between Backend and Catalog)
```

## Prerequisites

- Go 1.25 or later (no C compiler needed: SQLite driver is `modernc.org/sqlite`)
- `curl` and `jq` for the scripts
- Optional: Redis 6+ for the F07 cache
- Only to regenerate gRPC stubs: `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` (`scripts/gen-proto.sh`).
  The generated files in `gen/catalogpb` are committed.

## Run (3 terminals + TUIs)

```bash
go run ./cmd/catalog        # 1. gRPC catalog on :9092, seeds 30 manga on first start
go run ./cmd/backend        # 2. HTTP+WS :8080, TCP :9090, UDP :9091
go run ./cmd/tui            # 3. one or more TUI windows
```

## Configuration

All settings are environment variables with defaults (see `.env.example`):

| Variable | Default | Used by |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | backend (HTTP + `/ws`) |
| `TCP_ADDR` | `:9090` | backend (progress sync) |
| `UDP_ADDR` | `:9091` | backend (chapter alerts) |
| `CATALOG_ADDR` | `localhost:9092` | backend → catalog |
| `CATALOG_LISTEN` / `CATALOG_DB` | `:9092` / `catalog.db` | catalog |
| `BACKEND_DB` | `backend.db` | backend |
| `JWT_SECRET` | dev value | backend (HS256, tokens valid 24 h) |
| `ADMIN_KEY` | `dev-admin-key` | backend (`X-Admin-Key` for releases) |
| `REDIS_ADDR` | empty = cache off | backend |
| `CATALOG_TIMEOUT` / `CACHE_TTL` | `2s` / `30s` | backend |
| `MANGAHUB_HTTP` / `_TCP` / `_UDP` | `http://localhost:8080`, `localhost:9090`, `localhost:9091` | TUI |


## Tests

```bash
go test ./...
go test -race ./...
./scripts/smoke.sh          # HTTP checks against a running backend + catalog
```

API and message formats: `notes/contracts.md`.

