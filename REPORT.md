# MangaHub — Technical Report (draft)

> Draft. Sections marked **[B]** are written by member B, **[C]** together.
> Every number here comes from `notes/test-log.md`; re-check after the real catalog is merged.

## 1. System Architecture **[B]**

_Architecture diagram with real ports: TUI → Gin :8080 (HTTP + /ws), TCP :9090, UDP :9091;
Gin → gRPC catalog :9092; backend.db (users, user_library) and catalog.db (manga)._

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

### gRPC — Gin ↔ Catalog Service **[B]** (fault numbers below are from member A's tests)

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
| WebSocket **[B]** | | | |

## 4. Challenges, Reflection & Contributions **[C]**

_Real defect (symptom → cause → fix → retest) from `notes/defects.md`; limitations; contributions;
three AI-assisted interactions from `notes/ai-log.md`, at least one verified/corrected/rejected._
