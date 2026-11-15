#!/usr/bin/env bash
# Fresh demo environment:
#   1. delete old databases
#   2. build and start the catalog (gRPC) and backend in the background
#   3. create demo accounts alice / bob / charlie (password secret123)
#   4. print the commands for the TUI windows and the chapter release
# Stop everything with Ctrl+C.
set -euo pipefail
cd "$(dirname "$0")/.."

export HTTP_ADDR="${HTTP_ADDR:-:8080}"
export ADMIN_KEY="${ADMIN_KEY:-dev-admin-key}"
export JWT_SECRET="${JWT_SECRET:-demo-secret-$(date +%s)}"
BASE="${MANGAHUB_HTTP:-http://localhost:8080}"
mkdir -p bin

echo "== 1. clean databases"
rm -f backend.db* catalog.db*

echo "== 2. build + start"
go build -o bin/catalog ./cmd/catalog
go build -o bin/backend ./cmd/backend
./bin/catalog > bin/catalog.log 2>&1 &
CAT_PID=$!
./bin/backend > bin/backend.log 2>&1 &
BE_PID=$!
trap 'echo; echo "stopping"; kill $BE_PID $CAT_PID 2>/dev/null; wait 2>/dev/null' EXIT INT TERM

for _ in $(seq 1 50); do
  curl -s -m 1 "$BASE/api/health" >/dev/null && break
  sleep 0.2
done
curl -s -m 1 "$BASE/api/health" >/dev/null || { echo "backend did not start, see bin/backend.log"; exit 1; }
echo "   catalog pid $CAT_PID (log bin/catalog.log), backend pid $BE_PID (log bin/backend.log)"

echo "== 3. demo accounts"
for u in alice bob charlie; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/auth/register" \
    -H 'Content-Type: application/json' -d "{\"username\":\"$u\",\"password\":\"secret123\"}")
  echo "   $u / secret123 → $code"
done

cat <<EOF

== 4. ready. In other terminals:
   go run ./cmd/tui          # TUI #1 (alice)
   go run ./cmd/tui          # TUI #2 (alice again → TCP sync)
   go run ./cmd/tui          # TUI #3 (bob / charlie → isolation)

   Chapter release (UDP F04):
   curl -X POST $BASE/api/admin/manga/one-piece/release \\
        -H 'X-Admin-Key: $ADMIN_KEY' -H 'Content-Type: application/json' \\
        -d '{"total_chapters": 1131}'

   gRPC failure demo: kill -STOP $CAT_PID   (frozen → 503 after 2s)
                      kill -CONT $CAT_PID   (resume)
                      kill $CAT_PID         (stopped → 503 immediately)

Press Ctrl+C to stop catalog + backend.
EOF
wait
