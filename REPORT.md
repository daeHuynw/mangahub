# MangaHub — Technical Report (draft)

> Draft. Sections marked **[B]** are written by member B, **[C]** together.
> Every number here comes from `notes/test-log.md`. A's first runs used a stub catalog on Linux;
> B re-ran them with the real catalog on Windows (same results, see the B sections of the log).

## 1. System Architecture **[B]**

```
   TUI #1, #2, ... (cmd/tui, tview)                       one goroutine per network client;
   +------------------------------------+                 widgets change only in the tview
   | Browser | Library | Discussion     |                 event loop (QueueUpdateDraw)
   | Notifications | Status/Help        |
   +----+--------+--------+--------+----+
        |HTTP    |TCP     |UDP     |WebSocket
        |JSON    |lines   |dgrams  |JSON frames
        v        v        v        v
   +------------------------------------------+
   | Backend (cmd/backend, Go + Gin)          |      backend.db (SQLite)
   |  :8080  REST + GET /ws (internal/chat)   |----> users, user_library
   |  :9090  TCP progress sync (tcpsync)      |      (only the backend opens it)
   |  :9091  UDP chapter alerts (udpnotify)   |
   +-------------------+----------------------+
                       | gRPC (protobuf), 2 s deadline per call
                       v                          optional: Redis cache-aside
   +------------------------------------------+   in front of GetManga (F07)
   | Catalog Service (cmd/catalog, grpc-go)   |      catalog.db (SQLite)
   |  :9092  SearchManga / GetManga /         |----> manga, manga_genres
   |         UpdateChapters                   |      (only the catalog opens it)
   +------------------------------------------+
```

Three programs run on their own: `cmd/catalog`, `cmd/backend` and one or more `cmd/tui`. Ports
come from environment variables (`CATALOG_LISTEN`, `HTTP_ADDR`, `TCP_ADDR`, `UDP_ADDR`,
`MANGAHUB_HTTP/TCP/UDP`) with the defaults shown.

**Data ownership.** Each SQLite file has one owner. The backend never opens `catalog.db`; when it
needs `total_chapters` (to validate progress) or a title, it calls `GetManga` over gRPC. The
catalog never sees users. The TUI has no database and never talks to the catalog: manga data
always goes TUI → HTTP → Gin → gRPC → Catalog. The user id always comes from the verified JWT,
never from a request body.

**One login, four channels.** After `POST /api/auth/login` the TUI opens a TCP session (sends the
JWT as its first line, gets a `session_id`), subscribes to UDP alerts, and opens the WebSocket
(JWT in the `Authorization` header). The TCP `session_id` is sent back as `X-Session-ID` on every
HTTP request so the backend does not push a change back to the session that made it. Logout
(Ctrl+L) closes TCP, sends a UDP unsubscribe and closes the WebSocket.

**TUI threading.** Each network client (HTTP calls, TCP, UDP, WebSocket) runs in its own
goroutine and knows nothing about tview. Results come back through one helper that calls
`app.QueueUpdateDraw`, so every widget change happens in the event loop; `go test -race` runs the
UI tests on a tcell simulation screen to check this.

## 2. Why These Technologies?

### HTTP REST (Gin) — accounts, search, library, progress

- **Feature.** Register/login (F01), search and details (F02), library and progress (F03), admin release (F04).
- **Data flow.** TUI → `POST /api/auth/login` → bcrypt check in `backend.db` → HS256 JWT (24 h, claims `uid`, `username`).
  Every protected route passes `RequireAuth`, which verifies the token and puts `uid` in the Gin context.
  Handlers only ever use that `uid`; a `user_id` in the body is ignored, so user B cannot read or change user A's rows.
- **Why HTTP.** These are request/response actions that need a clear answer per action, and status codes
  express outcomes directly: 400 invalid input, 401 no/invalid token, 403 wrong admin key, 404 unknown manga
  or not in library, 409 duplicate username or library entry, 503 catalog unavailable. Every error body is
  `{"error": "..."}` so the TUI can show the server's message.
