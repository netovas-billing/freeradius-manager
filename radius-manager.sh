#!/bin/bash

# ============================================
# FreeRADIUS Multi-Instance Manager v3
# - Buat instance + database otomatis
# - NAS didaftarkan manual ke tabel nas
# ============================================

set -uo pipefail
trap 'error "Script berhenti di baris $LINENO — exit code: $?"' ERR

# ============================================
# CONFIGURATION
# ============================================
FREERADIUS_DIR="/etc/freeradius/3.0"
MODS_AVAILABLE="$FREERADIUS_DIR/mods-available"
MODS_ENABLED="$FREERADIUS_DIR/mods-enabled"
SITES_AVAILABLE="$FREERADIUS_DIR/sites-available"
SITES_ENABLED="$FREERADIUS_DIR/sites-enabled"
LOG_DIR="/var/log/freeradius"
FR_USER="freerad"
FR_GROUP="freerad"

DB_HOST="localhost"
DB_PORT="3306"  # fallback, akan di-detect otomatis dari MariaDB
DB_SOCKET="/var/run/mysqld/mysqld.sock"

DB_REMOTE_HOST="%"
ALLOW_REMOTE_DB=true

PORT_AUTH_START=11000
PORT_AUTH_STEP=10
COA_PORT_DEFAULT=3799
PORT_REGISTRY="$FREERADIUS_DIR/.port_registry"
FR_SCHEMA="$FREERADIUS_DIR/mods-config/sql/main/mysql/schema.sql"

API_DIR_BASE="/root"
API_REPO="https://github.com/heirro/freeradius-api"
PORT_API_START=8100

S3_REMOTE="ljns3"
S3_BUCKET="backup-db"
S3_BACKUP_ROOT="radiusdb"
S3_BACKUP_SCHEDULE="0 2 * * *"   # Tiap hari jam 02:00

# ============================================
# COLORS
# ============================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warning() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error()   { echo -e "${RED}[ERROR]${NC} $*"; }
header()  { echo -e "${CYAN}$*${NC}"; }

# ============================================
# FUNCTION: Check Root
# ============================================
check_root() {
    if [ "$EUID" -ne 0 ]; then
        error "Script harus dijalankan sebagai root!"
        exit 1
    fi
    success "Running as root"
}

# ============================================
# FUNCTION: Check & Install Dependencies
# ============================================
check_dependencies() {
    local pkgs=()

    command -v mariadb    &>/dev/null || pkgs+=(mariadb-client)
    command -v openssl    &>/dev/null || pkgs+=(openssl)
    command -v ss         &>/dev/null || pkgs+=(iproute2)
    command -v git        &>/dev/null || pkgs+=(git)
    command -v python3    &>/dev/null || pkgs+=(python3)
    python3 -c "import venv" &>/dev/null 2>&1 || pkgs+=(python3-venv)
    command -v freeradius &>/dev/null || pkgs+=(freeradius)
    { command -v radtest &>/dev/null && command -v radclient &>/dev/null; } \
        || pkgs+=(freeradius-utils)
    [ -f "$FR_SCHEMA" ] || pkgs+=(freeradius-mysql)

    [ ${#pkgs[@]} -eq 0 ] && return 0

    info "Installing missing dependencies: ${pkgs[*]}"
    if ! apt-get install -y "${pkgs[@]}" >/dev/null 2>&1; then
        error "Gagal install dependencies: ${pkgs[*]}"
        error "Coba manual: apt-get install -y ${pkgs[*]}"
        exit 1
    fi
    success "Dependencies ready"
}

# ============================================
# FUNCTION: Load Instance Info (aman, tanpa source)
# Menghindari bash mengeksekusi nilai seperti "05:57:50"
# sebagai command saat CREATED tidak dikuotasi.
# ============================================
load_instance_info() {
    local FILE=$1
    [ -f "$FILE" ] || return 1
    while IFS='=' read -r key value; do
        # Abaikan baris komentar dan baris kosong
        [[ "$key" =~ ^[[:space:]]*# ]] && continue
        [[ -z "$key" ]] && continue
        key="${key// /}"
        case "$key" in
            ADMIN_USERNAME) ADMIN_USERNAME="$value" ;;
            DB_NAME)        DB_NAME="$value"        ;;
            DB_USER)        DB_USER="$value"        ;;
            DB_PASS)        DB_PASS="$value"        ;;
            AUTH_PORT)      AUTH_PORT="$value"      ;;
            ACCT_PORT)      ACCT_PORT="$value"      ;;
            COA_PORT)       COA_PORT="$value"       ;;
            INNER_PORT)     INNER_PORT="$value"     ;;
            API_PORT)       API_PORT="$value"       ;;
        esac
    done < "$FILE"
}

# ============================================
# FUNCTION: MySQL via Unix Socket
# ============================================
mysql_cmd() {
    mariadb -u root "$@"
}

# ============================================
# FUNCTION: Test MariaDB
# ============================================
test_mariadb() {
    info "Testing MariaDB connection..."
    if mysql_cmd -e "SELECT 1;" >/dev/null 2>&1; then
        success "MariaDB OK"
        local detected
        detected=$(mysql_cmd -se "SHOW VARIABLES LIKE 'port';" 2>/dev/null | awk '{print $2}')
        if [[ "$detected" =~ ^[0-9]+$ ]] && [ "$detected" -gt 0 ]; then
            DB_PORT="$detected"
            success "MariaDB port detected: $DB_PORT"
        else
            warning "Tidak bisa deteksi port MariaDB, pakai default: $DB_PORT"
        fi
        return 0
    else
        error "Tidak bisa konek ke MariaDB!"
        error "Cek: systemctl status mariadb"
        error "Cek socket: ls -la ${DB_SOCKET}"
        return 1
    fi
}

# ============================================
# FUNCTION: Generate Password
# Pakai openssl agar tidak ada SIGPIPE
# ============================================
generate_password() {
    openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20
}

