#!/usr/bin/env bash
# ============================================================
# setup.sh — Setup Python venv & jalankan RadiusManager API
# Target: Debian 12/13 (Bookworm / Trixie)
#
# Penggunaan:
#   bash setup.sh          → install deps + jalankan (dev)
#   bash setup.sh install  → install deps saja
#   bash setup.sh run      → jalankan (langsung, setelah install)
#   bash setup.sh prod     → jalankan mode production (4 workers)
#   bash setup.sh service  → buat systemd service
# ============================================================

set -euo pipefail

# ─────────────────────────────────────────────
# Konfigurasi
# ─────────────────────────────────────────────
APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VENV_DIR="$APP_DIR/.venv"
PYTHON_MIN="3.11"
PYTHON_BIN=""
ENV_FILE="$APP_DIR/.env"
PID_FILE="$APP_DIR/.api.pid"
LOG_FILE="$APP_DIR/api.log"

# ─────────────────────────────────────────────
# Warna
# ─────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'
YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'

info()    { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()      { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()     { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ─────────────────────────────────────────────
# Cari Python >= 3.11
# ─────────────────────────────────────────────
find_python() {
    for candidate in python3.13 python3.12 python3.11 python3; do
        if command -v "$candidate" &>/dev/null; then
            local ver
            ver=$("$candidate" -c "import sys; print('%d.%d' % sys.version_info[:2])")
            local major minor
            IFS='.' read -r major minor <<< "$ver"
            local min_major min_minor
            IFS='.' read -r min_major min_minor <<< "$PYTHON_MIN"
            if (( major > min_major || (major == min_major && minor >= min_minor) )); then
                PYTHON_BIN="$candidate"
                ok "Python ditemukan: $candidate ($ver)"
                return 0
            fi
        fi
    done
    err "Python >= $PYTHON_MIN tidak ditemukan. Install: sudo apt install python3.11"
}

# ─────────────────────────────────────────────
# Install Python & pip (jika belum ada)
# ─────────────────────────────────────────────
install_system_deps() {
    if [[ "$EUID" -eq 0 ]]; then
        info "Update apt & install dependensi sistem…"
        apt-get update -qq
        apt-get install -y -qq \
            python3 python3-venv python3-pip \
            python3-dev build-essential \
            libssl-dev libffi-dev \
            pkg-config \
            default-libmysqlclient-dev
        ok "Dependensi sistem terinstall"
    else
        warn "Bukan root — skip install paket sistem."
        warn "Pastikan python3-venv sudah terinstall."
    fi
}

# ─────────────────────────────────────────────
# Buat virtual environment
# ─────────────────────────────────────────────
create_venv() {
    if [[ -d "$VENV_DIR" ]]; then
        ok "venv sudah ada: $VENV_DIR"
    else
        info "Membuat virtual environment di $VENV_DIR"
        "$PYTHON_BIN" -m venv "$VENV_DIR"
        ok "venv dibuat"
    fi
}

# ─────────────────────────────────────────────
# Install Python dependencies
# ─────────────────────────────────────────────
install_python_deps() {
    info "Install/update dependencies dari requirements.txt…"
    "$VENV_DIR/bin/pip" install --upgrade pip --quiet
    "$VENV_DIR/bin/pip" install -r "$APP_DIR/requirements.txt" --quiet
    ok "Dependencies terinstall"
}

# ─────────────────────────────────────────────
# Buat .env jika belum ada
# ─────────────────────────────────────────────
create_env() {
    if [[ -f "$ENV_FILE" ]]; then
        ok ".env sudah ada, skip"
        return
    fi
    info "Membuat .env dari .env.example…"
    if [[ -f "$APP_DIR/.env.example" ]]; then
        cp "$APP_DIR/.env.example" "$ENV_FILE"

        # Generate API key otomatis
        local api_key
        api_key=$(openssl rand -hex 32)
        sed -i "s|^API_KEY=.*|API_KEY=${api_key}|" "$ENV_FILE"
        ok ".env dibuat dengan API_KEY baru"
        warn "Simpan API key ini: ${api_key}"
    else
        warn ".env.example tidak ditemukan, buat .env manual"
    fi
}

# ─────────────────────────────────────────────
# Jalankan server (development)
# ─────────────────────────────────────────────
run_dev() {
    info "Menjalankan server (development mode)…"
    info "Docs: http://0.0.0.0:8000/docs"
    cd "$APP_DIR"
    exec "$VENV_DIR/bin/uvicorn" api.main:app \
        --host 0.0.0.0 \
        --port 8000 \
        --reload \
        --log-level info
}

# ─────────────────────────────────────────────
# Jalankan server (production)
# ─────────────────────────────────────────────
run_prod() {
    local workers
    workers=$(nproc)
    workers=$(( workers > 4 ? 4 : workers ))
    info "Menjalankan server (production, ${workers} workers)…"
    info "Log: $LOG_FILE"
    cd "$APP_DIR"
    exec "$VENV_DIR/bin/uvicorn" api.main:app \
        --host 0.0.0.0 \
        --port 8000 \
        --workers "$workers" \
        --log-level warning \
        --access-log 2>&1 | tee -a "$LOG_FILE"
}

# ─────────────────────────────────────────────
# Buat systemd service
# ─────────────────────────────────────────────
create_service() {
    [[ "$EUID" -ne 0 ]] && err "Harus dijalankan sebagai root untuk buat service"

    local service_file="/etc/systemd/system/radiusmanager-api.service"
    local run_user="${SUDO_USER:-root}"

    cat > "$service_file" << EOF
[Unit]
Description=RadiusManager API (FastAPI)
After=network.target mariadb.service

[Service]
Type=simple
User=${run_user}
WorkingDirectory=${APP_DIR}
ExecStart=${VENV_DIR}/bin/uvicorn api.main:app \\
    --host 0.0.0.0 \\
    --port 8000 \\
    --workers 2 \\
    --log-level info
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=radiusmanager-api

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable radiusmanager-api
    ok "Service dibuat: $service_file"
    info "Untuk start: systemctl start radiusmanager-api"
    info "Untuk log:   journalctl -u radiusmanager-api -f"
}

# ─────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────
main() {
    local cmd="${1:-}"

    echo ""
    echo -e "${CYAN}╔══════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║   RadiusManager API — Setup Script   ║${NC}"
    echo -e "${CYAN}╚══════════════════════════════════════╝${NC}"
    echo ""

    case "$cmd" in
        install)
            install_system_deps
            find_python
            create_venv
            install_python_deps
            create_env
            ok "Install selesai. Jalankan: bash setup.sh run"
            ;;
        run)
            find_python
            create_venv
            create_env
            run_dev
            ;;
        prod)
            find_python
            create_venv
            create_env
            run_prod
            ;;
        service)
            find_python
            create_venv
            create_env
            create_service
            ;;
        ""|start)
            install_system_deps
            find_python
            create_venv
            install_python_deps
            create_env
            run_dev
            ;;
        *)
            echo "Penggunaan: bash setup.sh [install|run|prod|service]"
            echo ""
            echo "  (tanpa argumen)  — install + jalankan dev server"
            echo "  install          — hanya install dependensi"
            echo "  run              — jalankan dev server (--reload)"
            echo "  prod             — jalankan production server"
            echo "  service          — daftarkan sebagai systemd service (root)"
            exit 1
            ;;
    esac
}

main "$@"
