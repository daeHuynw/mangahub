# MangaHub — test log (Expected vs Observed)

Environment: Linux, Go 1.27.4, run on 2026-11-14.
Backend = real `cmd/backend` binary. **Catalog note:** member B's `cmd/catalog` was not merged
yet, so the end-to-end runs below used a temporary in-memory gRPC stub that implements the same
`proto/catalog.proto` service. Re-run these rows with the real catalog before the final report.

## Automated tests

`go test -race ./...` → all packages pass (api, auth, cache, config, store, tcpsync, udpnotify).

| Package | What is covered |
| --- | --- |
| `internal/store` | duplicate user → `ErrConflict`; unknown user → `ErrNotFound`; data survives close/reopen; duplicate library row → `ErrConflict`; version 1→2→3; user isolation; 20 concurrent updates → version 21 |
| `internal/auth` | bcrypt hash ≠ password; valid token; wrong secret / tampered token rejected; expired token rejected; `RequireAuth` 401; `RequireAdmin` 403 |
| `internal/api` | register 201/409/400; login 200/401; protected routes 401 without/forged token; restart keeps users + library; search, empty search `[]`, detail, 404; catalog down → 503; library add 201/409/404/400; progress 200, version bump, TCP push with `X-Session-ID` excluded; chapter 99999 / −1 / missing / string → 400, boundaries 0 and total → 200; isolation (bob → 404 on alice's entry, body `user_id` ignored); 20 concurrent PUT → version 21; admin release 403/200/400/404/503 and no broadcast on failure |
| `internal/api` (real gRPC) | NotFound→404, InvalidArgument→400 over a real gRPC connection; stopped / frozen / never-started catalog → 503 within the deadline |
| `internal/tcpsync` | other session of same user receives, sender and other user do not; forged token → `auth_error` + close; first message must be auth; missing `\n` → closed after 5 s; disconnect removes session; ping/pong; concurrent sends |
| `internal/udpnotify` | 2 subscribed receive `new_chapter`, unsubscribed does not; forged token / bad JSON → `error`; unsubscribe; dead receiver (closed port) does not stop the server; silent subscriber expires after TTL |
| `internal/cache` | MISS → HIT → release → MISS with new chapter count; TTL 30 s; 404 not cached; Redis down → still 200 from gRPC |

## End-to-end (real processes)

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| HTTP smoke | `scripts/smoke.sh` | all checks pass | 25 passed, 0 failed |
| F01 persistence | stop backend (SIGINT), start again on same DB, log in | login 200, library intact | login OK, `naruto` chapter 12 version 2 still there |
| F03 TCP | alice sessions s2, s3 + bob s1; PUT chapter 12 with `X-Session-ID: s2` | only s3 receives `progress_updated` | s3 got `{"type":"progress_updated","manga_id":"naruto","current_chapter":12,"status":"reading","version":2}`; s2 and bob got nothing |
| F03 TCP auth | first line with forged token | `auth_error`, connection closed | `{"type":"auth_error","error":"invalid token"}` then EOF |
| F04 UDP | alice + bob subscribed via `nc -u`; release naruto → 701 | both receive `new_chapter`; response `notified: 2` | both received `{"type":"new_chapter","manga_id":"naruto","title":"Naruto","total_chapters":701}`; `notified: 2` |
| F06 gRPC | catalog up, `GET /api/manga/naruto` | 200 | 200 in 6 ms |
| F06 gRPC | `kill -STOP` catalog (frozen) | 503 bounded by 2 s deadline | 503 in 2012 ms, then 2014 ms |
| F06 gRPC | `kill -CONT` | 200 again | 200 in 11 ms |
| F06 gRPC | `kill` catalog (process gone) | 503 fast (connection refused) | 503 in 12 ms, then 9 ms |
| F06 gRPC | restart catalog | backend reconnects without restart | 200 in 11 ms |

## Measured fault timings (for REPORT section 3)

| Fault | Time to controlled error | Why |
| --- | --- | --- |
| Catalog frozen (`kill -STOP`) | ≈ 2.01 s | TCP connection still open, nothing answers → `context.WithTimeout(2s)` → `DeadlineExceeded` → 503 |
| Catalog stopped (`kill`) | ≈ 10 ms | OS refuses the connection → gRPC `Unavailable` → 503 |
| Catalog never started | ≈ 2 ms (unit test) | same as stopped |

## Re-run with the real Catalog Service (member B, B2)

Environment: Windows 11, Go 1.27.3, run on 2026-11-18. Real `cmd/catalog` (SQLite, embedded seed of
36 manga) + real `cmd/backend`, fresh databases. `jq` was not installed, so `smoke.sh` ran with a
minimal local `jq` stand-in supporting only the filters the script uses.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| HTTP smoke | `scripts/smoke.sh`, real catalog | all checks pass | 25 passed, 0 failed (search `piece` → One Piece; release 1131 OK; release 1 → 400) |
| F02 search | `GET /api/manga?q=zzzz` | 200 `{"items":[]}` | 200 `{"items":[]}` |
| F02 search | `GET /api/manga?q=sports` (genre) | 200 | 200 in 2 ms |
| F06 gRPC | catalog up, `GET /api/manga/naruto` | 200 | 200 in 2 ms |
| F06 gRPC | catalog process killed | 503 fast | 503 in 1.9 ms, then 1.3 ms |
| F06 gRPC | catalog restarted, backend not restarted | 200, data kept, no re-seed | 200 in 2.9 ms; one-piece still 1131; log "already has 36 manga, seed skipped" |
| F06 gRPC | frozen catalog (`kill -STOP`) | — | not reproducible on Windows (no SIGSTOP); covered by A's Linux run above and by `TestRPC_ClientDeadlineReachesServer` |

Automated (`go test -race ./...`, all packages pass) — `internal/catalog` and `cmd/catalog`:
seed once / all-or-nothing / ≥30 manga ≥4 genres; search, get, update incl. equal/decrease → InvalidArgument,
unknown → NotFound; DB error → Internal "internal error" (no SQL or path leaked); handler panic → Internal and
the server keeps serving; client deadline → DeadlineExceeded and the handler's context is cancelled;
GracefulStop waits for an in-flight RPC then refuses connections; member A's `api.GRPCCatalog` against the real
server (404/400/503 mapping); program restart on the same DB keeps the released chapter; missing seed file and
busy port fail at startup.

## Week 1 acceptance through the TUI (member B, B3)

Environment: Windows 11, Go 1.27.3, 2026-11-18. Real `bin/catalog.exe` + `bin/backend.exe` started in
separate terminals on fresh DBs. TUI flows driven by `TestE2E_RealBackend` / `TestE2E_CatalogDown`
(`internal/tui/e2e_test.go`: the real `tui.App` on a tcell simulation screen, keys injected like a user,
talking HTTP to the live backend). The real `tui.exe` was also started in a Windows console: the login screen
rendered correctly and Ctrl+C restored the shell prompt.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| F01 HTTP | register new user in the form, auto login | browser opens, header shows the user | OK in 1.58 s with `-race` (bcrypt) |
| F02 HTTP→gRPC | catalog listed after login | ≥ 30 manga, first details loaded | 36 manga; details with author/genres/status/chapters/description |
| F02 | search `piece` | 1 row One Piece + details | OK in 195 ms |
| F02 | search `zzzzzz` | empty list + "No manga matches" | OK |
| F02 | genre filter `sports` | only sports manga | 3 manga, all tagged sports |
| Status / help | F6 | backend reachable | "reachable (GET /api/health ...)" |
| F06 gRPC | catalog stopped (Ctrl+C), backend up, log in and search | login OK, controlled 503 message, user stays logged in | OK in 148 ms: "The catalog service is unavailable right now..." |
| F06 gRPC | catalog restarted, backend and TUI not restarted | works again, no re-seed | "already has 36 manga, seed skipped"; e2e passes again |
| Graceful shutdown | real Ctrl+C on `catalog.exe` (terminal panel sends a console Ctrl+C) | `shutting down` then `stopped`, no panic | stopped 3.4 ms after the signal (and 0.6 ms on the second run); DB reopened cleanly |
| Graceful shutdown | real Ctrl+C on `backend.exe` | clean exit | `shutting down...`, process exited |

Automated: `go test -race ./...` passes (11 packages incl. `internal/tui` and `internal/tui/client`).

## Library through the TUI (member B, B4)

Windows 11, 2026-11-18. Real catalog + backend on fresh DBs, flows driven by `TestE2E_RealBackend`.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| F03 HTTP | browser, `a` on One Piece | 201, "Added ..." | OK |
| F03 HTTP | `a` again | 409, "already in your library" | OK |
| F03 HTTP | F3 My Library | row One Piece, 0 / 1130, v1 | OK |
| F03 HTTP | `+` on the row | chapter 1, v2 | OK |
| F03 HTTP | dialog: chapter 25 | chapter 25, v3 | OK |
| F03 validation | dialog: chapter 99999 | rejected in the TUI, no request | "Chapter must be between 0 and 1130." |
| F03 persistence | second TUI session logs in | same row 25 / v3 | OK |
| F03 persistence | backend restarted, `GET /api/library` | row kept | `current_chapter 25, version 3` |
| F06 gRPC | first catalog call right after backend start | 200 | **503 after 2 s** on this machine, see `notes/defects.md` D1 |

## Progress sync over TCP through the TUI (member B, B5)

Windows 11, 2026-11-18. Real catalog + backend, three TUIs driven by `TestE2E_RealBackend`
(A and B = same user, C = another user). Backend log lines quoted as observed.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| F03 TCP | A and B log in as the same user | two sessions in the hub | `tcpsync: user 2 +s1`, `+s2 (2 session(s))`; header shows "sync on" |
| F03 TCP | C logs in | own session | `tcpsync: user 3 +s3` |
| F03 TCP | A presses `+` in My Library (PUT with `X-Session-ID: s1`) | only B gets the event, B redraws without a key press | `one-piece=26 v4 pushed to 1 session(s)`; B shows 26 / v4 and "Synced from another session" 41 ms after A's key |
| F03 isolation | same moment, C's TUI | nothing | C's status and library unchanged |
| F03 TCP | all TUIs closed | sessions removed | `-s1`, `-s2`, `-s3` |

Automated (`internal/tui/tcpclient`, against A's real `tcpsync.Server`): auth + event, other sessions
only, rejected token stops without retrying, reconnect after server restart, backend not running keeps
retrying, cancel closes the session, ping keeps an idle session alive, silent server detected after
ReadTimeout, malformed line skipped, one event split across two writes. UI (`internal/tui/sync_test.go`):
pushed update shown, own PUT carries the session id, older version ignored, reconnect reloads the library,
unknown manga reloads, expired session closes the TCP session.

## New-chapter alerts over UDP through the TUI (member B, B6)

Windows 11, 2026-11-18. Same live run as B5 (A and B = same user, C = another user), then a release
through the admin endpoint. Backend log lines quoted as observed.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| F04 UDP | A, B, C log in | each TUI subscribes from its own port | `subscribe user 2 from 127.0.0.1:52934`, `:52935`, `subscribe user 3 from :52936`; header "alerts on" |
| F04 UDP | C presses Ctrl+L (logout) | unsubscribe + TCP session closed | `unsubscribe 127.0.0.1:52936`, `tcpsync: user 3 -s3` |
| F04 UDP | `POST /api/admin/manga/one-piece/release` (1131) | 2 subscribers get `new_chapter`, shown inside the TUI | `sent to 2/2 subscriber(s)`, response `notified: 2`; A and B show "New chapter: One Piece now has 1131" 22 ms later; B's library row becomes 26 / 1131 |
| F04 UDP | same release, C (unsubscribed) | nothing | C has no alert |
| F04 UDP | A and B quit | unsubscribe before exit | `unsubscribe 127.0.0.1:52935`, `:52934` |

Automated (`internal/tui/udpclient`, against A's real `udpnotify.Notifier`): subscribe + alert, two
subscribed and one silent socket (only 2 sent), cancel sends unsubscribe, unsubscribe after the token was
cleared (regression, see below), forged token rejected, server down then up (3 tries, NoAck, heartbeat
subscribes; Windows "connection reset" read errors ignored), first two acks lost then subscribed on the
3rd try, heartbeat keeps the subscription past a short TTL, malformed datagram ignored. UI
(`internal/tui/alerts_test.go`): alert updates status bar, unread counter, library and browser counts;
F5 list and unread reset; Ctrl+L and quit both unsubscribe.

Defect found by `TestUI_LogoutUnsubscribes`: logout cleared the JWT right after cancelling the UDP client,
which then sent `unsubscribe` with an empty token; the server answered "invalid token" and kept sending
alerts to the old port until the 90 s TTL. Fix: unsubscribe reuses the token of the last subscribe.
Retest: the UI test and `TestUnsubscribeAfterTokenCleared` pass.

## Room chat over WebSocket (member B, B7: server side)

Windows 11, 2026-11-18. `internal/chat` mounted in the real backend through `api.Deps.WS`; the
scenario ran with gorilla clients (`TestE2E_LiveBackend`), the TUI screen comes in B8.

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| F05 WS | `GET /ws?token=forged` | 401, no upgrade | `401 GET /ws?token=forged` |
| F05 WS | alice, bob, charlie connect | all in `general` | `chat: ... connected (general: 1/2/3)` |
| F05 WS | charlie joins `one-piece`, alice chats in `general` | bob gets it from "alice", charlie does not | OK |
| F05 WS | charlie chats in `one-piece` | only charlie's room | charlie's next chat is his own; alice gets nothing |
| F05 WS | bob's socket closed without a close frame | bob removed, others keep chatting | `read: close 1006 (abnormal closure)`, `disconnected from general`; alice gets "bob left" and her next message works |
| F05 regression | full TUI e2e (F01-F04) with `/ws` mounted | still passes | passes |

Automated (`internal/chat`, `-race`, 3 runs): 401 before upgrade (missing / forged token, header token
accepted), Alice/Bob exchange with `from` taken from the token, room isolation, disconnect cleanup,
validation errors keep the connection (empty, 501 chars, wrong room, bad room name, unknown type, bad
JSON; 500 multi-byte characters accepted), 10 clients x 5 messages concurrently, a peer that stops
answering pings is removed after pongWait, a live peer stays, Close sends a close frame. Contract test
through A's router with a real JWT from A's login endpoint.

Note: Gin's request log prints the full URL, so `?token=<JWT>` ends up in the backend log. The TUI
client (B8) sends the token in the `Authorization` header instead.