- **What can fail.** A frozen dependency could make a handler hang; every gRPC call therefore has a 2 s deadline
  and becomes 503. Concurrent writes to SQLite could fail with `SQLITE_BUSY`; the store uses one connection,
  `busy_timeout(5000)` and WAL, and the progress update is a single `UPDATE … version = version + 1 RETURNING`
  inside a transaction (20 concurrent PUTs → version exactly 21, tested). Limitation: no HTTPS, tokens cannot
  be revoked before expiry.

### TCP — reading-progress sync between sessions of the same user

- **Feature.** F03: two TUIs logged in as alice; a chapter change in TUI A appears in TUI B without refresh.
- **Data flow.** TUI connects to :9090 and sends `{"type":"auth","token":…}\n` as its first line within 5 s.
  The server verifies the JWT with the same `auth.Verify` as HTTP, gives a `session_id`, and stores the session in
  `Hub.users[uid][session_id]`. TUI A sends `PUT /api/library/:id/progress` with header `X-Session-ID`. The handler
  validates `0 ≤ chapter ≤ total_chapters` (total from gRPC), commits to SQLite, and **only after the commit** calls
  `Hub.SendToUser(uid, progress_updated, except=X-Session-ID)`. Each session has its own buffered channel and writer
  goroutine, so one slow client cannot block the others or the HTTP handler.
- **Why TCP.** It is a long-lived, ordered, connection-oriented channel: the server can push at any time and
  the connection itself tells the server when a session is gone (EOF → removed from the hub). Polling HTTP would
  add delay and load.
- **Why framing.** TCP is a byte stream, not a message queue: one `Write` can arrive split or merged with another.
  Every message is one JSON object ending in `\n` and the reader uses `bufio.Scanner` per line. A client that
  forgets the `\n` never completes its auth line and is closed after 5 s (tested).
- **What can fail.** Disconnect → read loop ends → session removed, channel closed. A full send buffer → event
  dropped for that session (logged) instead of blocking. Events missed while disconnected are not replayed;
  `version` lets the TUI ignore stale events, and a reconnecting TUI reloads over HTTP.

### UDP — best-effort new-chapter alerts

- **Feature.** F04: subscribed TUIs show an in-app notification when an admin releases a chapter.
- **Data flow.** TUI sends `{"type":"subscribe","token":…}` from its own UDP socket; the server verifies the
  JWT and stores the datagram's **source address** with `lastSeen`; it answers `subscribed`. The TUI repeats
  subscribe every 30 s (heartbeat); entries silent for 90 s expire. `POST /api/admin/manga/:id/release`
  (`X-Admin-Key`) calls gRPC `UpdateChapters`; **only if it succeeds** the backend sends one
  `{"type":"new_chapter",…}` datagram to each subscriber and returns `notified: n`.
- **Why UDP.** An alert is small, independent and time-sensitive; losing one is acceptable (the library still shows
  the real count over HTTP). There is no connection to keep per client and one datagram = one message, so no framing.
- **What can fail.** Datagrams can be lost, duplicated or reordered — we do not claim delivery; `notified` counts
  datagrams handed to the OS, not received ones. An unsubscribed client never gets alerts because the server only
  knows addresses that subscribed. A closed client port makes Linux report "connection refused" on the next
  read; the server logs it and continues (tested: dead receiver does not stop the server). NAT/firewalls can block
  replies; out of scope for localhost.

### WebSocket — live chat **[B]**

- **Feature.** F05: the Discussion screen (F4). Users talk in `general` or in a room per manga;
  messages appear while the user keeps typing.
