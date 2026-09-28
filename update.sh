#!/bin/bash

set -uo pipefail

FREERADIUS_DIR="/etc/freeradius/3.0"
API_DIR_BASE="/root"
LOG_PREFIX="[$(date '+%Y-%m-%d %H:%M:%S')]"

S3_REMOTE="ljns3"
S3_BUCKET="backup-db"
S3_BACKUP_ROOT="radiusdb"

AUTOCLEARZOMBIE_SCHEDULE="*/5 * * * *"
AUTOBACKUPS3_SCHEDULE="@daily"

# ── Runtime Go ───────────────────────────────────────────────────────────────
# Cermin radius-manager.sh. Instance runtime Go TIDAK punya .git (yang disalin
# ke direktori instance hanya biner + skrip pemeliharaan), jadi tanpa cabang
# khusus di bawah ia akan dilewati dengan pesan "Bukan git repo" — aman, tapi
# artinya instance Go tak pernah bisa diperbarui sama sekali.
API_REPO_GO="${API_REPO_GO:-https://github.com/netovas-billing/freeradius-api}"
API_REPO_GO_REF="${API_REPO_GO_REF:-}"
API_GO_TEMPLATE_DIR="${API_GO_TEMPLATE_DIR:-/var/lib/radius-manager/freeradius-api-go-template}"
API_GO_CACHE_DIR="${API_GO_CACHE_DIR:-/var/cache/radius-manager/go-build}"
API_GO_BIN_NAME="freeradius-api"
API_GO_PKG_SUBDIR="api"

info()    { echo "${LOG_PREFIX} [INFO]  $*"; }
success() { echo "${LOG_PREFIX} [OK]    $*"; }
warning() { echo "${LOG_PREFIX} [WARN]  $*"; }
error()   { echo "${LOG_PREFIX} [ERROR] $*"; }

go_binary() {
    if [ -x /usr/local/go/bin/go ]; then echo /usr/local/go/bin/go; return 0; fi
    if command -v go >/dev/null 2>&1; then command -v go; return 0; fi
    error "toolchain Go tidak ditemukan"
    return 1
}

# Perbarui template Go sekali per jalannya skrip, bukan sekali per instance:
# semua instance Go di satu mesin memakai biner yang sama.
GO_TEMPLATE_SIAP=false
refresh_go_template() {
    [ "$GO_TEMPLATE_SIAP" = true ] && return 0

    local GO_BIN
    GO_BIN=$(go_binary) || return 1

    if [ ! -d "$API_GO_TEMPLATE_DIR/.git" ]; then
        error "Template Go tidak ada di ${API_GO_TEMPLATE_DIR} — jalankan create sekali dulu"
        return 1
    fi

    # Ref yang DIPAKU tidak boleh ditarik: menariknya membuat "versi terpasang"
    # jadi pertanyaan terbuka lagi, padahal itu justru yang dipaku.
    if [ -z "$API_REPO_GO_REF" ]; then
        info "Memperbarui template Go..."
        git -C "$API_GO_TEMPLATE_DIR" pull --quiet --ff-only || {
            warning "git pull template Go gagal — memakai checkout yang ada"
        }
    else
        info "Template Go dipaku di ${API_REPO_GO_REF}, tidak di-pull"
    fi

    mkdir -p "$API_GO_CACHE_DIR" || { error "Gagal membuat cache build"; return 1; }
    info "Membangun biner Go..."
    ( cd "${API_GO_TEMPLATE_DIR}/${API_GO_PKG_SUBDIR}" && \
      GOPROXY=off GOFLAGS=-mod=vendor GOCACHE="$API_GO_CACHE_DIR" \
      "$GO_BIN" build -mod=vendor -o "$API_GO_BIN_NAME" . ) || {
        error "go build gagal"
        return 1
    }
    GO_TEMPLATE_SIAP=true
    success "Biner Go siap"
    return 0
}

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

    # Instance runtime Go: tidak ada .git, yang ada biner + skrip pemeliharaan.
    if [ -f "${API_DIR}/${API_GO_BIN_NAME}" ] && [ ! -d "${API_DIR}/venv" ]; then
        info "Instance runtime GO terdeteksi"
        if ! refresh_go_template; then
            error "Gagal menyiapkan biner Go, skip ${ADMIN_USERNAME}"
            continue
        fi

        BIN_BARU="${API_GO_TEMPLATE_DIR}/${API_GO_PKG_SUBDIR}/${API_GO_BIN_NAME}"
        BIN_LAMA="${API_DIR}/${API_GO_BIN_NAME}"

        # Bandingkan isi, bukan waktu: `go build` menulis ulang berkasnya tiap
        # kali meski hasilnya identik, jadi mtime akan selalu berubah dan setiap
        # instance akan di-restart percuma tiap kali skrip ini jalan.
        if cmp -s "$BIN_BARU" "$BIN_LAMA"; then
            info "Biner tidak berubah, skip restart"
            continue
        fi

        info "Memasang biner baru..."
        cp "$BIN_BARU" "${BIN_LAMA}.baru" || { error "Gagal menyalin biner"; continue; }
        chmod 755 "${BIN_LAMA}.baru"
        # Ganti ATOMIK: mv pada satu filesystem tak pernah meninggalkan biner
        # separuh tertulis yang bisa dijalankan systemd saat restart.
        mv -f "${BIN_LAMA}.baru" "$BIN_LAMA" || { error "Gagal mengganti biner"; continue; }

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
        continue
    fi

    if [ ! -d "${API_DIR}/.git" ]; then
        warning "Bukan git repo dan bukan instance Go: ${API_DIR}, skip"
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

        # Sync cron schedule autobackups3
        CRON_BACKUP_MARKER="autobackups3-${ADMIN_USERNAME}"
        CRON_BACKUP_JOB="${AUTOBACKUPS3_SCHEDULE} ${API_DIR}/autobackups3.sh >> /var/log/autobackups3-${ADMIN_USERNAME}.log 2>&1"
        CURRENT_BACKUP_CRON=$(crontab -l 2>/dev/null | grep -F "$CRON_BACKUP_MARKER" || true)
        if [ "$CURRENT_BACKUP_CRON" != "$CRON_BACKUP_JOB" ]; then
            ( crontab -l 2>/dev/null | grep -vF "$CRON_BACKUP_MARKER"; echo "$CRON_BACKUP_JOB" ) | crontab -
            success "Cron autobackups3-${ADMIN_USERNAME} di-sync: ${AUTOBACKUPS3_SCHEDULE}"
        fi
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
