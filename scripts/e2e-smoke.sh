#!/usr/bin/env bash
# End-to-end smoke test against the Docker dev stack.
#
# Usage: ./scripts/e2e-smoke.sh
#   or:  make e2e
#
# Brings the stack up, exercises POST/GET/DELETE on /v1/instances/,
# verifies supervisord transitions, and tears the stack down. Exits
# non-zero on any check failure.

set -euo pipefail

COMPOSE="docker compose -f docker-compose.dev.yml"
INSTANCE="smoke_$(date +%s)"
HOST=${HOST:-127.0.0.1}
PORT=${PORT:-9000}

color() { printf "\033[%sm%s\033[0m\n" "$1" "$2"; }
ok()    { color 32 "[OK]   $*"; }
fail()  { color 31 "[FAIL] $*"; exit 1; }
info()  { color 36 "[INFO] $*"; }

cleanup() {
    info "tearing down stack..."
    $COMPOSE down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

info "bringing up stack (this may take ~30s on first run)..."
$COMPOSE up -d --build >/dev/null

info "waiting for RM-API health..."
for i in $(seq 1 60); do
    if curl -fsS "http://$HOST:$PORT/v1/server/health" >/dev/null 2>&1; then
        ok "RM-API up after ${i}s"
        break
    fi
    if [ "$i" -eq 60 ]; then
        $COMPOSE logs rm-api | tail -40
        fail "RM-API never became healthy"
    fi
    sleep 1
done

TOKEN=$($COMPOSE exec -T rm-api cat /etc/radius-manager-api/token 2>/dev/null || true)
[ -n "$TOKEN" ] || fail "could not read API token from container"
ok "got token: ${TOKEN:0:8}..."

info "POST /v1/instances/ name=$INSTANCE (this may take ~15s with bootstrap)..."
HTTP_CODE=$(curl -sS -o /tmp/create.json -w '%{http_code}' \
    -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"name\":\"$INSTANCE\"}" "http://$HOST:$PORT/v1/instances/")
if [ "$HTTP_CODE" != "201" ]; then
    cat /tmp/create.json
    fail "POST returned $HTTP_CODE, expected 201"
fi
ok "instance created (HTTP 201)"
DB_PASS=$(grep -o '"password":"[^"]*"' /tmp/create.json | head -1 | sed 's/.*":"//;s/"//')
[ -n "$DB_PASS" ] || fail "no DB password in response"
ok "DB password received (${#DB_PASS} chars)"

info "GET /v1/instances/$INSTANCE..."
HTTP_CODE=$(curl -sS -o /tmp/get.json -w '%{http_code}' \
    -H "Authorization: Bearer $TOKEN" "http://$HOST:$PORT/v1/instances/$INSTANCE")
[ "$HTTP_CODE" = "200" ] || { cat /tmp/get.json; fail "GET returned $HTTP_CODE"; }
ok "instance readable (HTTP 200)"

info "supervisorctl status (should include $INSTANCE-api)..."
if $COMPOSE exec -T rm-api supervisorctl status | grep -q "$INSTANCE-api"; then
    ok "$INSTANCE-api supervised"
else
    $COMPOSE exec -T rm-api supervisorctl status
    fail "$INSTANCE-api not in supervisorctl status"
fi

info "DELETE /v1/instances/$INSTANCE?with_db=true..."
HTTP_CODE=$(curl -sS -o /tmp/del.json -w '%{http_code}' \
    -X DELETE -H "Authorization: Bearer $TOKEN" \
    "http://$HOST:$PORT/v1/instances/$INSTANCE?with_db=true")
[ "$HTTP_CODE" = "200" ] || { cat /tmp/del.json; fail "DELETE returned $HTTP_CODE"; }
grep -q '"database_dropped":true' /tmp/del.json || fail "DELETE response missing database_dropped:true"
ok "instance deleted (HTTP 200, database dropped)"

info "verifying $INSTANCE-api removed from supervisorctl..."
if $COMPOSE exec -T rm-api supervisorctl status | grep -q "$INSTANCE-api"; then
    fail "$INSTANCE-api still listed after delete"
fi
ok "$INSTANCE-api gone from supervisorctl"

info "verifying audit log captured both mutations..."
AUDIT=$($COMPOSE exec -T rm-api cat /var/log/radius-manager-api/audit.log 2>/dev/null || true)
# POST audit lines don't carry the instance name (URL is /v1/instances/),
# so we check method+status pairs. DELETE lines do carry the instance name.
POST_OK=$(printf '%s\n' "$AUDIT" | grep -c '"method":"POST".*"status":201' || true)
DEL_OK=$(printf '%s\n' "$AUDIT" | grep -c "\"method\":\"DELETE\".*\"instance\":\"$INSTANCE\".*\"status\":200" || true)
if [ "$POST_OK" -lt 1 ] || [ "$DEL_OK" -lt 1 ]; then
    printf '%s\n' "$AUDIT" | tail -5
    fail "audit log incomplete (POST_OK=$POST_OK DEL_OK=$DEL_OK)"
fi
ok "audit log captured POST 201 and DELETE 200 for $INSTANCE"

color 32 "
=========================================================
  END-TO-END SMOKE PASSED
=========================================================
"