# ============================================
# FUNCTION: Auto-Assign Port
# ============================================
find_available_port() {
    local port
    touch "$PORT_REGISTRY"

    while true; do
        # Port acak dalam range 10000–59000 (inner = port+5000, max 64000 < 65535)
        port=$(( RANDOM % 49001 + 10000 ))
        # Cek registry
        if grep -q "^${port} " "$PORT_REGISTRY" 2>/dev/null; then continue; fi
        # Cek port auth di sistem
        if ss -tulnp 2>/dev/null | grep -q ":${port} "; then continue; fi
        # Cek port acct (auth+1)
        if ss -tulnp 2>/dev/null | grep -q ":$((port + 1)) "; then continue; fi
        # Cek port coa (auth+2000)
        if ss -tulnp 2>/dev/null | grep -q ":$((port + 2000)) "; then continue; fi
        # Cek port inner (auth+5000)
        if ss -tulnp 2>/dev/null | grep -q ":$((port + 5000)) "; then continue; fi
        echo "$port"
        return 0
    done
}

register_port() {
    local port=$1 admin=$2
    touch "$PORT_REGISTRY"
    echo "${port} # ${admin} auth"               >> "$PORT_REGISTRY"
    echo "$((port + 1)) # ${admin} acct"         >> "$PORT_REGISTRY"
    echo "$((port + 2000)) # ${admin} coa"       >> "$PORT_REGISTRY"
    echo "$((port + 5000)) # ${admin} inner"     >> "$PORT_REGISTRY"
}

unregister_port() {
    local admin=$1
    [ -f "$PORT_REGISTRY" ] && sed -i "/ # ${admin} /d" "$PORT_REGISTRY"
}

find_available_api_port() {
    local port=$PORT_API_START
    touch "$PORT_REGISTRY"

    while true; do
        if grep -q "^${port} " "$PORT_REGISTRY" 2>/dev/null; then
            port=$((port + 1)); continue
        fi
        if ss -tulnp 2>/dev/null | grep -q ":${port} "; then
            port=$((port + 1)); continue
        fi
        echo "$port"
        return 0
    done
}

