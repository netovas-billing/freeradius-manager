#!/bin/bash

set -uo pipefail

FREERADIUS_DIR="/etc/freeradius/3.0"
API_DIR_BASE="/root"
LOG_PREFIX="[$(date '+%Y-%m-%d %H:%M:%S')]"

info()    { echo "${LOG_PREFIX} [INFO]  $*"; }
success() { echo "${LOG_PREFIX} [OK]    $*"; }
warning() { echo "${LOG_PREFIX} [WARN]  $*"; }
error()   { echo "${LOG_PREFIX} [ERROR] $*"; }

found=0

for INFO_FILE in "$FREERADIUS_DIR"/.instance_*; do
    [ -f "$INFO_FILE" ] || continue

    # Baca ADMIN_USERNAME dari info file
    ADMIN_USERNAME=""
    DB_HOST="localhost"
    DB_PORT="3306"
    DB_USER=""
    DB_PASS=""
    DB_NAME=""

    while IFS='=' read -r key value; do
        [[ "$key" =~ ^[[:space:]]*# ]] && continue
        [[ -z "$key" ]] && continue
        key="${key// /}"
        case "$key" in
            ADMIN_USERNAME) ADMIN_USERNAME="$value" ;;
            DB_HOST)        DB_HOST="$value"        ;;
            DB_PORT)        DB_PORT="$value"        ;;
            DB_USER)        DB_USER="$value"        ;;
            DB_PASS)        DB_PASS="$value"        ;;
            DB_NAME)        DB_NAME="$value"        ;;
        esac
    done < "$INFO_FILE"

    [ -z "$ADMIN_USERNAME" ] && continue

    API_DIR="${API_DIR_BASE}/${ADMIN_USERNAME}-api"
    SERVICE_NAME="${ADMIN_USERNAME}-api"
    found=1

    info "--- Checking: ${ADMIN_USERNAME} (${API_DIR}) ---"

    if [ ! -d "${API_DIR}/.git" ]; then
        warning "Bukan git repo: ${API_DIR}, skip"
        continue
    fi

    # Git pull
    PULL_OUTPUT=$(git -C "$API_DIR" pull 2>&1)
    PULL_EXIT=$?

    if [ $PULL_EXIT -ne 0 ]; then
        error "Git pull gagal di ${API_DIR}:"
        echo "$PULL_OUTPUT"
        continue
    fi

    info "Git pull: ${PULL_OUTPUT}"

    # Cek apakah ada perubahan
    if echo "$PULL_OUTPUT" | grep -q "Already up to date"; then
        info "Tidak ada perubahan, skip restart"
        continue
    fi

    # Ada update — patch autoclearzombie.sh jika ada
    if [ -f "${API_DIR}/autoclearzombie.sh" ]; then
        info "Re-patch credentials autoclearzombie.sh..."
        sed -i \
            -e "s|^DB_HOST=.*|DB_HOST=\"${DB_HOST}\"|" \
            -e "s|^DB_PORT=.*|DB_PORT=\"${DB_PORT}\"|" \
            -e "s|^DB_USER=.*|DB_USER=\"${DB_USER}\"|" \
            -e "s|^DB_PASS=.*|DB_PASS=\"${DB_PASS}\"|" \
            -e "s|^DB_NAME=.*|DB_NAME=\"${DB_NAME}\"|" \
            "${API_DIR}/autoclearzombie.sh"
        chmod +x "${API_DIR}/autoclearzombie.sh"
        success "autoclearzombie.sh di-patch ulang"
    fi

    # Restart service
    if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
        info "Restarting service: ${SERVICE_NAME}..."
        if systemctl restart "$SERVICE_NAME"; then
            success "Service ${SERVICE_NAME} berhasil di-restart"
        else
            error "Gagal restart ${SERVICE_NAME}!"
            journalctl -u "$SERVICE_NAME" --no-pager -n 10
        fi
    else
        warning "Service ${SERVICE_NAME} tidak aktif, skip restart"
    fi
done

[ $found -eq 0 ] && warning "Tidak ada instance ditemukan"

info "--- Done ---"
exit 0
