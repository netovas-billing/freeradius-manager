#!/usr/bin/env bash
# Install radius-manager-api on a RADIUS Server VM.
#
# Usage (run as root):
#   ./install.sh [--listen 10.254.252.2:9000]
#
# Steps:
#   1. Copy the binary to /usr/local/bin/.
#   2. Create /etc/radius-manager-api/ and generate a random token.
#   3. Install systemd unit (with optional --listen override).
#   4. Print the generated token (operator must paste this into ERP).
#
# Idempotent: re-running keeps the existing token (won't regenerate).

set -euo pipefail

LISTEN="127.0.0.1:9000"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --listen) LISTEN="$2"; shift 2 ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [[ "$EUID" -ne 0 ]]; then
    echo "must be run as root" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC="${SCRIPT_DIR}/../radius-manager-api"
BIN_DST="/usr/local/bin/radius-manager-api"
ETC_DIR="/etc/radius-manager-api"
TOKEN_FILE="${ETC_DIR}/token"
UNIT_SRC="${SCRIPT_DIR}/systemd/radius-manager-api.service"
UNIT_DST="/etc/systemd/system/radius-manager-api.service"

if [[ ! -f "$BIN_SRC" ]]; then
    echo "binary not found at $BIN_SRC — build first: go build -o radius-manager-api ./cmd/radius-manager-api" >&2
    exit 1
fi

install -m 0755 "$BIN_SRC" "$BIN_DST"
mkdir -p "$ETC_DIR"
chmod 700 "$ETC_DIR"

if [[ -s "$TOKEN_FILE" ]]; then
    echo "[OK]  token already exists at $TOKEN_FILE (kept)"
else
    "$BIN_DST" init > "$TOKEN_FILE"
    chmod 600 "$TOKEN_FILE"
    echo "[OK]  generated token at $TOKEN_FILE"
fi

# Customize listen address in the unit file.
sed "s|RM_API_LISTEN=127.0.0.1:9000|RM_API_LISTEN=${LISTEN}|" "$UNIT_SRC" > "$UNIT_DST"

systemctl daemon-reload
systemctl enable --now radius-manager-api

echo
echo "===== INSTALL COMPLETE ====="
echo "Listen:     ${LISTEN}"
echo "Token file: ${TOKEN_FILE}"
echo "Token:"
cat "$TOKEN_FILE"
echo
echo "Verify:  curl http://${LISTEN}/v1/server/health"
