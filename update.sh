#!/bin/bash

set -uo pipefail

FREERADIUS_DIR="/etc/freeradius/3.0"
API_DIR_BASE="/root"
LOG_PREFIX="[$(date '+%Y-%m-%d %H:%M:%S')]"

S3_REMOTE="ljns3"
S3_BUCKET="backup-db"
S3_BACKUP_ROOT="radiusdb"

AUTOCLEARZOMBIE_SCHEDULE="*/5 * * * *"

info()    { echo "${LOG_PREFIX} [INFO]  $*"; }
success() { echo "${LOG_PREFIX} [OK]    $*"; }
warning() { echo "${LOG_PREFIX} [WARN]  $*"; }
error()   { echo "${LOG_PREFIX} [ERROR] $*"; }

found=0

for INFO_FILE in "$FREERADIUS_DIR"/.instance_*; do
    [ -f "$INFO_FILE" ] || continue

    # Baca ADMIN_USERNAME dari info file
    ADMIN_USERNAME=""
    DB_HOST=""
    DB_PORT=""
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

    # Fallback untuk instance lama: baca DB_HOST/DB_PORT dari .env
    ENV_FILE="${API_DIR}/.env"
    if [ -f "$ENV_FILE" ]; then
        [ -z "$DB_HOST" ] && DB_HOST=$(grep '^DB_HOST=' "$ENV_FILE" | cut -d= -f2)
        [ -z "$DB_PORT" ] && DB_PORT=$(grep '^DB_PORT=' "$ENV_FILE" | cut -d= -f2)
    fi
    DB_HOST="${DB_HOST:-localhost}"
    DB_PORT="${DB_PORT:-3306}"

    info "--- Checking: ${ADMIN_USERNAME} (${API_DIR}) ---"

    if [ ! -d "${API_DIR}/.git" ]; then
        warning "Bukan git repo: ${API_DIR}, skip"
        continue
    fi

    # Stash local changes (patched credentials) supaya pull tidak konflik.
    # Catat hash stash supaya bisa dibedakan stash baru vs. stash lama.
    STASH_BEFORE=$(git -C "$API_DIR" rev-parse -q --verify refs/stash 2>/dev/null || echo "")
    git -C "$API_DIR" stash --quiet 2>/dev/null || true
    STASH_AFTER=$(git -C "$API_DIR" rev-parse -q --verify refs/stash 2>/dev/null || echo "")
    STASH_CREATED=false
    [ -n "$STASH_AFTER" ] && [ "$STASH_BEFORE" != "$STASH_AFTER" ] && STASH_CREATED=true

    # Git pull
    PULL_OUTPUT=$(git -C "$API_DIR" pull 2>&1)
    PULL_EXIT=$?

    if [ $PULL_EXIT -ne 0 ]; then
        error "Git pull gagal di ${API_DIR}:"
        echo "$PULL_OUTPUT"
        [ "$STASH_CREATED" = true ] && git -C "$API_DIR" stash pop --quiet 2>/dev/null || true
        continue
    fi

    # Pull sukses — credentials akan di-patch ulang, stash tidak dibutuhkan lagi.
    [ "$STASH_CREATED" = true ] && git -C "$API_DIR" stash drop --quiet 2>/dev/null || true

    info "Git pull: ${PULL_OUTPUT}"

    # Patch credentials selalu dari .instance_* (source of truth)
    if [ -f "${API_DIR}/autoclearzombie.sh" ]; then
        info "Patch credentials autoclearzombie.sh..."
        sed -i \
            -e "s|^DB_HOST=.*|DB_HOST=\"${DB_HOST}\"|" \
            -e "s|^DB_PORT=.*|DB_PORT=\"${DB_PORT}\"|" \
            -e "s|^DB_USER=.*|DB_USER=\"${DB_USER}\"|" \
            -e "s|^DB_PASS=.*|DB_PASS=\"${DB_PASS}\"|" \
            -e "s|^DB_NAME=.*|DB_NAME=\"${DB_NAME}\"|" \
            "${API_DIR}/autoclearzombie.sh"
        chmod +x "${API_DIR}/autoclearzombie.sh"
        success "autoclearzombie.sh di-patch"

        # Sync cron schedule autoclearzombie
        CRON_MARKER="autoclearzombie-${ADMIN_USERNAME}"
        CRON_JOB="${AUTOCLEARZOMBIE_SCHEDULE} ${API_DIR}/autoclearzombie.sh >> /var/log/autoclearzombie-${ADMIN_USERNAME}.log 2>&1"
        CURRENT_CRON=$(crontab -l 2>/dev/null | grep -F "$CRON_MARKER" || true)
        if [ "$CURRENT_CRON" != "$CRON_JOB" ]; then
            ( crontab -l 2>/dev/null | grep -vF "$CRON_MARKER"; echo "$CRON_JOB" ) | crontab -
            success "Cron autoclearzombie-${ADMIN_USERNAME} di-sync: ${AUTOCLEARZOMBIE_SCHEDULE}"
        fi
    fi

    if [ -f "${API_DIR}/autobackups3.sh" ]; then
        info "Patch credentials autobackups3.sh..."
        sed -i \
            -e "s|^REMOTE=.*|REMOTE=\"${S3_REMOTE}\"|" \
            -e "s|^BUCKET=.*|BUCKET=\"${S3_BUCKET}\"|" \
            -e "s|^BACKUP_PATH=.*|BACKUP_PATH=\"${S3_BACKUP_ROOT}/${ADMIN_USERNAME}\"|" \
            -e "s|^DB_HOST=.*|DB_HOST=\"${DB_HOST}\"|" \
            -e "s|^DB_PORT=.*|DB_PORT=\"${DB_PORT}\"|" \
            -e "s|^DB_USER=.*|DB_USER=\"${DB_USER}\"|" \
            -e "s|^DB_PASS=.*|DB_PASS=\"${DB_PASS}\"|" \
            -e "s|^DB_NAME=.*|DB_NAME=\"${DB_NAME}\"|" \
            "${API_DIR}/autobackups3.sh"
        chmod +x "${API_DIR}/autobackups3.sh"
        success "autobackups3.sh di-patch"
    fi

    # Cek apakah ada perubahan kode untuk restart
    if echo "$PULL_OUTPUT" | grep -q "Already up to date"; then
        info "Tidak ada perubahan kode, skip restart"
        continue
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