- **Data flow.** The TUI opens `GET /ws` with the JWT in the `Authorization` header. The handler
  verifies the token **before** the upgrade (bad token → 401 JSON, no socket), then hands the
  connection to the hub. One hub goroutine owns all rooms (`map[room]set of clients`) and each
  client's current room, so there is no shared map and no mutex. Every connection has a read
  goroutine (decodes `{"type":"join"|"chat",...}` and passes it to the hub) and exactly one write
  goroutine fed by a 32-message channel, because gorilla/websocket allows only one writer. On
  `chat` the hub sets `from` from the token (a client-sent `from` is ignored), checks
  1-500 characters and that the client is in that room, and broadcasts to that room only. Joining
  another room sends `left` to the old room and `joined` to the new one.
- **Why WebSocket.** Chat is many-to-many and server-pushed: every message goes to everybody in a
  room, which is different from TCP sync (one user's own sessions). WebSocket starts as an HTTP
  request on the same port, so it reuses the Gin router and the JWT check, and it carries whole
  messages as frames, so no newline framing is needed. Polling HTTP for new messages would be
  slower and is not allowed by the assignment.
- **What can fail.** A TUI that dies without a close frame: its read fails (`close 1006`) and it
  is removed; the room sees "left" (tested live). A frozen peer that keeps the socket open: the
  server pings every 50 s and drops it after 60 s without a pong (tested with short timers). A
  client that reads too slowly: its buffer fills and the hub drops it instead of blocking the room.
  The TUI client reconnects with backoff and rejoins the room the user had picked; messages sent
  while it was offline are lost, there is no history. The token used to be sent as `?token=`, and
  Gin's request log printed the whole JWT; the TUI now sends it in the header.

### gRPC — Gin ↔ Catalog Service **[B]**

- **Feature.** F02 search and details, F03 (`total_chapters` to validate progress) and F04
  (`UpdateChapters` on a release) all go through the catalog's three unary RPCs in
  `proto/catalog.proto`: `SearchManga`, `GetManga`, `UpdateChapters`. Stubs are generated with
  `protoc` and committed.
- **Data flow.** The catalog is a separate process with its own `catalog.db` (36 manga, 14 genres,
  seeded from an embedded JSON only when the table is empty). The backend's client gives every call
  a 2 s `context.WithTimeout`. The deadline travels with the request (`grpc-timeout` header), so the
  server's context is cancelled too and a slow query is abandoned on both sides (tested).
  `UpdateChapters` is one conditional `UPDATE ... WHERE id = ? AND total_chapters < ?`, so two
  releases at the same time can never move the count backwards (20 concurrent releases tested).
  The server maps errors to status codes, and the backend maps those to HTTP:

  | Situation | gRPC status | HTTP |
  | --- | --- | --- |
  | empty id, or new total ≤ current | `InvalidArgument` (readable message) | 400 |
  | unknown manga id | `NotFound` | 404 |
  | database error, handler panic | `Internal` ("internal error", details only in the catalog log) | 500 |
  | catalog stopped / frozen | `Unavailable` / `DeadlineExceeded` | 503 |

- **Why gRPC.** The two backend processes are written by different members; the `.proto` file is
  the contract both compile against, so a renamed field breaks the build instead of failing at
  run time (a contract test calls A's real client against B's real server). Status codes and
  deadlines are part of the protocol, which is exactly what the "catalog is down" case needs.
  REST between the two processes is not allowed by the assignment.
- **What can fail.** Catalog stopped: the backend gets `Unavailable` at once → 503 (12 ms on
  Linux, 2 ms on Windows). Catalog frozen (`kill -STOP`): the connection is open but nothing
  answers, so the 2 s deadline fires → 503 in 2012 ms (Linux; Windows has no SIGSTOP). Catalog
  restarted: the backend reconnects by itself, no restart needed (200 in 3 ms). Found in testing
  (D1): on Windows the first call after the backend starts can take > 2 s and return 503, because
  grpc-go's `dns` resolver also asks DNS for a TXT record (`_grpc_config.localhost`) and that query
  times out on our network; with `CATALOG_ADDR=127.0.0.1:9092` the first call takes 3 ms.
  Limitation: plaintext connection (no TLS), no automatic retry.

### Redis (optional F07) — cache-aside for `GET /api/manga/:id`

- **Feature / flow.** `cache.CachedCatalog` wraps the gRPC client. Read: `GET manga:<id>` → HIT returns it
  (`X-Cache: HIT`); MISS calls gRPC `GetManga` and `SET` with TTL 30 s (`X-Cache: MISS`). Release: gRPC
  `UpdateChapters` first, then `DEL manga:<id>`, so the next read is a MISS with the new count. The catalog SQLite
  stays the source of truth; errors (404/503) are never cached.
- **What can fail.** Redis down → each Redis call is bounded by 200 ms, the request falls back to gRPC and still
  returns 200 (tested). If the `DEL` fails, a stale value can live up to the 30 s TTL.

## 3. Implementation & Verification

### Flow 1 — progress sync (F03, HTTP + TCP)

```
TUI A            Gin backend                    SQLite        Catalog (gRPC)     TUI B
  | PUT /api/library/naruto/progress {12}          |                 |               |
  | X-Session-ID: s2, Bearer JWT                  |                 |               |
  |------------->| RequireAuth → uid=1            |                 |               |
  |              |-- GetManga(naruto), 2s deadline ---------------->|               |
  |              |<------------------------- total_chapters=700 ----|               |
  |              | 0 ≤ 12 ≤ 700 ✓                 |                 |               |
  |              |-- UPDATE … version+1 --------->|                 |               |
  |              |<-- version=2, COMMIT ----------|                 |               |
  |              |-- Hub.SendToUser(1, ev, except s2) ------------------------------->|
  |              |      {"type":"progress_updated",…,"version":2}\n                  |
  |<-- 200 {version:2}                            |                 |   B redraws   |
```

### Flow 2 — chapter release (F04, HTTP → gRPC → UDP)

```
Admin (curl)     Gin backend                 Catalog (gRPC)        Subscribed TUIs
  | POST /api/admin/manga/naruto/release {701}, X-Admin-Key     |
  |------------->| RequireAdmin ✓                                |
  |              |-- UpdateChapters(naruto,701), 2s ------->|    |
  |              |<------------------- Manga{total=701} ----|    |
  |              | (Redis: DEL manga:naruto)                     |
  |              |-- UDP {"type":"new_chapter",…} -------------->| alice, bob
  |<-- 200 {"notified":2}                                        |
```

### Test table (from `notes/test-log.md`)

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
| HTTP auth | wrong password / duplicate username | 401 / 409 | 401 / 409 |
| HTTP auth | protected route without or with forged token | 401 | 401 |
| HTTP persistence | stop + start backend, log in again | data kept | login OK, library row (chapter 12, v2) kept |
| HTTP validation | progress 99999 (> total), −1, `"ten"` | 400, no TCP push | 400, push count 0 |
| HTTP isolation | bob PUTs alice's manga | 404 | 404 |
| HTTP concurrency | 20 parallel PUTs, `-race` | version 21 | version 21, no race |
| TCP sync | alice s2 updates, s3 + bob connected | only s3 receives | only s3 received v2 event |
| TCP auth | forged token / no `\n` | `auth_error` / closed after 5 s | as expected |
| UDP | 2 subscribed, 1 not | 2 receive | 2 received, `notified: 2`; unsubscribed got nothing |
| UDP | subscriber's port closed | server keeps running | next subscriber still served |
| gRPC fault | `kill -STOP` catalog | 503 ≤ 2 s | 503 in 2012 ms |
| gRPC fault | `kill` catalog | 503 fast | 503 in 12 ms |
| gRPC recovery | restart catalog | 200, no backend restart | 200 in 11 ms |
| Redis | MISS → HIT → release → MISS | new count after release | as expected (miniredis test) |
| Redis down | Redis stopped | still 200 | 200 from gRPC |
| gRPC errors (real catalog) | unknown id / release ≤ current / empty id | 404 / 400 / 400 | as expected (contract test with A's client) |
| gRPC restart (Windows) | stop catalog, then start it, backend untouched | 503 fast, then 200 | 503 in 2 ms; 200 in 3 ms, no re-seed |
| TCP via TUI | A presses `+`, B (same user) and C (other user) open | B redraws, C nothing | B in 41 ms; backend `pushed to 1 session(s)` |
| UDP via TUI | release with A, B subscribed, C logged out | A and B show the alert | `sent to 2/2`, C got nothing |
| WebSocket auth | `/ws` with forged token | 401, no upgrade | 401 |
| WebSocket rooms | A, B in general, C in one-piece | A↔B only | C saw neither direction (TUI and gorilla clients) |
| WebSocket disconnect | client killed without close frame | removed, room keeps working | `close 1006` logged, "left" shown, chat continues |
| WebSocket dead peer | peer stops answering pings | dropped after pong timeout | dropped (short-timer test) |

## 4. Challenges, Reflection & Contributions **[C]**

_Real defect (symptom → cause → fix → retest) from `notes/defects.md`; limitations; contributions;
three AI-assisted interactions from `notes/ai-log.md`, at least one verified/corrected/rejected._

> Draft of member B's parts below. A: please add yours, then we pick one defect together.

### Real defect (candidate D3, B)

- **Symptom.** After logging out with Ctrl+L, a chapter release was still sent to that TUI's UDP
  port. Quitting with Ctrl+C was fine, so trying it by hand would likely have missed it.
- **Cause.** Logout cancels the UDP client and then clears the JWT. The client goroutine only read
  the token when it built the `unsubscribe` datagram, which was after it had been cleared; the server
  answered "invalid token" and kept the address until its 90 s timeout. A timing bug between two
  goroutines, not a protocol bug.
- **Fix.** `unsubscribe` reuses the token of the last `subscribe`.
- **Retest.** A unit test clears the token before cancelling and checks the server has 0
  subscribers; the UI logout test passes; in the live run the backend logs `unsubscribe` right
  after the logout.

Other candidates in `notes/defects.md`: D1 (first catalog call after start times out on Windows,
slow DNS TXT lookup in grpc-go) and D5 (an auth line without `\n` is accepted after the 5 s
timeout because `bufio.Scanner` returns the leftover bytes; this makes the TCP paragraph's
"closed after 5 s" only partly true until A fixes it).

### Known limitations (B side)

- Chat keeps no history: a TUI that joins or reconnects only sees new messages.
- UDP alerts are best effort; a lost datagram is not resent.
- The TUI flows are tested on a tcell simulation screen against the live backend; the real Windows
  console was checked by hand for start-up, login screen and Ctrl+C, not for every screen.
- Plaintext everywhere (HTTP, gRPC, WebSocket); fine for localhost only.

### Contributions

- **Member B:** Catalog Service (SQLite repository, embedded seed, gRPC server, `cmd/catalog`),
  the whole TUI (screens, HTTP/TCP/UDP/WebSocket clients), the WebSocket chat hub, their tests and
  the live end-to-end test, report sections 1, WebSocket and gRPC.
- **Member A:** _(to be written by A)_

### AI-assisted interactions (B)

1. **Corrected:** AI-written UI tests called `t.Fatalf` inside the tview event loop; when an
   assertion failed the event loop died and the test hung. Found with a goroutine dump, fixed by
   asserting outside the event loop and adding a timeout to the helper.
2. **Caught by our test:** the AI's first UDP client produced defect D3 above.
3. **Verification decision:** for D1 the AI first suggested a firewall prompt. We did not accept
   that without evidence; grpc-go logs and a timed DNS query showed the TXT lookup, and both fixes
   were measured before being written down.

Full list: `notes/ai-log.md`.
