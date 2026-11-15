# MangaHub — Hợp đồng giao tiếp (contracts)

> File này là "luật chung" giữa thành viên A (backend) và B (catalog + TUI).
> Ai muốn đổi bất cứ dòng nào phải mở PR và người kia approve.
> Mọi JSON dùng tên trường `snake_case`. Thời gian dạng RFC3339 UTC, ví dụ `2026-11-20T09:01:02Z`.

## 0. Quyết định chung

| Hạng mục | Chốt |
| --- | --- |
| Go module | `mangahub` (Go 1.25+) |
| Driver SQLite | `modernc.org/sqlite` (pure Go, không cần gcc) — tên driver `"sqlite"` |
| JWT | HS256, TTL 24h, secret từ env `JWT_SECRET`; claims: `uid` (int64), `username`, `exp`, `iat`, `sub` |
| Admin | Header `X-Admin-Key` khớp env `ADMIN_KEY` |
| Phiên TCP | Server cấp `session_id`; TUI gửi kèm header `X-Session-ID` trên mọi request HTTP |
| Định dạng lỗi HTTP | Luôn `{"error": "<thông báo dễ hiểu>"}` |

## 1. Port và biến môi trường

| Thành phần | Env | Mặc định |
| --- | --- | --- |
| HTTP + WebSocket (Gin) | `HTTP_ADDR` | `:8080` |
| TCP sync | `TCP_ADDR` | `:9090` |
| UDP notify | `UDP_ADDR` | `:9091` |
| Catalog gRPC (server lắng nghe) | `CATALOG_LISTEN` | `:9092` |
| Catalog gRPC (backend kết nối tới) | `CATALOG_ADDR` | `localhost:9092` |
| DB backend | `BACKEND_DB` | `backend.db` |
| DB catalog | `CATALOG_DB` | `catalog.db` |
| Redis (tuỳ chọn) | `REDIS_ADDR` | `localhost:6379` (rỗng = tắt cache) |
| TUI → backend | `MANGAHUB_HTTP`, `MANGAHUB_TCP`, `MANGAHUB_UDP` | `http://localhost:8080`, `localhost:9090`, `localhost:9091` |

## 2. Dữ liệu dùng chung

```json
// Manga (giống message Manga trong proto/catalog.proto)
{
  "id": "one-piece",
  "title": "One Piece",
  "author": "Eiichiro Oda",
  "genres": ["action", "adventure"],
  "status": "ongoing",            // "ongoing" | "completed"
  "total_chapters": 1130,
  "description": "..."
}

// LibraryItem
{
  "manga_id": "one-piece",
  "title": "One Piece",
  "status": "reading",            // "plan-to-read" | "reading" | "completed"
  "current_chapter": 25,
  "total_chapters": 1130,
  "updated_at": "2026-11-20T09:01:02Z",
  "version": 3
}
```

## 3. HTTP API (Gin, `:8080`)

Header cho route cần đăng nhập: `Authorization: Bearer <JWT>`.

| # | Method + Path | Auth | Body gửi | Thành công | Lỗi |
| --- | --- | --- | --- | --- | --- |
| 1 | `GET /api/health` | — | — | 200 `{"status":"ok"}` | — |
| 2 | `POST /api/auth/register` | — | `{"username","password"}` (username 3–32 ký tự chữ/số; password 6–72) | 201 `{"id","username"}` | 400, 409 |
| 3 | `POST /api/auth/login` | — | `{"username","password"}` | 200 `{"token","user":{"id","username"}}` | 400, 401 |
| 4 | `GET /api/manga?q=<text>` | JWT | — | 200 `{"items":[Manga...]}` (rỗng → `[]`) | 401, 503 |
| 5 | `GET /api/manga/:id` | JWT | — | 200 `Manga` (+ header `X-Cache: HIT/MISS` nếu có Redis) | 401, 404, 503 |
| 6 | `GET /api/library` | JWT | — | 200 `{"items":[LibraryItem...]}` | 401, 503 |
| 7 | `POST /api/library` | JWT | `{"manga_id","status"?}` (mặc định `plan-to-read`) | 201 `LibraryItem` | 400, 401, 404, 409, 503 |
| 8 | `PUT /api/library/:mangaId/progress` | JWT | `{"current_chapter": int, "status"?}` | 200 `{"manga_id","current_chapter","status","version"}` | 400, 401, 404, 503 |
| 9 | `POST /api/admin/manga/:id/release` | `X-Admin-Key` | `{"total_chapters": int}` | 200 `{"manga_id","total_chapters","notified": n}` | 400, 403, 404, 503 |
| 10 | `GET /ws?token=<JWT>` | JWT (query hoặc header) | — | 101 Switching Protocols | 401 |

