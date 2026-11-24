# PR: member B, catalog + TUI + chat (B1-B9) → main

Branch: `docs/B9-report` → `main`. This replaces the open B3 PR (`feat/B3-tui-browser`):
the branch contains B1-B3 as well, so the old PR can be closed.

## What is in it

| Commit | Stage | Content |
| --- | --- | --- |
| `f8505a1` | B1 | catalog SQLite repo, 36 manga seed embedded with `go:embed`, `CATALOG_SEED` override |
| `69d40e1` | B2 | gRPC server (`internal/catalog/server.go`), `cmd/catalog`, graceful shutdown |
| `3d36074` | B3 | TUI: login/register, browser, details, status page; `internal/tui/client` |
| `5fc44dd` | - | proto header comment only, stubs regenerated (no field changed) |
| `ce1ba10` | B4 | My Library (F3), add with `a`, Update Chapter dialog, `+`/`-` |
| `cc1e029` | B5 | TCP sync client with reconnect, `X-Session-ID` |
| `f74c950` | B6 | UDP alerts client, Notifications (F5), Ctrl+L logout, week 2 review fixes |
| `4d347ea` | B7 | WebSocket hub `internal/chat`, mounted at `GET /ws` |
| `f8cefc3` | B8 | Discussion screen (F4), `internal/tui/wsclient` |
| `60c21f8` | B9 | REPORT: section 1, WebSocket, gRPC, test rows, draft of B's part of section 4 |

54 files, about 9k lines, most of it tests.

## A, please review these (your files)

1. `cmd/backend/main.go`: one import and one line at the spot you left for it:

   ```go
   WS: chat.New(chat.JWTVerifier(jwt)).Handler(), // GET /ws, internal/chat (member B)
   ```

   `chat.New` starts the hub goroutine; nothing else in your code changed.

2. `go.mod` / `go.sum`: only new modules, no version of an existing one changed:
   `github.com/rivo/tview v0.42.0`, `github.com/gdamore/tcell/v2 v2.13.10`,
   `github.com/gorilla/websocket v1.5.3` (+ their indirect deps).

## How to test

```bash
go build ./... && go vet ./...
go test -race ./...                          # 16 packages
go run ./cmd/catalog                         # terminal 1
go run ./cmd/backend                         # terminal 2
MANGAHUB_E2E_HTTP=http://localhost:8080 go test -run E2E -v ./internal/tui/   # F01-F05 with 3 TUIs
go run ./cmd/tui                             # by hand, see the checklist in notes/test-log.md
```

On Windows: `powershell -ExecutionPolicy Bypass -File scripts/demo.ps1` does what `demo.sh` does.

Contract tests call your real code (imported, not changed): `api.GRPCCatalog` against the real
catalog, the TUI client against `api.NewRouter`, the TCP/UDP clients against `tcpsync.Server` and
`udpnotify.Notifier`.

## Waiting on A (details in `notes/defects.md`)

- **D1 (503 on Windows, first call after backend start).** grpc-go's `dns` resolver also asks DNS
  for the TXT record `_grpc_config.localhost`; on our network that times out (~2.9 s), so the first
  catalog call misses the 2 s deadline. Measured fixes: `CATALOG_ADDR=127.0.0.1:9092` (3 ms) or
  `GRPC_ENABLE_TXT_SERVICE_CONFIG=false` (10 ms). Suggested in code:
  `grpc.WithDisableServiceConfig()` in `DialCatalog`. Needed before the demo if it runs on Windows.
- **D5 (TCP auth line without `\n` accepted after the timeout).** When the 5 s deadline fires,
  `bufio.Scanner` returns the leftover bytes as a last line, so the server authenticates a line with
  no newline. Makes `TestMissingNewlineTimesOut` flaky. Suggested: also reject when
  `sc.Err() != nil` after the first `Scan()`.
- **Contract §6.** The TUI sends the WebSocket JWT in the `Authorization` header (the contract
  already allows header or query). Worth one line in `contracts.md`: Gin's request log prints
  `?token=<JWT>` in full, so the header is the safer default.
- **REPORT, TCP paragraph.** "A client that forgets the `\n` ... is closed after 5 s (tested)" is
  only partly true until D5 is fixed. Please adjust or fix D5 first.
- **README / .env.example.** Suggested text for `CATALOG_SEED`, `MANGAHUB_LOG` and the TUI keys is
  in `notes/proposed-docs-for-A.md`.
- **REPORT section 4.** A's contributions, limitations and AI uses; then we pick the main defect
  (B suggests D3, or D5 if it gets fixed).
