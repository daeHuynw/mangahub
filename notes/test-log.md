# MangaHub — test log (Expected vs Observed)

Environment: Linux, Go 1.27.4, run on 2026-11-14.
Backend = real `cmd/backend` binary. **Catalog note:** member B's `cmd/catalog` was not merged
yet, so the end-to-end runs below used a temporary in-memory gRPC stub that implements the same
`proto/catalog.proto` service. Re-run these rows with the real catalog before the final report.

## gRPC fault injection (real processes)

| Feature / protocol | Condition | Expected | Observed |
| --- | --- | --- | --- |
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