Quy tắc:
- `user_id` **luôn** lấy từ JWT, không bao giờ từ body/query.
- (8) kiểm `0 ≤ current_chapter ≤ total_chapters` (lấy qua gRPC `GetManga`). Chỉ **sau khi** DB ghi thành công mới push TCP.
- (9) chỉ broadcast UDP khi `UpdateChapters` thành công.

## 4. TCP sync (`:9090`) — mỗi thông điệp là 1 dòng JSON kết thúc bằng `\n`

| Chiều | Thông điệp | Ghi chú |
| --- | --- | --- |
| C → S | `{"type":"auth","token":"<JWT>"}` | Bắt buộc là dòng đầu tiên, trong 5 giây |
| S → C | `{"type":"auth_ok","session_id":"s7"}` | |
| S → C | `{"type":"auth_error","error":"invalid token"}` | Server đóng kết nối ngay sau đó |
| S → C | `{"type":"progress_updated","manga_id":"one-piece","current_chapter":25,"status":"reading","version":3}` | Chỉ gửi tới **các phiên khác** của **cùng user** |
| C → S | `{"type":"ping"}` | Tuỳ chọn, mỗi 30s |
| S → C | `{"type":"pong"}` | |

TUI bỏ qua `progress_updated` nếu `version` ≤ version đang hiển thị.

## 5. UDP notify (`:9091`) — mỗi datagram là 1 JSON, < 1200 byte

| Chiều | Thông điệp | Ghi chú |
| --- | --- | --- |
| C → S | `{"type":"subscribe","token":"<JWT>"}` | Gửi lại mỗi 30s (heartbeat); server xoá nếu im lặng > 90s |
| S → C | `{"type":"subscribed"}` | Ack, có thể mất → client thử lại tối đa 3 lần, chờ 1s mỗi lần |
| C → S | `{"type":"unsubscribe","token":"<JWT>"}` | Khi đăng xuất / thoát |
| S → C | `{"type":"new_chapter","manga_id":"one-piece","title":"One Piece","total_chapters":1131}` | Best-effort, có thể mất |
| S → C | `{"type":"error","error":"invalid token"}` | |

## 6. WebSocket chat (`GET /ws`)

| Chiều | Thông điệp | Ghi chú |
| --- | --- | --- |
| C → S | `{"type":"join","room":"one-piece"}` | Vào phòng khác (tự rời phòng cũ). Mặc định khi kết nối: `general` |
| C → S | `{"type":"chat","room":"general","text":"..."}` | `text` 1–500 ký tự; server bỏ qua `from` nếu client gửi |
| S → C | `{"type":"chat","room":"general","from":"alice","text":"...","at":"<RFC3339>"}` | |
| S → C | `{"type":"system","room":"general","text":"bob joined"}` | joined / left |
| S → C | `{"type":"error","text":"you are not in room general"}` | Gửi vào phòng không phải phòng hiện tại |

Phòng có sẵn trong TUI: `general`, cộng thêm phòng theo id manga (vd `one-piece`).

## 7. gRPC (Catalog, `:9092`) — xem `proto/catalog.proto`

| RPC | Lỗi trả về | Backend đổi sang HTTP |
| --- | --- | --- |
| `SearchManga(query, limit)` | `Internal` | 500 |
| `GetManga(id)` | `InvalidArgument` (id rỗng), `NotFound` | 400, 404 |
| `UpdateChapters(id, total_chapters)` | `NotFound`, `InvalidArgument` (nhỏ hơn số hiện tại) | 404, 400 |
| (mọi RPC) | `Unavailable` (catalog tắt), `DeadlineExceeded` (quá 2s) | 503 |

Backend luôn gọi với `context.WithTimeout(..., 2*time.Second)`.

## 8. Tài khoản demo

`alice`, `bob`, `charlie` — mật khẩu `secret123` (tạo bằng `scripts/demo.sh`, không commit DB).
