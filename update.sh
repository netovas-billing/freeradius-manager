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

# Direktori template: TIGA nama harus sepakat, dan dulu tidak.
#
# Skrip ini dan radius-manager.sh memakai API_GO_TEMPLATE_DIR dengan bawaan
# /var/lib/radius-manager/..., sementara control plane Go (internal/config)
# memakai RM_API_GO_TEMPLATE_DIR dengan bawaan /var/lib/radius-manager-API/... —
# nama berkas yang BEDA SATU KATA. Akibatnya di mesin yang instance-nya dibuat
# lewat ERP (yaitu lewat RM-API), skrip ini berhenti di "Template Go tidak ada"
# dan MELEWATI SEMUA INSTANCE. Gagalnya berisik, tapi tetap berarti pembaruan
# tak pernah sampai — dan itu baru ketahuan saat operator justru sedang
# memperbaiki sesuatu.
#
# Urutan pencariannya: yang disebut pemanggil dulu, lalu setelan per-host,
# baru dua bawaan yang dikenal. Yang dipilih adalah yang PUNYA .git — karena
# itulah satu-satunya yang bisa di-pull dan dibangun.
RM_API_ENV_FILE="${RM_API_ENV_FILE:-/etc/radius-manager-api/env}"

# Ambil HANYA kunci yang dibutuhkan, dan hanya nilai sederhana. Sourcing utuh
# berkas setelan berarti menjalankan isinya sebagai skrip — terlalu banyak
# kuasa untuk mengambil satu jalur direktori.
baca_setelan_host() {
    local kunci="$1" baris
    [ -r "$RM_API_ENV_FILE" ] || return 1
    baris=$(grep -E "^[[:space:]]*${kunci}=" "$RM_API_ENV_FILE" 2>/dev/null | tail -1) || return 1
    [ -n "$baris" ] || return 1
    baris="${baris#*=}"
    baris="${baris%\"}"; baris="${baris#\"}"
    baris="${baris%\'}"; baris="${baris#\'}"
    printf '%s' "$baris"
}

pilih_template_dir() {
    local kandidat=()
    [ -n "${API_GO_TEMPLATE_DIR:-}" ] && kandidat+=("$API_GO_TEMPLATE_DIR")
    [ -n "${RM_API_GO_TEMPLATE_DIR:-}" ] && kandidat+=("$RM_API_GO_TEMPLATE_DIR")
    local dariEnv
    dariEnv=$(baca_setelan_host RM_API_GO_TEMPLATE_DIR) && [ -n "$dariEnv" ] && kandidat+=("$dariEnv")
    kandidat+=("/var/lib/radius-manager-api/freeradius-api-go-template")
    kandidat+=("/var/lib/radius-manager/freeradius-api-go-template")

    local d
    for d in "${kandidat[@]}"; do
        if [ -d "$d/.git" ]; then
            printf '%s' "$d"
            return 0
        fi
    done
    # Tak ada yang punya .git: kembalikan kandidat pertama supaya pesan
    # galatnya menyebut jalur yang MASUK AKAL bagi operator, bukan jalur
    # bawaan yang mungkin tak pernah ia pakai.
    printf '%s' "${kandidat[0]}"
    return 1
}

API_GO_TEMPLATE_DIR_DIMINTA="${API_GO_TEMPLATE_DIR:-}"
API_GO_TEMPLATE_DIR="$(pilih_template_dir)" || true
API_GO_CACHE_DIR="${API_GO_CACHE_DIR:-/var/cache/radius-manager/go-build}"
API_GO_BIN_NAME="freeradius-api"
API_GO_PKG_SUBDIR="api"

info()    { echo "${LOG_PREFIX} [INFO]  $*"; }
success() { echo "${LOG_PREFIX} [OK]    $*"; }
warning() { echo "${LOG_PREFIX} [WARN]  $*"; }
error()   { echo "${LOG_PREFIX} [ERROR] $*"; }

# git tidak boleh bertanya: dijalankan cron/otomatis, prompt = menggantung diam.
export GIT_TERMINAL_PROMPT=0
export GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh -o BatchMode=yes}"

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
        error "Template Go tidak ada di ${API_GO_TEMPLATE_DIR}"
        error "  Dicari di: \$API_GO_TEMPLATE_DIR, \$RM_API_GO_TEMPLATE_DIR, ${RM_API_ENV_FILE},"
        error "  /var/lib/radius-manager-api/freeradius-api-go-template,"
        error "  /var/lib/radius-manager/freeradius-api-go-template"
        error "  Jalankan create sekali dulu, atau sebut jalurnya:"
        error "    API_GO_TEMPLATE_DIR=<jalur> bash update.sh"
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
