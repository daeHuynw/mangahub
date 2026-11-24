# Demo Q&A — member B's parts

Short answers to say out loud, each with the line to open if the teacher asks "show me".
Line numbers are for branch `docs/B9-report`.

## gRPC / catalog

**1. Where is the real gRPC call, and why is it not a function call?**
The catalog is a separate process (`cmd/catalog`, own `catalog.db`). Gin calls it through generated
stubs over the network: `internal/api/catalog_client.go:40` (A's client) → `internal/catalog/server.go`
(B's server). Stop the catalog and search returns 503; a function call could not fail that way.

**2. What happens when the catalog is stopped, and when it is frozen?**
Stopped: connection refused → `Unavailable` → 503 at once (2 ms Windows, 12 ms Linux). Frozen: the
socket is open but nothing answers, so the 2 s deadline set at `internal/api/catalog_client.go:40`
fires → `DeadlineExceeded` → 503 (2012 ms on Linux). Mapping: `internal/api/catalog_client.go:91`.

**3. How does the server choose the status code?**
`internal/catalog/server.go:112` (`toStatus`): `ErrNotFound` → NotFound, `ErrInvalidArgument` →
InvalidArgument with a readable message, anything else → Internal with only "internal error"
(line 122) so SQL and file paths do not leak. A panic is turned into Internal by
`internal/catalog/server.go:153`.

**4. Two admins release at the same time. Can the chapter count go down?**
No. `internal/catalog/repo.go:258` is one conditional `UPDATE ... WHERE total_chapters < ?`; SQLite runs
it atomically, so a smaller value written later matches no row. Tested with 20 concurrent releases.

**5. Why is the seed not imported twice after a restart?**
`internal/catalog/repo.go:133`: count and insert happen in one transaction (count at line 147); a
non-empty table means skip. The log says "already has 36 manga, seed skipped".

**6. What does Ctrl+C do to the catalog?**
`cmd/catalog/main.go:123`: `GracefulStop` stops new RPCs and waits for running ones; after 5 s it
forces `Stop` (line 130). The DB is closed last by a defer, so no RPC uses a closed DB.

## TCP progress sync (TUI side)

**7. Why does the second TUI update without refresh, and why not the first one?**
The backend pushes `progress_updated` to the user's other sessions only after the DB commit:
`internal/api/handlers_library.go:150`, skipping the `X-Session-ID` session. The TUI stores its session
id as soon as `auth_ok` arrives (`internal/tui/sync.go:45`), so its own PUT carries it.

**8. TCP is a stream. How are messages separated? (live challenge: missing `\n`)**
One JSON object per line. The client sends the auth line at `internal/tui/tcpclient/tcpclient.go:179`
and reads lines with `bufio.Scanner` (line 182). Without `\n` the server never sees a full line. Known
gap (D5): when the server's 5 s deadline fires, `bufio.Scanner` returns the leftover bytes as a last
line, so `internal/tcpsync/tcpsync.go:226` still accepts it; fix is to check `sc.Err()` too.

**9. What if the connection drops or the backend restarts?**
Reconnect with backoff 1 s → 10 s (`internal/tui/tcpclient/tcpclient.go:135`); a rejected token stops
instead of retrying (line 121). Events sent while offline are lost, so after a reconnect the TUI reloads
the library over HTTP (`internal/tui/sync.go:99`). Silence longer than the read timeout (line 221) counts
as a dead link; pings every 30 s (line 211) keep a healthy one busy.

**10. Two updates arrive out of order. Which one wins?**
The higher `version`: `internal/tui/library_state.go:42` ignores anything not newer than what is shown.
The same rule covers our own HTTP answer and TCP events.

## UDP alerts

**11. Why does an unsubscribed TUI get nothing? (live challenge)**
The server only knows addresses that sent `subscribe` with a valid JWT; `Broadcast`
(`internal/udpnotify/udpnotify.go:150`) loops over that list. The TUI subscribes from one connected
socket (`internal/tui/udpclient/udpclient.go:103`) so every heartbeat (line 190) refreshes the same
address; logout sends `unsubscribe` (line 178).

**12. What if the ack or an alert is lost?**
Ack: retry up to 3 times, 1 s apart (`internal/tui/udpclient/udpclient.go:183`), then keep trying on the
30 s heartbeat. Alert: lost, we do not claim delivery; the library shows the right count after the next
HTTP load. Windows reports ICMP "port unreachable" as a read error; the reader ignores it (line 134).

## WebSocket chat

**13. How is a message kept inside its room, and can a client fake its name?**
The hub (`internal/chat/chat.go:169`) is the only goroutine that knows rooms. A chat for a room the client
is not in gets an error (line 257). `from` is cleared on every incoming message (line 308) and set from
the JWT, which is checked before the upgrade (line 146).

**14. Remove a disconnected client. (live challenge)**
When the socket dies, the read loop ends and sends the client to `unregister` (`internal/chat/chat.go:284`);
the hub removes it, closes its channel and tells the room "left". A frozen client stops answering pings:
the pong handler (line 291) no longer extends the read deadline, so it is removed after 60 s. A slow reader
is dropped (line 195) instead of blocking the room.

**15. Why does the TUI not freeze while messages arrive?**
Network clients run in goroutines and never touch widgets. Every result goes through `post`
(`internal/tui/app.go:128`), which calls `QueueUpdateDraw` (line 134); HTTP calls use `runAsync`
(line 143). Late search results are dropped by a sequence number (`internal/tui/browser.go:128`).

## Other live challenges, quick answers

- **Reject progress above total_chapters:** backend `internal/api/handlers_library.go:128` (authoritative,
  400); the TUI also checks first in `internal/tui/library_state.go:88` so no request is sent.
- **Add a bounded gRPC timeout:** already there, `internal/api/catalog_client.go:40` (2 s, `CATALOG_TIMEOUT`).
- **Invalidate an old cache entry:** `internal/cache/cache.go:98` deletes the key after `UpdateChapters`.
- **Why the token is not in the WebSocket URL:** Gin logs the full URL; the TUI sends it in the header
  (`internal/tui/wsclient/wsclient.go:170`). After a reconnect it rejoins the room (line 222).
- **Where B touched A's code:** only `cmd/backend/main.go:88` (mounting `/ws`).
