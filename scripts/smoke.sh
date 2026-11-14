#!/usr/bin/env bash
# HTTP smoke test against a RUNNING backend (+ catalog for the manga part).
#   ./scripts/smoke.sh            # full run
#   ./scripts/smoke.sh auth       # auth part only (no catalog needed)
# Env: MANGAHUB_HTTP (default http://localhost:8080), ADMIN_KEY (default dev-admin-key)
set -uo pipefail

BASE="${MANGAHUB_HTTP:-http://localhost:8080}"
ADMIN_KEY="${ADMIN_KEY:-dev-admin-key}"
ONLY="${1:-all}"
USER1="smoke$RANDOM$RANDOM"
USER2="smoke$RANDOM$RANDOM"
PASS=0
FAIL=0

command -v jq >/dev/null || { echo "jq is required"; exit 2; }

# req METHOD PATH [BODY] [extra curl args...] → sets CODE and BODY
req() {
  local method=$1 path=$2 body=${3:-}
  shift 3 2>/dev/null || shift $#
  local out
  out=$(curl -sS -m 5 -w $'\n%{http_code}' -X "$method" "$BASE$path" \
        -H 'Content-Type: application/json' ${body:+-d "$body"} "$@") || { CODE=000; BODY=""; return; }
  CODE=${out##*$'\n'}
  BODY=${out%$'\n'*}
}

check() { # check NAME EXPECTED_CODE
  if [[ "$CODE" == "$2" ]]; then
    PASS=$((PASS + 1)); printf '  ok   %-45s %s\n' "$1" "$CODE"
  else
    FAIL=$((FAIL + 1)); printf '  FAIL %-45s got %s want %s  %s\n' "$1" "$CODE" "$2" "$BODY"
  fi
}

echo "== backend $BASE"
req GET /api/health;                                                   check "health" 200

echo "== F01 auth"
CRED="{\"username\":\"$USER1\",\"password\":\"secret123\"}"
req POST /api/auth/register "$CRED";                                   check "register" 201
req POST /api/auth/register "$CRED";                                   check "register duplicate" 409
req POST /api/auth/register '{"username":"x","password":"1"}';        check "register invalid" 400
req POST /api/auth/login "{\"username\":\"$USER1\",\"password\":\"wrong-pw\"}"; check "login wrong password" 401
req POST /api/auth/login "$CRED";                                      check "login" 200
TOKEN=$(jq -r .token <<<"$BODY")
AUTH=(-H "Authorization: Bearer $TOKEN")
req GET /api/me "";                                                    check "me without token" 401
req GET /api/me "" "${AUTH[@]}";                                       check "me with token" 200

if [[ "$ONLY" != "auth" ]]; then
  echo "== F02 manga (via gRPC catalog)"
  req GET "/api/manga?q=piece" "" "${AUTH[@]}";                        check "search 'piece'" 200
  echo "       found: $(jq -r '[.items[].title] | join(", ")' <<<"$BODY" 2>/dev/null)"
  req GET "/api/manga?q=zzzzzz" "" "${AUTH[@]}";                       check "search no result" 200
  [[ "$(jq '.items | length' <<<"$BODY")" == 0 ]] || { FAIL=$((FAIL+1)); echo "  FAIL empty search not []"; }
  req GET /api/manga/one-piece "" "${AUTH[@]}";                        check "detail one-piece" 200
  TOTAL=$(jq -r .total_chapters <<<"$BODY")
  req GET /api/manga/does-not-exist "" "${AUTH[@]}";                   check "detail unknown" 404

  echo "== F03 library"
  req POST /api/library '{"manga_id":"one-piece"}' "${AUTH[@]}";       check "add to library" 201
  req POST /api/library '{"manga_id":"one-piece"}' "${AUTH[@]}";       check "add duplicate" 409
  req POST /api/library '{"manga_id":"nope-nope"}' "${AUTH[@]}";       check "add unknown manga" 404
  req PUT /api/library/one-piece/progress '{"current_chapter":25,"status":"reading"}' "${AUTH[@]}"; check "progress 25" 200
  echo "       version: $(jq -r .version <<<"$BODY")"
  req PUT /api/library/one-piece/progress '{"current_chapter":99999}' "${AUTH[@]}"; check "progress 99999 (> $TOTAL)" 400
  req GET /api/library "" "${AUTH[@]}";                                check "list library" 200
  req GET /api/library "";                                             check "list library without token" 401

  echo "== isolation"
  CRED2="{\"username\":\"$USER2\",\"password\":\"secret123\"}"
  req POST /api/auth/register "$CRED2";                                check "register user 2" 201
  req POST /api/auth/login "$CRED2"
  AUTH2=(-H "Authorization: Bearer $(jq -r .token <<<"$BODY")")
  req GET /api/library "" "${AUTH2[@]}";                               check "user 2 library" 200
  [[ "$(jq '.items | length' <<<"$BODY")" == 0 ]] && echo "       user 2 sees 0 items (isolated)" || { FAIL=$((FAIL+1)); echo "  FAIL user 2 sees user 1 data"; }
  req PUT /api/library/one-piece/progress '{"current_chapter":3}' "${AUTH2[@]}"; check "user 2 edits user 1 entry" 404

  echo "== F04 admin release"
  NEW=$((TOTAL + 1))
  req POST /api/admin/manga/one-piece/release "{\"total_chapters\":$NEW}"; check "release without key" 403
  req POST /api/admin/manga/one-piece/release "{\"total_chapters\":$NEW}" -H "X-Admin-Key: $ADMIN_KEY"; check "release $NEW" 200
  echo "       notified: $(jq -r .notified <<<"$BODY")"
  req POST /api/admin/manga/one-piece/release '{"total_chapters":1}' -H "X-Admin-Key: $ADMIN_KEY"; check "release decrease" 400
fi

echo "== result: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
