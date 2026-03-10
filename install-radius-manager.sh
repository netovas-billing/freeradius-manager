#!/bin/bash
# ============================================================
# install-radius-manager.sh
# Installer otomatis untuk FreeRADIUS Multi-Instance Manager
#
# Yang dilakukan:
#   1. Install MariaDB via repo resmi + konfigurasi port 53360
#   2. Install FreeRADIUS 3.x + mysql + utils
#   3. Aktifkan module SQL di FreeRADIUS
#   4. Konfigurasi MariaDB: bind port + unix socket
#   5. Verifikasi semua service berjalan
#   6. Siapkan radius-manager.sh
#
# Target: Debian 12 / 13 (Bookworm / Trixie)
# ============================================================

set -euo pipefail

# ─────────────────────────────────────────────
# Konfigurasi — sesuaikan jika perlu
# ─────────────────────────────────────────────
MARIADB_PORT=53360
MARIADB_BIND="0.0.0.0"            # ganti ke 127.0.0.1 jika tidak butuh akses remote
MARIADB_SOCKET="/var/run/mysqld/mysqld.sock"
MARIADB_CONF="/etc/mysql/mariadb.conf.d/99-radiusmanager.cnf"

FREERADIUS_DIR="/etc/freeradius/3.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─────────────────────────────────────────────
# Warna
# ─────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'
YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BLUE='\033[0;34m'; NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()      { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()     { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }
header()  { echo -e "\n${CYAN}$*${NC}"; }
step()    { echo -e "\n${CYAN}══════════════════════════════════════${NC}"; \
            echo -e "${CYAN}  $*${NC}"; \
            echo -e "${CYAN}══════════════════════════════════════${NC}"; }

# ─────────────────────────────────────────────
# Check root
# ─────────────────────────────────────────────
check_root() {
    [[ "$EUID" -eq 0 ]] || err "Script harus dijalankan sebagai root: sudo bash $0"
    ok "Running as root"
}

# ─────────────────────────────────────────────
# Deteksi OS
# ─────────────────────────────────────────────
check_os() {
    if [[ ! -f /etc/debian_version ]]; then
        err "Hanya mendukung Debian/Ubuntu. Terdeteksi: $(uname -s)"
    fi
    local ver
    ver=$(cat /etc/debian_version)
    ok "OS: $(lsb_release -ds 2>/dev/null || echo "Debian $ver")"
}

# ─────────────────────────────────────────────
# Update apt
# ─────────────────────────────────────────────
apt_update() {
    info "Update apt..."
    apt-get update -qq
    apt-get install -y -qq curl wget gnupg2 lsb-release ca-certificates
    ok "apt updated"
}

# ─────────────────────────────────────────────
# Install MariaDB via repo resmi
# ─────────────────────────────────────────────
install_mariadb() {
    step "1/5  Install MariaDB"

    if command -v mariadb &>/dev/null; then
        ok "MariaDB sudah terinstall: $(mariadb --version | awk '{print $5}' | tr -d ',')"
        return 0
    fi

    info "Download & setup MariaDB repo..."
    curl -LsS https://downloads.mariadb.com/MariaDB/mariadb_repo_setup | bash -s --
    apt-get install -y mariadb-server mariadb-client
    ok "MariaDB terinstall"
}

# ─────────────────────────────────────────────
# Konfigurasi MariaDB: port 53360 + socket
# ─────────────────────────────────────────────
configure_mariadb() {
    step "2/5  Konfigurasi MariaDB port $MARIADB_PORT"

    info "Menulis konfigurasi: $MARIADB_CONF"
    cat > "$MARIADB_CONF" << EOF
# RadiusManager — konfigurasi MariaDB
# Port non-standar untuk menghindari konflik
[mysqld]
port                = ${MARIADB_PORT}
bind-address        = ${MARIADB_BIND}
socket              = ${MARIADB_SOCKET}

# Performa
innodb_buffer_pool_size  = 128M
max_connections          = 200
query_cache_size         = 0
query_cache_type         = 0

[client]
port   = ${MARIADB_PORT}
socket = ${MARIADB_SOCKET}

[mysql]
port   = ${MARIADB_PORT}
socket = ${MARIADB_SOCKET}
EOF

    ok "Konfigurasi ditulis: $MARIADB_CONF"

    info "Restart MariaDB..."
    systemctl restart mariadb
    sleep 2

    if systemctl is-active --quiet mariadb; then
        ok "MariaDB berjalan di port $MARIADB_PORT"
    else
        err "MariaDB gagal start! Cek: journalctl -xeu mariadb"
    fi

    # Verifikasi port
    if ss -tulnp | grep -q ":${MARIADB_PORT} "; then
        ok "Port $MARIADB_PORT LISTENING"
    else
        warn "Port $MARIADB_PORT belum terdeteksi di ss — mungkin butuh waktu"
    fi
}

# ─────────────────────────────────────────────
# Secure MariaDB (tanpa interaktif)
# ─────────────────────────────────────────────
secure_mariadb() {
    step "3/5  Secure MariaDB"

    info "Menghapus anonymous user & test database..."
    mariadb -u root << 'SQL'
-- Hapus anonymous user
DELETE FROM mysql.user WHERE User='';
-- Hapus akses root dari luar localhost
DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost', '127.0.0.1', '::1');
-- Hapus database test
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';
FLUSH PRIVILEGES;
SQL
    ok "MariaDB secured"
}

# ─────────────────────────────────────────────
# Install FreeRADIUS
# ─────────────────────────────────────────────
install_freeradius() {
    step "4/5  Install FreeRADIUS"

    if command -v freeradius &>/dev/null; then
        ok "FreeRADIUS sudah terinstall: $(freeradius -v 2>&1 | head -1)"
        return 0
    fi

    info "Install freeradius freeradius-mysql freeradius-utils..."
    apt-get install -y freeradius freeradius-mysql freeradius-utils
    ok "FreeRADIUS terinstall"
}

# ─────────────────────────────────────────────
# Konfigurasi FreeRADIUS: aktifkan sql module
# ─────────────────────────────────────────────
configure_freeradius() {
    step "5/5  Konfigurasi FreeRADIUS"

    local FR_DIR="$FREERADIUS_DIR"

    # Nonaktifkan site default (radius-manager akan buat site per-instance)
    info "Nonaktifkan default sites (inner-tunnel, default)..."
    for site in default inner-tunnel; do
        local link="$FR_DIR/sites-enabled/$site"
        if [[ -L "$link" ]]; then
            rm -f "$link"
            ok "Disabled: $site"
        else
            info "Skip (sudah tidak aktif): $site"
        fi
    done

    # Aktifkan module yang dibutuhkan
    info "Aktifkan modules: sql, mschap, eap, files, etc..."
    for mod in sql mschap eap attr_filter preprocess acct_unique detail; do
        local avail="$FR_DIR/mods-available/$mod"
        local enabled="$FR_DIR/mods-enabled/$mod"
        if [[ -f "$avail" ]] && [[ ! -L "$enabled" ]]; then
            ln -sf "$avail" "$enabled"
            ok "Enabled module: $mod"
        elif [[ -L "$enabled" ]]; then
            ok "Module sudah aktif: $mod"
        else
            warn "Module tidak ditemukan: $mod (skip)"
        fi
    done

    # Test config
    info "Test konfigurasi FreeRADIUS..."
    local TEST_OUT
    TEST_OUT=$(freeradius -XC 2>&1) || true
    if echo "$TEST_OUT" | grep -q "Configuration appears to be OK"; then
        ok "FreeRADIUS config OK"
    else
        warn "Ada warning di config FreeRADIUS:"
        echo "$TEST_OUT" | grep -iE "error|warning|failed" | head -10
    fi

    # Enable + start
    info "Enable & start freeradius..."
    systemctl enable --now freeradius
    sleep 2

    if systemctl is-active --quiet freeradius; then
        ok "FreeRADIUS berjalan"
    else
        warn "FreeRADIUS tidak jalan, cek: journalctl -xeu freeradius"
    fi
}

# ─────────────────────────────────────────────
# Siapkan radius-manager.sh
# ─────────────────────────────────────────────
prepare_script() {
    local script="$SCRIPT_DIR/radius-manager.sh"
    if [[ -f "$script" ]]; then
        chmod +x "$script"
        ok "radius-manager.sh siap digunakan"
    else
        warn "radius-manager.sh tidak ditemukan di $SCRIPT_DIR"
    fi
}

# ─────────────────────────────────────────────
# Ringkasan akhir
# ─────────────────────────────────────────────
print_summary() {
    echo ""
    echo -e "${CYAN}╔══════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║        Instalasi Selesai!                    ║${NC}"
    echo -e "${CYAN}╚══════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  MariaDB     : $(systemctl is-active mariadb)"
    echo    "  Port        : $MARIADB_PORT"
    echo    "  Socket      : $MARIADB_SOCKET"
    echo -e "  FreeRADIUS  : $(systemctl is-active freeradius)"
    echo    "  Config dir  : $FREERADIUS_DIR"
    echo ""
    echo -e "${CYAN}  Langkah berikutnya:${NC}"
    echo ""
    echo    "  Buat instance baru:"
    echo -e "    ${GREEN}./radius-manager.sh create <nama_instance>${NC}"
    echo ""
    echo    "  Lihat semua instance:"
    echo -e "    ${GREEN}./radius-manager.sh list${NC}"
    echo ""
    echo    "  Jika koneksi DB gagal, cek konfigurasi:"
    echo    "    cat $MARIADB_CONF"
    echo    "    ss -tulnp | grep $MARIADB_PORT"
    echo ""
    echo -e "${YELLOW}  PERHATIAN:${NC}"
    echo    "  MariaDB dikonfigurasi tanpa password root."
    echo    "  Untuk set password root:"
    echo    "    mariadb -u root"
    echo    "    ALTER USER 'root'@'localhost' IDENTIFIED BY 'passwordbaru';"
    echo    "    FLUSH PRIVILEGES;"
    echo ""
}

# ─────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────
main() {
    echo ""
    echo -e "${CYAN}╔══════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║   RadiusManager — Installer                  ║${NC}"
    echo -e "${CYAN}║   Target: Debian 12/13                       ║${NC}"
    echo -e "${CYAN}╚══════════════════════════════════════════════╝${NC}"
    echo ""

    check_root
    check_os
    apt_update
    install_mariadb
    configure_mariadb
    secure_mariadb
    install_freeradius
    configure_freeradius
    prepare_script
    print_summary
}

main "$@"