# ============================================
# FUNCTION: Create Database & User
# ============================================
create_database() {
    local DB_NAME=$1 DB_USER=$2 DB_PASS=$3

    info "Creating database: $DB_NAME"

    if mysql_cmd -e "USE \`${DB_NAME}\`;" 2>/dev/null; then
        warning "Database '${DB_NAME}' sudah ada, skip"
    else
        mysql_cmd -e "CREATE DATABASE \`${DB_NAME}\`
            CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
        success "Database '${DB_NAME}' dibuat"
    fi

    # User localhost
    local user_local
    user_local=$(mysql_cmd -se \
        "SELECT COUNT(*) FROM mysql.user
         WHERE user='${DB_USER}' AND host='localhost';" 2>/dev/null || echo "0")

    if [ "${user_local:-0}" -gt 0 ]; then
        warning "User '${DB_USER}'@'localhost' sudah ada, update password"
        mysql_cmd -e "ALTER USER '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASS}';"
    else
        mysql_cmd -e "CREATE USER '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASS}';"
        success "User '${DB_USER}'@'localhost' dibuat"
    fi
    mysql_cmd -e "GRANT SELECT,INSERT,UPDATE,DELETE ON \`${DB_NAME}\`.*
        TO '${DB_USER}'@'localhost';"

    # User remote
    if [ "${ALLOW_REMOTE_DB}" = true ]; then
        local user_remote
        user_remote=$(mysql_cmd -se \
            "SELECT COUNT(*) FROM mysql.user
             WHERE user='${DB_USER}' AND host='${DB_REMOTE_HOST}';" \
            2>/dev/null || echo "0")

        if [ "${user_remote:-0}" -gt 0 ]; then
            warning "User '${DB_USER}'@'${DB_REMOTE_HOST}' sudah ada, update password"
            mysql_cmd -e "ALTER USER '${DB_USER}'@'${DB_REMOTE_HOST}'
                IDENTIFIED BY '${DB_PASS}';"
        else
            mysql_cmd -e "CREATE USER '${DB_USER}'@'${DB_REMOTE_HOST}'
                IDENTIFIED BY '${DB_PASS}';"
            success "User '${DB_USER}'@'${DB_REMOTE_HOST}' dibuat (remote)"
        fi
        mysql_cmd -e "GRANT SELECT,INSERT,UPDATE,DELETE ON \`${DB_NAME}\`.*
            TO '${DB_USER}'@'${DB_REMOTE_HOST}';"
    fi

    mysql_cmd -e "FLUSH PRIVILEGES;"
    success "Database & user selesai"
}

# ============================================
# FUNCTION: Drop Database & User
# ============================================
drop_database() {
    local DB_NAME=$1 DB_USER=$2

    echo ""
    warning "PERHATIAN! Akan menghapus:"
    echo "    Database : $DB_NAME"
    echo "    User     : ${DB_USER}@localhost"
    [ "${ALLOW_REMOTE_DB}" = true ] && \
        echo "    User     : ${DB_USER}@${DB_REMOTE_HOST}"
    echo ""
    read -r -p "Yakin hapus database & user? [y/N] " confirm
    echo ""
    [[ ! "${confirm}" =~ ^[Yy]$ ]] && { info "Skip hapus database"; return 0; }

    if mysql_cmd -e "USE \`${DB_NAME}\`;" 2>/dev/null; then
        mysql_cmd -e "DROP DATABASE \`${DB_NAME}\`;"
        success "Database '${DB_NAME}' dihapus"
    else
        warning "Database '${DB_NAME}' tidak ditemukan, skip"
    fi

    local user_local
    user_local=$(mysql_cmd -se \
        "SELECT COUNT(*) FROM mysql.user
         WHERE user='${DB_USER}' AND host='localhost';" 2>/dev/null || echo "0")
    if [ "${user_local:-0}" -gt 0 ]; then
        mysql_cmd -e "DROP USER '${DB_USER}'@'localhost';"
        success "User '${DB_USER}'@'localhost' dihapus"
    else
        warning "User '${DB_USER}'@'localhost' tidak ditemukan, skip"
    fi

    if [ "${ALLOW_REMOTE_DB}" = true ]; then
        local user_remote
        user_remote=$(mysql_cmd -se \
            "SELECT COUNT(*) FROM mysql.user
             WHERE user='${DB_USER}' AND host='${DB_REMOTE_HOST}';" \
            2>/dev/null || echo "0")
        if [ "${user_remote:-0}" -gt 0 ]; then
            mysql_cmd -e "DROP USER '${DB_USER}'@'${DB_REMOTE_HOST}';"
            success "User '${DB_USER}'@'${DB_REMOTE_HOST}' dihapus"
        else
            warning "User remote tidak ditemukan, skip"
        fi
    fi

    mysql_cmd -e "FLUSH PRIVILEGES;"
    success "Database & user selesai dihapus"
}

# ============================================
# FUNCTION: Import Schema
# ============================================
import_schema() {
    local DB_NAME=$1

    info "Importing FreeRADIUS schema ke: $DB_NAME"

    local table_exists
    table_exists=$(mysql_cmd "${DB_NAME}" -se \
        "SHOW TABLES LIKE 'radcheck';" 2>/dev/null || echo "")

    if [ -n "$table_exists" ]; then
        warning "Schema sudah ada, skip"
        return 0
    fi

    if [ ! -f "$FR_SCHEMA" ]; then
        error "Schema tidak ditemukan: $FR_SCHEMA"
        error "Install: apt install freeradius-mysql"
        return 1
    fi

    mysql_cmd "$DB_NAME" < "$FR_SCHEMA"
    success "Schema diimport ke '${DB_NAME}'"
    info "Tabel:"
    mysql_cmd "$DB_NAME" -e "SHOW TABLES;" | sed 's/^/    /'
}

# ============================================
# FUNCTION: Create SQL Module
# ============================================
create_sql_module() {
    local ADMIN_USERNAME=$1 DB_NAME=$2 DB_USER=$3 DB_PASS=$4
    local MODULE_NAME="sql_${ADMIN_USERNAME}"
    local MODULE_FILE="$MODS_AVAILABLE/$MODULE_NAME"

    info "Creating SQL module: $MODULE_NAME"

    cat > "$MODULE_FILE" << EOF
sql ${MODULE_NAME} {
    dialect = "mysql"
    driver  = "rlm_sql_\${dialect}"

    mysql {
        warnings = auto
    }

    server    = "${DB_HOST}"
    port      = ${DB_PORT}
    login     = "${DB_USER}"
    password  = "${DB_PASS}"
    radius_db = "${DB_NAME}"

    sql_user_name = "%{User-Name}"

    acct_table1      = "radacct"
    acct_table2      = "radacct"
    postauth_table   = "radpostauth"
    authcheck_table  = "radcheck"
    groupcheck_table = "radgroupcheck"
    authreply_table  = "radreply"
    groupreply_table = "radgroupreply"
    usergroup_table  = "radusergroup"

    read_clients          = yes
    client_table          = "nas"
    read_groups           = yes
    read_profiles         = yes
    delete_stale_sessions = yes

    group_attribute = "${MODULE_NAME}-SQL-Group"

    safe_characters = "@abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_: /"

    pool {
        start        = 3
        min          = 2
        max          = 10
        spare        = 3
        uses         = 0
        retry_delay  = 30
        lifetime     = 0
        idle_timeout = 60
    }

    \$INCLUDE \${modconfdir}/sql/main/\${dialect}/queries.conf
}
EOF

    ln -sf "$MODULE_FILE" "$MODS_ENABLED/$MODULE_NAME"
    chown -h "${FR_USER}:${FR_GROUP}" "$MODS_ENABLED/$MODULE_NAME"
    success "SQL module: $MODULE_NAME"
}

# ============================================
# FUNCTION: Create EAP Module
# FIX: store syntax v3 pakai = bukan { }
# ============================================
create_eap_module() {
    local ADMIN_USERNAME=$1 INNER_PORT=$2
    local MODULE_NAME="eap_${ADMIN_USERNAME}"
    local MODULE_FILE="$MODS_AVAILABLE/$MODULE_NAME"

    info "Creating EAP module: $MODULE_NAME"

    cat > "$MODULE_FILE" << EOF
eap ${MODULE_NAME} {
    default_eap_type              = ttls
    timer_expire                  = 60
    ignore_unknown_eap_types      = no
    cisco_accounting_username_bug = no
    max_sessions                  = \${max_requests}

    md5 { }

    gtc {
        auth_type = PAP
    }

    tls-config tls-${ADMIN_USERNAME} {
        private_key_password = whatever
        private_key_file     = /etc/ssl/private/ssl-cert-snakeoil.key
        certificate_file     = /etc/ssl/certs/ssl-cert-snakeoil.pem
        ca_file              = /etc/ssl/certs/ca-certificates.crt
        ca_path              = \${cadir}
        cipher_list          = "DEFAULT"
        tls_min_version      = "1.2"
        tls_max_version      = "1.3"
        ecdh_curve           = ""

        cache {
            enable   = no
            lifetime = 24
            store    = "Tunnel-Private-Group-Id"
        }

        verify { }

        ocsp {
            enable            = no
            override_cert_url = yes
            url               = "http://127.0.0.1/ocsp/"
        }
    }

    tls {
        tls = tls-${ADMIN_USERNAME}
    }

    ttls {
        tls                    = tls-${ADMIN_USERNAME}
        default_eap_type       = md5
        copy_request_to_tunnel = no
        use_tunneled_reply     = yes
        virtual_server         = "inner-tunnel-${ADMIN_USERNAME}"
    }

    peap {
        tls                    = tls-${ADMIN_USERNAME}
        default_eap_type       = mschapv2
        copy_request_to_tunnel = no
        use_tunneled_reply     = yes
        virtual_server         = "inner-tunnel-${ADMIN_USERNAME}"
    }

    mschapv2 { }
}
EOF

    ln -sf "$MODULE_FILE" "$MODS_ENABLED/$MODULE_NAME"
    chown -h "${FR_USER}:${FR_GROUP}" "$MODS_ENABLED/$MODULE_NAME"
    success "EAP module: $MODULE_NAME"
}

# ============================================
# FUNCTION: Create Inner Tunnel
# ============================================
create_inner_tunnel() {
    local ADMIN_USERNAME=$1 INNER_PORT=$2
    local SERVER_FILE="$SITES_AVAILABLE/inner-tunnel-${ADMIN_USERNAME}"

    info "Creating inner tunnel: port $INNER_PORT"

    cat > "$SERVER_FILE" << EOF
server inner-tunnel-${ADMIN_USERNAME} {

    listen {
        ipaddr = 127.0.0.1
        port   = ${INNER_PORT}
        type   = auth
    }

    authorize {
        filter_username
        chap
        mschap
        update control {
            &Proxy-To-Realm := LOCAL
        }
        eap_${ADMIN_USERNAME} {
            ok = return
        }
        sql_${ADMIN_USERNAME}
        pap
    }

    authenticate {
        Auth-Type PAP {
            pap
        }
        Auth-Type CHAP {
            chap
        }
        Auth-Type MS-CHAP {
            mschap
        }
        eap_${ADMIN_USERNAME}
    }

    session {
        sql_${ADMIN_USERNAME}
    }

    post-auth {
        sql_${ADMIN_USERNAME}
        Post-Auth-Type REJECT {
            sql_${ADMIN_USERNAME}
            attr_filter.access_reject
        }
    }
}
EOF

    ln -sf "$SERVER_FILE" "$SITES_ENABLED/inner-tunnel-${ADMIN_USERNAME}"
    chown -h "${FR_USER}:${FR_GROUP}" "$SITES_ENABLED/inner-tunnel-${ADMIN_USERNAME}"
    success "Inner tunnel: inner-tunnel-${ADMIN_USERNAME}"
}

# ============================================
# FUNCTION: Create Virtual Server
# ============================================
create_virtual_server() {
    local ADMIN_USERNAME=$1 AUTH_PORT=$2
    local ACCT_PORT=$((AUTH_PORT + 1))
    local COA_PORT=$((AUTH_PORT + 2000))
    local SERVER_NAME="${ADMIN_USERNAME}"          # <-- TANPA prefix pppoe_
    local SERVER_FILE="$SITES_AVAILABLE/$SERVER_NAME"

    info "Creating virtual server: $SERVER_NAME"
    info "  Auth : $AUTH_PORT | Acct : $ACCT_PORT | CoA : $COA_PORT"

    cat > "$SERVER_FILE" << EOF
server ${SERVER_NAME} {

    # ============================================
    # NAS dibaca dari tabel nas di database
    # Tambahkan manual:
    #
    # USE ${ADMIN_USERNAME};
    # INSERT INTO nas (nasname, shortname, type, secret, server)
    # VALUES ('IP_MIKROTIK', 'nama', 'other', 'secret', '${SERVER_NAME}');
    # ============================================

    listen {
        type   = auth
        ipaddr = *
        port   = ${AUTH_PORT}
        limit {
            max_connections = 16
            lifetime        = 0
            idle_timeout    = 30
        }
    }

    listen {
        type   = acct
        ipaddr = *
        port   = ${ACCT_PORT}
        limit {
            max_connections = 16
            lifetime        = 0
            idle_timeout    = 30
        }
    }

    listen {
        type   = coa
        ipaddr = *
        port   = ${COA_PORT}
        limit {
            max_connections = 16
            lifetime        = 0
            idle_timeout    = 30
        }
    }

    authorize {
        filter_username
        preprocess
        chap
        mschap
        digest
        eap_${ADMIN_USERNAME} {
            ok = return
        }
        files
        sql_${ADMIN_USERNAME}
        expiration
        logintime
        pap
        Autz-Type New-TLS-Connection {
            ok
        }
    }

    authenticate {
        Auth-Type PAP {
            pap
        }
        Auth-Type CHAP {
            chap
        }
        Auth-Type MS-CHAP {
            mschap
        }
        mschap
        digest
        eap_${ADMIN_USERNAME}
    }

    preacct {
        preprocess
        acct_unique
        files
    }

    accounting {
        detail
        sql_${ADMIN_USERNAME}
        exec
        attr_filter.accounting_response
    }

    session {
        sql_${ADMIN_USERNAME}
    }

    post-auth {
        if (session-state:User-Name && reply:User-Name && request:User-Name && (reply:User-Name == request:User-Name)) {
            update reply {
                &User-Name !* ANY
            }
        }
        update {
            &reply: += &session-state:
        }
        sql_${ADMIN_USERNAME}
        exec
        remove_reply_message_if_eap
        Post-Auth-Type REJECT {
            sql_${ADMIN_USERNAME}
            attr_filter.access_reject
            eap_${ADMIN_USERNAME}
            remove_reply_message_if_eap
        }
        Post-Auth-Type Challenge {
        }
    }

    recv-coa {
        ok
    }

    send-coa {
        ok
    }
}
EOF

    ln -sf "$SERVER_FILE" "$SITES_ENABLED/$SERVER_NAME"
    chown -h "${FR_USER}:${FR_GROUP}" "$SITES_ENABLED/$SERVER_NAME"
    success "Virtual server: $SERVER_NAME"
}

# ============================================
# FUNCTION: Create Log Dir
# ============================================
create_log_dir() {
    local A=$1
    mkdir -p "$LOG_DIR/radacct-${A}"
    chown -R "${FR_USER}:${FR_GROUP}" "$LOG_DIR/radacct-${A}"
    chmod 750 "$LOG_DIR/radacct-${A}"
    success "Log dir: $LOG_DIR/radacct-${A}"
}

# ============================================
# FUNCTION: Restart FreeRADIUS
# ============================================
restart_freeradius() {
    info "Testing FreeRADIUS config..."

    local TEST_OUTPUT
    TEST_OUTPUT=$(freeradius -XC 2>&1) || true

    if echo "$TEST_OUTPUT" | grep -q "Configuration appears to be OK"; then
        success "Config OK"
    else
        error "Config FAILED!"
        echo "$TEST_OUTPUT" | grep -iE "error|failed|unknown|cannot|denied|can't|socket" | head -30
        return 1
    fi

    info "Restarting FreeRADIUS..."
    systemctl restart freeradius

    local i
    for i in 1 2 3 4 5; do
        sleep 2
        if systemctl is-active --quiet freeradius; then
            success "FreeRADIUS restarted OK"
            return 0
        fi
        [ "$i" -lt 5 ] && info "Waiting for FreeRADIUS... (attempt $i/5)"
    done

    error "FreeRADIUS gagal start!"
    echo ""
    error "--- FreeRADIUS diagnostic (freeradius -X) ---"
    freeradius -X 2>&1 \
        | grep -E ".*[Ee]rror.*|.*[Ff]ail.*|.*[Cc]annot.*|.*[Aa]lready.*bind|.*[Pp]ermission.*" \
        | grep -v "^$" | head -30
    echo ""
    error "--- journalctl (app messages only) ---"
    journalctl -xeu freeradius.service --no-pager -o cat 2>/dev/null \
        | grep -v "^░" | grep -v "^$" | tail -15
    return 1
}

# ============================================
# FUNCTION: Delete Config
# ============================================
delete_config() {
    local A=$1
    info "Removing config: $A"
    # Hapus symlink/file enabled
    rm -f "$MODS_ENABLED/sql_${A}"              "$MODS_ENABLED/eap_${A}"
    rm -f "$SITES_ENABLED/${A}"                 "$SITES_ENABLED/inner-tunnel-${A}"
    # Hapus varian lama dengan prefix pppoe_ (backward compat)
    rm -f "$SITES_ENABLED/pppoe_${A}"           "$SITES_ENABLED/inner-tunnel-${A}"
    rm -f "$SITES_AVAILABLE/pppoe_${A}"
    # Hapus file available
    rm -f "$MODS_AVAILABLE/sql_${A}"            "$MODS_AVAILABLE/eap_${A}"
    rm -f "$SITES_AVAILABLE/${A}"               "$SITES_AVAILABLE/inner-tunnel-${A}"
    unregister_port "$A"
    success "Config deleted: $A"
}

# ============================================
# FUNCTION: Stop / Start
# ============================================
stop_instance() {
    local A=$1
    rm -f "$MODS_ENABLED/sql_${A}"         "$MODS_ENABLED/eap_${A}"
    rm -f "$SITES_ENABLED/${A}"            "$SITES_ENABLED/inner-tunnel-${A}"
    success "Instance '$A' stopped"
}

start_instance() {
    local A=$1
    [ -f "$SITES_AVAILABLE/${A}" ] || {
        error "Config tidak ada! Jalankan '$0 create ${A}' dulu."
        return 1
    }
    ln -sf "$MODS_AVAILABLE/sql_${A}"           "$MODS_ENABLED/sql_${A}"
    ln -sf "$MODS_AVAILABLE/eap_${A}"           "$MODS_ENABLED/eap_${A}"
    ln -sf "$SITES_AVAILABLE/inner-tunnel-${A}" "$SITES_ENABLED/inner-tunnel-${A}"
    ln -sf "$SITES_AVAILABLE/${A}"              "$SITES_ENABLED/${A}"
    chown -h "${FR_USER}:${FR_GROUP}" \
        "$MODS_ENABLED/sql_${A}"             \
        "$MODS_ENABLED/eap_${A}"             \
        "$SITES_ENABLED/inner-tunnel-${A}"   \
        "$SITES_ENABLED/${A}"
    success "Instance '$A' started"
}

# ============================================
# FUNCTION: List Instances
# ============================================
list_instances() {
    echo ""
    header "======================================================"
    header "  FreeRADIUS Instances"
    header "======================================================"

    local found=0
    for INFO_FILE in "$FREERADIUS_DIR"/.instance_*; do
        [ -f "$INFO_FILE" ] || continue
        found=1

        # Baca variabel dari file info (parser aman, tanpa source)
        local ADMIN_USERNAME DB_NAME AUTH_PORT ACCT_PORT COA_PORT
        load_instance_info "$INFO_FILE"

        local f="$SITES_AVAILABLE/${ADMIN_USERNAME}"
        local st; [ -L "$SITES_ENABLED/${ADMIN_USERNAME}" ] && \
            st="${GREEN}ENABLED${NC}" || st="${YELLOW}STOPPED${NC}"
        local as; ss -tulnp 2>/dev/null | grep -q ":${AUTH_PORT} " && \
            as="${GREEN}LISTEN${NC}" || as="${RED}DOWN${NC}"
        local cs; ss -tulnp 2>/dev/null | grep -q ":${ACCT_PORT} " && \
            cs="${GREEN}LISTEN${NC}" || cs="${RED}DOWN${NC}"
        local apis; systemctl is-active --quiet "${ADMIN_USERNAME}-api" 2>/dev/null && \
            apis="${GREEN}RUNNING${NC}" || apis="${RED}STOPPED${NC}"

        echo ""
        echo -e "  Instance  : ${BLUE}${ADMIN_USERNAME}${NC}  [${st}]"
        echo    "  Database  : ${DB_NAME}"
        echo -e "  Auth Port : ${AUTH_PORT}  [${as}]"
        echo -e "  Acct Port : ${ACCT_PORT}  [${cs}]"
        echo -e "  CoA  Port : ${COA_PORT}  [$(ss -tulnp 2>/dev/null | grep -q ":${COA_PORT} " && echo -e "${GREEN}LISTEN${NC}" || echo -e "${RED}DOWN${NC}")]"
        echo -e "  API  Port : ${API_PORT:-N/A}  [${apis}]"
    done

    [ $found -eq 0 ] && warning "Tidak ada instance"
    echo ""
    header "======================================================"
    echo ""
}

# ============================================
# FUNCTION: Test Instance
# ============================================
test_instance() {
    local A=$1
    [ -f "$SITES_AVAILABLE/${A}" ] || { error "Instance tidak ditemukan: $A"; return 1; }
    local INFO_FILE="$FREERADIUS_DIR/.instance_${A}"
    [ -f "$INFO_FILE" ] || { error "Info file tidak ditemukan: $INFO_FILE"; return 1; }
    local AUTH_PORT
    load_instance_info "$INFO_FILE"
    local ap="$AUTH_PORT"
    info "Testing $A — port $ap"
    ss -tulnp 2>/dev/null | grep -q ":${ap} " && \
        success "Port $ap LISTENING" || { error "Port $ap NOT listening!"; return 1; }
    if command -v radtest &>/dev/null; then
        echo ""
        info "Test Access-Request..."
        radtest testuser testpass "localhost:${ap}" 0 testing123 || true
    else
        warning "radtest tidak ada: apt install freeradius-utils"
    fi
}

# ============================================
# FUNCTION: Test Disconnect
# ============================================
test_disconnect() {
    local A=$1 U=$2 S=$3
    [ -f "$SITES_AVAILABLE/${A}" ] || { error "Instance tidak ditemukan: $A"; return 1; }
    local INFO_FILE="$FREERADIUS_DIR/.instance_${A}"
    [ -f "$INFO_FILE" ] || { error "Info file tidak ditemukan: $INFO_FILE"; return 1; }
    local COA_PORT
    load_instance_info "$INFO_FILE"
    local cp="$COA_PORT"
    command -v radclient &>/dev/null || { error "radclient tidak ada"; return 1; }
    printf "User-Name=%s\nAcct-Session-Id=%s\n" "$U" "$S" | \
        radclient "127.0.0.1:${cp}" disconnect testing123
}

# ============================================
# FUNCTION: Setup API
# ============================================
setup_api() {
    local A=$1 DB_NAME=$2 DB_USER=$3 DB_PASS=$4 API_PORT=$5
    local API_DIR="${API_DIR_BASE}/${A}-api"
    local SERVICE_NAME="${A}-api"
    local SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

    # Cek sudah ada
    if [ -d "$API_DIR" ] && [ -f "$SERVICE_FILE" ]; then
        warning "API '${SERVICE_NAME}' sudah ada, skip"
        return 0
    fi

    # Clone repo jika belum ada
    if [ -d "$API_DIR" ]; then
        info "Direktori '$API_DIR' sudah ada, skip clone"
    else
        info "Cloning API repo ke ${API_DIR}..."
        git clone --quiet "$API_REPO" "$API_DIR" || {
            error "Gagal clone repo: $API_REPO"
            return 1
        }
        success "Repo cloned: $API_DIR"
    fi

    # Buat .env
    local SWAGGER_PASS
    SWAGGER_PASS=$(generate_password)
    info "Membuat .env..."
    cat > "${API_DIR}/.env" << ENVEOF
# Application Settings
APP_NAME=${A}-api
APP_DEBUG=False

# Swagger/BASIC Auth Credentials
SWAGGER_USERNAME=admin
SWAGGER_PASSWORD=${SWAGGER_PASS}

# Database Settings
DB_TYPE=mariadb
DB_HOST=${DB_HOST}
DB_PORT=${DB_PORT}
DB_NAME=${DB_NAME}
DB_USER=${DB_USER}
DB_PASSWORD=${DB_PASS}
ENVEOF
    chmod 600 "${API_DIR}/.env"
    success ".env dibuat"

    # Patch autoclearzombie.sh dengan credentials
    if [ -f "${API_DIR}/autoclearzombie.sh" ]; then
        info "Mengisi credentials di autoclearzombie.sh..."
        sed -i \
            -e "s|^DB_HOST=.*|DB_HOST=\"${DB_HOST}\"|" \
            -e "s|^DB_PORT=.*|DB_PORT=\"${DB_PORT}\"|" \
            -e "s|^DB_USER=.*|DB_USER=\"${DB_USER}\"|" \
            -e "s|^DB_PASS=.*|DB_PASS=\"${DB_PASS}\"|" \
            -e "s|^DB_NAME=.*|DB_NAME=\"${DB_NAME}\"|" \
            "${API_DIR}/autoclearzombie.sh"
        chmod +x "${API_DIR}/autoclearzombie.sh"
        success "autoclearzombie.sh dikonfigurasi"

        # Buat cron job autoclearzombie (tiap 30 menit)
        local CRON_JOB="*/15 * * * * ${API_DIR}/autoclearzombie.sh >> /var/log/autoclearzombie-${A}.log 2>&1"
        local CRON_MARKER="autoclearzombie-${A}"
        if crontab -l 2>/dev/null | grep -qF "$CRON_MARKER"; then
            warning "Cron autoclearzombie-${A} sudah ada, skip"
        else
            ( crontab -l 2>/dev/null; echo "$CRON_JOB" ) | crontab -
            success "Cron job dibuat: ${CRON_JOB}"
        fi
    fi

    # Patch autobackups3.sh dengan credentials
    if [ -f "${API_DIR}/autobackups3.sh" ]; then
        info "Mengisi credentials di autobackups3.sh..."
        sed -i \
            -e "s|^REMOTE=.*|REMOTE=\"${S3_REMOTE}\"|" \
            -e "s|^BUCKET=.*|BUCKET=\"${S3_BUCKET}\"|" \
            -e "s|^BACKUP_PATH=.*|BACKUP_PATH=\"${S3_BACKUP_ROOT}/${A}\"|" \
            -e "s|^DB_HOST=.*|DB_HOST=\"${DB_HOST}\"|" \
            -e "s|^DB_PORT=.*|DB_PORT=\"${DB_PORT}\"|" \
            -e "s|^DB_USER=.*|DB_USER=\"${DB_USER}\"|" \
            -e "s|^DB_PASS=.*|DB_PASS=\"${DB_PASS}\"|" \
            -e "s|^DB_NAME=.*|DB_NAME=\"${DB_NAME}\"|" \
            "${API_DIR}/autobackups3.sh"
        chmod +x "${API_DIR}/autobackups3.sh"
        success "autobackups3.sh dikonfigurasi (S3: ${S3_REMOTE}:${S3_BUCKET}/${S3_BACKUP_ROOT}/${A})"

        # Buat cron job backup S3
        local CRON_BACKUP="${S3_BACKUP_SCHEDULE} ${API_DIR}/autobackups3.sh >> /var/log/autobackups3-${A}.log 2>&1"
        local CRON_BACKUP_MARKER="autobackups3-${A}"
        if crontab -l 2>/dev/null | grep -qF "$CRON_BACKUP_MARKER"; then
            warning "Cron autobackups3-${A} sudah ada, skip"
        else
            ( crontab -l 2>/dev/null; echo "$CRON_BACKUP" ) | crontab -
            success "Cron backup S3 dibuat: ${CRON_BACKUP}"
        fi
    fi

    # Setup Python venv
    info "Setting up Python venv..."
    python3 -m venv "${API_DIR}/venv" >/dev/null 2>&1 || {
        error "Gagal buat Python venv!"
        return 1
    }
    "${API_DIR}/venv/bin/pip" install -q -r "${API_DIR}/requirements.txt" || {
        error "Gagal install requirements.txt!"
        return 1
    }
    success "Python venv ready"

    # Register API port
    touch "$PORT_REGISTRY"
    echo "${API_PORT} # ${A} api" >> "$PORT_REGISTRY"

    # Buat systemd service
    info "Membuat systemd service: ${SERVICE_NAME}..."
    cat > "$SERVICE_FILE" << SVCEOF
[Unit]
Description=RadiusAPI with Uvicorn - ${A}
After=network.target

[Service]
User=root
Group=root
WorkingDirectory=${API_DIR}
ExecStart=${API_DIR}/venv/bin/uvicorn main:app --host 0.0.0.0 --port ${API_PORT} --workers 4
Restart=always
RestartSec=5
SyslogIdentifier=${SERVICE_NAME}

[Install]
WantedBy=multi-user.target
SVCEOF

    systemctl daemon-reload
    systemctl enable --quiet "${SERVICE_NAME}"
    systemctl start "${SERVICE_NAME}"
    sleep 2

    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        success "API '${SERVICE_NAME}' berjalan di port ${API_PORT}"
    else
        error "API service gagal start!"
        journalctl -xeu "${SERVICE_NAME}.service" --no-pager | tail -10
        return 1
    fi
}

# ============================================
# FUNCTION: Delete API
# ============================================
delete_api() {
    local A=$1
    local SERVICE_NAME="${A}-api"
    local SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    local API_DIR="${API_DIR_BASE}/${A}-api"

    if [ -f "$SERVICE_FILE" ]; then
        info "Stopping & removing service: ${SERVICE_NAME}..."
        systemctl stop    "${SERVICE_NAME}" 2>/dev/null || true
        systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
        rm -f "$SERVICE_FILE"
        systemctl daemon-reload
        success "Service '${SERVICE_NAME}' dihapus"
    else
        warning "Service '${SERVICE_NAME}' tidak ditemukan, skip"
    fi

    # Hapus cron job autoclearzombie
    if crontab -l 2>/dev/null | grep -qF "autoclearzombie-${A}"; then
        crontab -l 2>/dev/null | grep -vF "autoclearzombie-${A}" | crontab -
        success "Cron job autoclearzombie-${A} dihapus"
    else
        info "Cron autoclearzombie-${A} tidak ditemukan, skip"
    fi

    # Hapus cron job backup S3
    if crontab -l 2>/dev/null | grep -qF "autobackups3-${A}"; then
        crontab -l 2>/dev/null | grep -vF "autobackups3-${A}" | crontab -
        success "Cron job autobackups3-${A} dihapus"
    else
        info "Cron autobackups3-${A} tidak ditemukan, skip"
    fi

    if [ -d "$API_DIR" ]; then
        echo ""
        read -r -p "Hapus direktori API ${API_DIR}? [y/N] " confirm_api
        echo ""
        if [[ "${confirm_api}" =~ ^[Yy]$ ]]; then
            rm -rf "$API_DIR"
            success "Direktori API dihapus: $API_DIR"
        else
            info "Direktori API dibiarkan: $API_DIR"
        fi
    fi
}

# ============================================
# MAIN
# ============================================
check_root
check_dependencies

case "${1:-}" in

    # ------------------------------------------
    create)
        if [ $# -lt 2 ]; then
            echo ""
            echo "Usage: $0 create <admin_username> [db_pass]"
            echo ""
            echo "  admin_username : nama instance & database"
            echo "  db_pass        : password DB (opsional, auto-generate)"
            echo ""
            echo "Contoh:"
            echo "  $0 create replaymedia"
            echo "  $0 create baimnabil MyPass123"
            echo ""
            exit 1
        fi

        ADMIN_USERNAME=$2
        DB_NAME="${ADMIN_USERNAME}"
        DB_USER="${ADMIN_USERNAME}"
        DB_PASS="${3:-$(generate_password)}"

        info "Mencari port kosong..."
        AUTH_PORT=$(find_available_port)
        ACCT_PORT=$((AUTH_PORT + 1))
        COA_PORT=$((AUTH_PORT + 2000))
        INNER_PORT=$((AUTH_PORT + 5000))
        API_PORT=$(find_available_api_port)

        echo ""
        header "======================================================"
        header "  Creating Instance: $ADMIN_USERNAME"
        header "======================================================"
        echo "  Database   : $DB_NAME"
        echo "  DB User    : $DB_USER"
        echo "  DB Password: $DB_PASS"
        echo "  Auth Port  : $AUTH_PORT"
        echo "  Acct Port  : $ACCT_PORT"
        echo "  CoA  Port  : $COA_PORT"
        echo "  Inner Port : $INNER_PORT"
        echo "  API  Port  : $API_PORT"
        header "======================================================"
        echo ""

        # Simpan info instance
        INFO_FILE="$FREERADIUS_DIR/.instance_${ADMIN_USERNAME}"
        INFO_API_FILE="$API_DIR_BASE/${ADMIN_USERNAME}-api/.env"
        cat > "$INFO_FILE" << INFOEOF
ADMIN_USERNAME=${ADMIN_USERNAME}
DB_NAME=${DB_NAME}
DB_USER=${DB_USER}
DB_PASS=${DB_PASS}
AUTH_PORT=${AUTH_PORT}
ACCT_PORT=${ACCT_PORT}
COA_PORT=${COA_PORT}
INNER_PORT=${INNER_PORT}
API_PORT=${API_PORT}
SWAGGER_USERNAME=admin
SWAGGER_PASSWORD=$(grep '^SWAGGER_PASSWORD=' "$INFO_API_FILE" | cut -d= -f2)
WEB_API_URL=http://$(hostname -I | awk '{print $1}'):${API_PORT}/docs
CREATED="$(date '+%Y-%m-%d %H:%M:%S')"
INFOEOF
        chmod 600 "$INFO_FILE"

        test_mariadb                                               || exit 1
        create_database       "$DB_NAME" "$DB_USER" "$DB_PASS"
        import_schema         "$DB_NAME"
        create_sql_module     "$ADMIN_USERNAME" "$DB_NAME" "$DB_USER" "$DB_PASS"
        create_eap_module     "$ADMIN_USERNAME" "$INNER_PORT"
        create_inner_tunnel   "$ADMIN_USERNAME" "$INNER_PORT"
        create_virtual_server "$ADMIN_USERNAME" "$AUTH_PORT"
        create_log_dir        "$ADMIN_USERNAME"
        register_port         "$AUTH_PORT" "$ADMIN_USERNAME"
        restart_freeradius
        setup_api             "$ADMIN_USERNAME" "$DB_NAME" "$DB_USER" "$DB_PASS" "$API_PORT"

        echo ""
        header "======================================================"
        success "Instance '$ADMIN_USERNAME' berhasil dibuat!"
        echo ""
        echo "  Auth Port  : $AUTH_PORT"
        echo "  Acct Port  : $ACCT_PORT"
        echo "  DB Name    : $DB_NAME"
        echo "  DB User    : $DB_USER"
        echo "  DB Pass    : $DB_PASS"
        echo " Swagger User: $(grep '^SWAGGER_USERNAME=' "$INFO_API_FILE" | cut -d= -f2)"
        echo " Swagger Pass: $(grep '^SWAGGER_PASSWORD=' "$INFO_API_FILE" | cut -d= -f2)"
        echo "  Web API URL: $API_PORT  → http://$(hostname -I | awk '{print $1}'):${API_PORT}/docs"
        echo ""
        echo "  Info  : $0 info $ADMIN_USERNAME"
        echo "  List  : $0 list"
        header "======================================================"
        echo ""
        ;;

    # ------------------------------------------
    delete)
        if [ $# -lt 2 ]; then
            echo "Usage: $0 delete <admin> [--with-db]"
            echo ""
            echo "  --with-db   Hapus juga database & user MariaDB"
            echo ""
            echo "Contoh:"
            echo "  $0 delete replaymedia            # config saja"
            echo "  $0 delete replaymedia --with-db  # config + database"
            exit 1
        fi

        ADMIN_USERNAME=$2
        WITH_DB="${3:-}"
        INFO_FILE="$FREERADIUS_DIR/.instance_${ADMIN_USERNAME}"

        echo ""
        header "======================================================"
        header "  Deleting Instance: $ADMIN_USERNAME"
        header "======================================================"

        if [ "${WITH_DB}" = "--with-db" ]; then
            if [ -f "$INFO_FILE" ]; then
                load_instance_info "$INFO_FILE"
                test_mariadb || exit 1
                drop_database "$DB_NAME" "$DB_USER"
            else
                warning "File info tidak ditemukan, skip drop database"
            fi
        else
            info "Hanya config yang dihapus (tambah --with-db untuk hapus database)"
        fi

        delete_config "$ADMIN_USERNAME"
        delete_api    "$ADMIN_USERNAME"
        rm -f "$INFO_FILE"

        LOG_PATH="$LOG_DIR/radacct-${ADMIN_USERNAME}"
        if [ -d "$LOG_PATH" ]; then
            echo ""
            read -r -p "Hapus log directory ${LOG_PATH}? [y/N] " confirm_log
            echo ""
            if [[ "${confirm_log}" =~ ^[Yy]$ ]]; then
                rm -rf "$LOG_PATH"
                success "Log directory dihapus"
            else
                info "Log directory dibiarkan: $LOG_PATH"
            fi
        fi

        restart_freeradius
        echo ""
        success "Instance '$ADMIN_USERNAME' dihapus!"
        echo ""
        ;;

    # ------------------------------------------
    stop)
        [ $# -ge 2 ] || { echo "Usage: $0 stop <admin>"; exit 1; }
        stop_instance "$2"
        restart_freeradius
        ;;

    # ------------------------------------------
    start)
        [ $# -ge 2 ] || { echo "Usage: $0 start <admin>"; exit 1; }
        start_instance "$2"
        restart_freeradius
        ;;

    # ------------------------------------------
    restart)
        restart_freeradius
        ;;

    # ------------------------------------------
    list)
        list_instances
        ;;

    # ------------------------------------------
    test)
        [ $# -ge 2 ] || { echo "Usage: $0 test <admin>"; exit 1; }
        test_instance "$2"
        ;;

    # ------------------------------------------
    test-disconnect)
        [ $# -ge 4 ] || {
            echo "Usage: $0 test-disconnect <admin> <username> <session-id>"
            exit 1
        }
        test_disconnect "$2" "$3" "$4"
        ;;

    # ------------------------------------------
    info)
        [ $# -ge 2 ] || { echo "Usage: $0 info <admin>"; exit 1; }
        INFO_FILE="$FREERADIUS_DIR/.instance_${2}"
        INFO_API_FILE="$API_DIR_BASE/${2}-api/.env"
        [ -f "$INFO_FILE" ] || { error "Info tidak ditemukan: $2"; exit 1; }
        echo ""
        header "=== Instance: $2 ==="
        echo ""
        echo " Auth Port  : $(grep '^AUTH_PORT=' "$INFO_FILE" | cut -d= -f2)"
        echo " Acct Port  : $(grep '^ACCT_PORT=' "$INFO_FILE" | cut -d= -f2)"
        echo " CoA  Port  : $(grep '^COA_PORT=' "$INFO_FILE" | cut -d= -f2)"
        echo " API  Port  : $(grep '^API_PORT=' "$INFO_FILE" | cut -d= -f2)"
        echo " DB Name    : $(grep '^DB_NAME=' "$INFO_FILE" | cut -d= -f2)"
        echo " DB User    : $(grep '^DB_USER=' "$INFO_FILE" | cut -d= -f2)"
        echo " DB Pass    : $(grep '^DB_PASS=' "$INFO_FILE" | cut -d= -f2)"
        echo " Swagger User: $(grep '^SWAGGER_USERNAME=' "$INFO_API_FILE" | cut -d= -f2)"
        echo " Swagger Pass: $(grep '^SWAGGER_PASSWORD=' "$INFO_API_FILE" | cut -d= -f2)"
        echo " API URL    : $(grep '^WEB_API_URL=' "$INFO_FILE" | cut -d= -f2)"
        #cat "$INFO_FILE"
        echo ""
        ;;

    # ------------------------------------------
    *)
        echo ""
        header "FreeRADIUS Multi-Instance Manager"
        header "=================================="
        echo ""
        echo "Usage: $0 {command} [options]"
        echo ""
        echo "  create <admin> [db_pass]           Buat instance + database baru"
        echo "  delete <admin> [--with-db]         Hapus instance"
        echo "                  --with-db          + hapus database & user MariaDB"
        echo "  stop   <admin>                     Nonaktifkan instance"
        echo "  start  <admin>                     Aktifkan instance"
        echo "  restart                            Restart FreeRADIUS"
        echo "  list                               Lihat semua instance"
        echo "  test   <admin>                     Test instance"
        echo "  test-disconnect <admin> <u> <sid>  CoA Disconnect"
        echo "  info   <admin>                     Detail instance"
        echo ""
        echo "Contoh:"
        echo "  $0 create radiussite1"
        echo "  $0 create radiussite2 MyPass123"
        echo "  $0 list"
        echo "  $0 info radiussite1"
        echo "  $0 stop radiussite1"
        echo "  $0 start radiussite1"
        echo "  $0 delete radiussite1 --with-db"
        echo ""
        exit 1
        ;;
esac

exit 0