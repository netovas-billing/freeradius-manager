#!/usr/bin/env bash
# Entrypoint for the radius-manager-api Docker dev image.
#
# Responsibilities:
#   1. Generate /etc/radius-manager-api/token on first boot (idempotent).
#   2. Wait for MariaDB to be reachable so the manager doesn't start in
#      read-only mode.
#   3. Build RM_API_DB_DSN from RM_API_DB_USER/PASSWORD/HOST/PORT when the
#      DSN itself is not pre-set.
#   4. For "serve", exec supervisord which will start freeradius + rm-api
#      side by side. For "init"/"version"/"help", just exec the binary
#      directly without spinning up supervisord.

set -euo pipefail

ETC_DIR="/etc/radius-manager-api"
TOKEN_FILE="${RM_API_TOKEN_FILE:-${ETC_DIR}/token}"

log() { echo "[entrypoint] $*"; }

# ---- 1. Token bootstrap ------------------------------------------------
mkdir -p "$ETC_DIR"
chmod 0700 "$ETC_DIR"

if [[ ! -s "$TOKEN_FILE" ]]; then
    log "generating fresh API token at $TOKEN_FILE"
    /usr/local/bin/radius-manager-api init > "$TOKEN_FILE"
    chmod 0600 "$TOKEN_FILE"
else
    log "reusing existing token at $TOKEN_FILE"
fi
export RM_API_TOKEN_FILE="$TOKEN_FILE"

# ---- 2. DSN composition ------------------------------------------------
# If RM_API_DB_DSN is empty, build one from the RM_API_DB_* knobs. The
# Go config also accepts an empty DSN (read-only mode), so this is purely
# a convenience for compose users.
DB_HOST="${RM_API_DB_HOST:-mariadb}"
DB_PORT="${RM_API_DB_PORT:-3306}"
DB_USER="${RM_API_DB_USER:-root}"
DB_PASS="${RM_API_DB_PASSWORD:-}"

if [[ -z "${RM_API_DB_DSN:-}" ]]; then
    # multiStatements=true is required so the canonical FreeRADIUS schema
    # (which is one big SQL file with many CREATE TABLE blocks) can be
    # applied as a single ExecContext call by the manager.
    if [[ -n "$DB_PASS" ]]; then
        export RM_API_DB_DSN="${DB_USER}:${DB_PASS}@tcp(${DB_HOST}:${DB_PORT})/?multiStatements=true"
    else
        export RM_API_DB_DSN="${DB_USER}@tcp(${DB_HOST}:${DB_PORT})/?multiStatements=true"
    fi
    log "composed RM_API_DB_DSN from RM_API_DB_* (host=${DB_HOST} port=${DB_PORT} user=${DB_USER})"
fi

# Per-instance configs (sql_<name> module + freeradius-api .env) need the
# DB host to reach the MariaDB service from inside the container, not
# "localhost". Default to whatever DB_HOST/DB_PORT resolved to above so
# operators rarely need to set RM_API_INSTANCE_DB_HOST explicitly.
export RM_API_INSTANCE_DB_HOST="${RM_API_INSTANCE_DB_HOST:-$DB_HOST}"
export RM_API_INSTANCE_DB_PORT="${RM_API_INSTANCE_DB_PORT:-$DB_PORT}"

# ---- 3. Wait for MariaDB ----------------------------------------------
wait_for_mariadb() {
    local tries=60
    log "waiting for MariaDB at ${DB_HOST}:${DB_PORT} (up to ${tries}s)"
    for ((i=0; i<tries; i++)); do
        if mariadb-admin --protocol=TCP -h "$DB_HOST" -P "$DB_PORT" \
                -u "$DB_USER" ${DB_PASS:+-p"$DB_PASS"} ping >/dev/null 2>&1; then
            log "MariaDB is ready"
            return 0
        fi
        sleep 1
    done
    log "WARNING: MariaDB never answered; rm-api will start in read-only mode"
    return 0
}

# ---- 4. Dispatch -------------------------------------------------------
cmd="${1:-serve}"

case "$cmd" in
    serve)
        wait_for_mariadb
        # Hand off to supervisord which runs freeradius + rm-api in
        # parallel. supervisord becomes PID 1 so its signal handling
        # propagates SIGTERM/SIGINT to all programs at shutdown.
        log "starting supervisord (PID 1)"
        exec /usr/bin/supervisord -c /etc/supervisor/supervisord.conf
        ;;
    init|version|--version|-v|help|--help|-h)
        exec /usr/local/bin/radius-manager-api "$cmd"
        ;;
    *)
        # Anything else: pass through verbatim. Useful for debugging:
        # `docker run --entrypoint /entrypoint.sh ... bash`.
        exec "$@"
        ;;
esac
