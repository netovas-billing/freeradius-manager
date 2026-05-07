#!/usr/bin/env bash
# install.sh - One-command installer for radius-manager-api (Ubuntu/Debian).
#
#   curl -fsSL https://raw.githubusercontent.com/netovas-billing/freeradius-manager/master/install.sh | sudo bash
#   # or, from a checkout:
#   sudo bash install.sh
#
# Run `bash install.sh --help` for the env-var override matrix.
# Idempotent: re-running detects existing state and skips safely.

set -euo pipefail

# Defaults chosen to match docs/SRS-RadiusManagerAPI.md + deployments/systemd.
RM_INSTALL_BIND="${RM_INSTALL_BIND:-127.0.0.1:9000}"
RM_INSTALL_REPO="${RM_INSTALL_REPO:-https://github.com/netovas-billing/freeradius-manager.git}"
RM_INSTALL_BRANCH="${RM_INSTALL_BRANCH:-master}"
RM_INSTALL_DIR="${RM_INSTALL_DIR:-/opt/freeradius-manager}"
RM_INSTALL_SKIP_TESTS="${RM_INSTALL_SKIP_TESTS:-0}"

# Pinned because go.mod declares `go 1.26.2`; older toolchains refuse to build.
GO_VERSION="${GO_VERSION:-1.26.2}"

BIN_DST="/usr/local/bin/radius-manager-api"
ETC_DIR="/etc/radius-manager-api"
TOKEN_FILE="${ETC_DIR}/token"
STATE_DIR="/var/lib/radius-manager-api"
LOG_DIR="/var/log/radius-manager-api"
UNIT_DST="/etc/systemd/system/radius-manager-api.service"
GO_PROFILE="/etc/profile.d/go.sh"

TOTAL_PHASES=14
PHASE=0

if [[ -t 1 ]]; then
    C_RESET=$'\033[0m'; C_GREEN=$'\033[32m'; C_RED=$'\033[31m'
    C_YELLOW=$'\033[33m'; C_CYAN=$'\033[36m'; C_BOLD=$'\033[1m'
else
    C_RESET=""; C_GREEN=""; C_RED=""; C_YELLOW=""; C_CYAN=""; C_BOLD=""
fi
ok()    { printf "%s[OK]%s   %s\n"   "$C_GREEN"  "$C_RESET" "$*"; }
warn()  { printf "%s[WARN]%s %s\n"   "$C_YELLOW" "$C_RESET" "$*"; }
fail()  { printf "%s[FAIL]%s %s\n"   "$C_RED"    "$C_RESET" "$*" >&2; exit 1; }
info()  { printf "%s[..]%s   %s\n"   "$C_CYAN"   "$C_RESET" "$*"; }
phase() { PHASE=$((PHASE + 1)); printf "\n%s==> Phase %d/%d: %s%s\n" "$C_BOLD" "$PHASE" "$TOTAL_PHASES" "$*" "$C_RESET"; }

usage() {
    cat <<EOF
install.sh - One-command installer for radius-manager-api.

USAGE
  sudo bash install.sh [--help]
  curl -fsSL <raw-url>/install.sh | sudo bash

ENVIRONMENT (defaults shown)
  RM_INSTALL_BIND=127.0.0.1:9000        Listen address baked into the systemd unit.
  RM_INSTALL_REPO=...freeradius-manager.git  Source repo (curl|bash mode only).
  RM_INSTALL_BRANCH=master              Branch to clone.
  RM_INSTALL_DIR=/opt/freeradius-manager  Clone target.
  RM_INSTALL_SKIP_TESTS=0               1=skip 'go test ./...' (emergencies only).
  GO_VERSION=${GO_VERSION}                  Pinned Go toolchain (must satisfy go.mod).
EOF
}

for arg in "$@"; do
    case "$arg" in
        --help|-h) usage; exit 0 ;;
        *) fail "unknown argument: $arg (try --help)" ;;
    esac
done

# -- Phase 1: pre-flight ------------------------------------------------------
phase "Pre-flight (root + OS detection)"
[[ "$EUID" -ne 0 ]] && fail "must run as root (try: sudo bash install.sh)"
[[ -r /etc/os-release ]] || fail "/etc/os-release missing - cannot detect distro"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}:${ID_LIKE:-}" in
    ubuntu:*|debian:*|*:*ubuntu*|*:*debian*) ok "detected ${PRETTY_NAME:-${ID:-unknown}}" ;;
    *) fail "unsupported distro '${ID:-unknown}' - this installer supports Ubuntu/Debian only" ;;
esac
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a   # silence needrestart kernel/service prompts on Ubuntu 22.04+

# -- Phase 2: apt deps --------------------------------------------------------
phase "apt dependencies"
# ssl-cert pulls /etc/ssl/certs/ssl-cert-snakeoil.pem (freeradius eap needs it).
APT_PKGS=(
    mariadb-server freeradius freeradius-mysql freeradius-utils
    python3 python3-venv python3-pip
    git openssl iproute2 curl ca-certificates ssl-cert build-essential
)

# Skip apt-get update if pkgcache is fresh - cheap optimization for re-runs.
if [[ -f /var/cache/apt/pkgcache.bin ]] && \
   find /var/cache/apt/pkgcache.bin -mmin -60 2>/dev/null | grep -q .; then
    ok "apt cache fresh (<60min); skipping apt-get update"
else
    info "apt-get update..."
    apt-get update -y -qq
fi

MISSING=()
for pkg in "${APT_PKGS[@]}"; do
    dpkg-query -W -f='${Status}\n' "$pkg" 2>/dev/null | grep -q "install ok installed" || MISSING+=("$pkg")
done
if [[ "${#MISSING[@]}" -eq 0 ]]; then
    ok "all apt dependencies already installed"
else
    info "installing ${#MISSING[@]} package(s): ${MISSING[*]}"
    apt-get install -y -qq --no-install-recommends "${MISSING[@]}"
    ok "apt packages installed"
fi

# -- Phase 3: Go toolchain ----------------------------------------------------
phase "Go toolchain (target ${GO_VERSION})"

# go.mod hard-floor is 1.26 - older Go refuses to build. Always upgrade if older.
target_minor="${GO_VERSION#*.}"; target_minor="${target_minor%%.*}"
need_install=1
for candidate in /usr/local/go/bin/go "$(command -v go 2>/dev/null || true)"; do
    [[ -n "$candidate" && -x "$candidate" ]] || continue
    cur=$("$candidate" version 2>/dev/null | awk '{print $3}'); cur="${cur#go}"
    cur_major="${cur%%.*}"; cur_rest="${cur#*.}"; cur_minor="${cur_rest%%.*}"
    [[ -z "$cur_major" || -z "$cur_minor" ]] && continue
    if (( cur_major > 1 )) || (( cur_major == 1 && cur_minor >= target_minor )); then
        GO_BIN="$candidate"; need_install=0; break
    fi
done

if [[ "$need_install" -eq 1 ]]; then
    arch="$(dpkg --print-architecture)"
    case "$arch" in
        amd64) gotar="go${GO_VERSION}.linux-amd64.tar.gz" ;;
        arm64) gotar="go${GO_VERSION}.linux-arm64.tar.gz" ;;
        armhf) gotar="go${GO_VERSION}.linux-armv6l.tar.gz" ;;
        *) fail "unsupported arch for Go install: $arch" ;;
    esac
    url="https://go.dev/dl/${gotar}"
    info "downloading $url"
    tmpd=$(mktemp -d); trap 'rm -rf "$tmpd"' EXIT
    curl -fsSL -o "$tmpd/$gotar" "$url" || fail "could not download Go from $url"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmpd/$gotar"
    rm -rf "$tmpd"; trap - EXIT
    GO_BIN="/usr/local/go/bin/go"
    ok "installed $($GO_BIN version)"
else
    ok "go toolchain already satisfies floor: $($GO_BIN version)"
fi

if [[ ! -f "$GO_PROFILE" ]] || ! grep -q "/usr/local/go/bin" "$GO_PROFILE"; then
    printf '# Added by radius-manager-api install.sh\nexport PATH="/usr/local/go/bin:$PATH"\n' > "$GO_PROFILE"
    chmod 0644 "$GO_PROFILE"
    ok "wrote $GO_PROFILE"
fi
export PATH="/usr/local/go/bin:$PATH"

# -- Phase 4: source dir ------------------------------------------------------
phase "Source checkout"
# If install.sh lives next to a go.mod, we're inside an existing clone. Else clone.
SCRIPT_PATH=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
SOURCE_DIR=""
if [[ -n "$SCRIPT_PATH" && -f "$SCRIPT_PATH/go.mod" ]]; then
    SOURCE_DIR="$SCRIPT_PATH"
    ok "running from existing checkout: $SOURCE_DIR"
elif [[ -d "$RM_INSTALL_DIR/.git" ]]; then
    info "updating existing clone at $RM_INSTALL_DIR"
    git -C "$RM_INSTALL_DIR" fetch --depth 1 origin "$RM_INSTALL_BRANCH" >/dev/null 2>&1 || true
    git -C "$RM_INSTALL_DIR" checkout "$RM_INSTALL_BRANCH" >/dev/null 2>&1 || true
    git -C "$RM_INSTALL_DIR" reset --hard "origin/$RM_INSTALL_BRANCH" >/dev/null 2>&1 || true
    SOURCE_DIR="$RM_INSTALL_DIR"
    ok "source updated at $SOURCE_DIR"
else
    info "cloning $RM_INSTALL_REPO ($RM_INSTALL_BRANCH) -> $RM_INSTALL_DIR"
    mkdir -p "$(dirname "$RM_INSTALL_DIR")"
    git clone --depth 1 --branch "$RM_INSTALL_BRANCH" "$RM_INSTALL_REPO" "$RM_INSTALL_DIR"
    SOURCE_DIR="$RM_INSTALL_DIR"
    ok "source cloned to $SOURCE_DIR"
fi

# -- Phase 5: go test ---------------------------------------------------------
phase "go test ./... (skip with RM_INSTALL_SKIP_TESTS=1)"
if [[ "$RM_INSTALL_SKIP_TESTS" == "1" ]]; then
    warn "RM_INSTALL_SKIP_TESTS=1 - SKIPPING tests (not recommended)"
else
    info "running go test ./... in $SOURCE_DIR"
    # System-wide writable cache survives ProtectHome=yes for the service.
    export GOCACHE="${GOCACHE:-/var/cache/go-build}"
    export GOPATH="${GOPATH:-/var/lib/go}"
    mkdir -p "$GOCACHE" "$GOPATH"
    ( cd "$SOURCE_DIR" && "$GO_BIN" test ./... ) || fail "go test ./... failed"
    ok "all unit tests passed"
fi

# -- Phase 6: build + install binary ------------------------------------------
phase "Build radius-manager-api"
build_dir="$SOURCE_DIR/bin"
mkdir -p "$build_dir"
info "go build ./cmd/radius-manager-api"
( cd "$SOURCE_DIR" && CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags='-s -w' \
    -o "$build_dir/radius-manager-api" ./cmd/radius-manager-api )
install -m 0755 "$build_dir/radius-manager-api" "$BIN_DST"
ok "installed $BIN_DST ($($BIN_DST version 2>/dev/null || echo '?'))"

# -- Phase 7: config dir + token ----------------------------------------------
phase "Config dir + API token"
mkdir -p "$ETC_DIR"; chmod 0700 "$ETC_DIR"; chown root:root "$ETC_DIR"
if [[ -s "$TOKEN_FILE" ]]; then
    ok "token already present at $TOKEN_FILE (kept)"
else
    "$BIN_DST" init > "$TOKEN_FILE"
    chmod 0600 "$TOKEN_FILE"; chown root:root "$TOKEN_FILE"
    ok "generated $TOKEN_FILE (root:root, 0600)"
fi

# -- Phase 8: state + log dirs ------------------------------------------------
phase "State and log dirs"
for d in "$STATE_DIR" "$LOG_DIR"; do
    mkdir -p "$d"; chmod 0750 "$d"; chown root:root "$d"
done
ok "ensured $STATE_DIR and $LOG_DIR (0750 root:root)"

# -- Phase 9: mariadb ---------------------------------------------------------
phase "MariaDB service"
# Default Debian/Ubuntu mariadb-server uses unix_socket auth for root@localhost,
# matching RM_API_DB_DSN=root@unix(...)/. We don't touch grants here.
if systemctl list-unit-files mariadb.service >/dev/null 2>&1; then
    if systemctl is-active --quiet mariadb; then
        ok "mariadb already active"
    else
        info "starting mariadb"
        systemctl enable --now mariadb
        ok "mariadb started"
    fi
else
    warn "mariadb.service unit not present"
fi
if [[ -S /var/run/mysqld/mysqld.sock ]] && \
   mysql --protocol=socket -uroot -e "SELECT 1" >/dev/null 2>&1; then
    ok "root@unix(/var/run/mysqld/mysqld.sock) login works"
else
    warn "root unix-socket login not yet usable (operator may need post-install setup)"
fi

# -- Phase 10: freeradius -----------------------------------------------------
phase "FreeRADIUS service"
if systemctl list-unit-files freeradius.service >/dev/null 2>&1; then
    if systemctl is-active --quiet freeradius; then
        ok "freeradius already active"
    else
        info "starting freeradius"
        systemctl enable --now freeradius || warn "freeradius failed to start (check 'journalctl -u freeradius')"
        systemctl is-active --quiet freeradius && ok "freeradius active"
    fi
else
    warn "freeradius.service unit not present"
fi

# -- Phase 11: install systemd unit -------------------------------------------
phase "Install systemd unit"
[[ -f "$SOURCE_DIR/deployments/systemd/radius-manager-api.service" ]] || \
    fail "missing $SOURCE_DIR/deployments/systemd/radius-manager-api.service"

tmp_unit="$(mktemp)"
cat > "$tmp_unit" <<EOF
[Unit]
Description=Radius Manager API (Control Plane for FreeRADIUS instances)
Documentation=https://github.com/heirro/freeradius-manager/blob/master/docs/SRS-RadiusManagerAPI.md
After=network-online.target mariadb.service freeradius.service
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${BIN_DST} serve
Restart=on-failure
RestartSec=5s

# Environment - override per-host via drop-ins under
# /etc/systemd/system/radius-manager-api.service.d/override.conf
Environment="RM_API_LISTEN=${RM_INSTALL_BIND}"
Environment="RM_API_TOKEN_FILE=${TOKEN_FILE}"
Environment="RM_API_FREERADIUS_DIR=/etc/freeradius/3.0"
Environment="RM_API_STATE_DIR=${STATE_DIR}"
Environment="RM_API_DB_DSN=root@unix(/var/run/mysqld/mysqld.sock)/"
Environment="RM_API_LOG_FORMAT=json"
Environment="RM_API_AUDIT_LOG=${LOG_DIR}/audit.log"
Environment="RM_API_BOOTSTRAP_REPO=https://github.com/heirro/freeradius-api"
Environment="RM_API_BOOTSTRAP_TEMPLATE_DIR=${STATE_DIR}/freeradius-api-template"
Environment="RM_API_CAPACITY_MAX=50"

# Hardening - same set the source unit ships with.
NoNewPrivileges=yes
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF

if [[ -f "$UNIT_DST" ]] && cmp -s "$tmp_unit" "$UNIT_DST"; then
    ok "$UNIT_DST already up-to-date"
    rm -f "$tmp_unit"
else
    install -m 0644 "$tmp_unit" "$UNIT_DST"
    rm -f "$tmp_unit"
    ok "wrote $UNIT_DST"
fi

# -- Phase 12: enable + start -------------------------------------------------
phase "Enable + start radius-manager-api"
systemctl daemon-reload
systemctl is-enabled --quiet radius-manager-api 2>/dev/null && ok "already enabled" \
    || systemctl enable radius-manager-api
# Always restart so a freshly built binary takes effect.
systemctl restart radius-manager-api
ok "radius-manager-api restarted"

# -- Phase 13: self-test ------------------------------------------------------
phase "Self-test (HTTP health + service status)"
health_url="http://${RM_INSTALL_BIND}/v1/server/health"
code=000
for i in $(seq 1 30); do
    code=$(curl -s -o /tmp/rm-install-health.json -w '%{http_code}' "$health_url" 2>/dev/null || echo "000")
    [[ "$code" == "200" ]] && break
    sleep 1
done
if [[ "$code" == "200" ]]; then
    ok "$health_url returned 200"
else
    journalctl -u radius-manager-api --no-pager -n 30 || true
    fail "health endpoint never returned 200 within 30s (last code: ${code})"
fi
for u in radius-manager-api mariadb freeradius; do
    systemctl is-active --quiet "$u" && ok "$u is active" || warn "$u is NOT active (see 'journalctl -u $u')"
done

# -- Phase 14: summary --------------------------------------------------------
phase "Summary"
cat <<EOF

  ${C_BOLD}radius-manager-api install complete${C_RESET}

  API URL          http://${RM_INSTALL_BIND}/
  Health           http://${RM_INSTALL_BIND}/v1/server/health
  Token file       ${TOKEN_FILE}      (0600 root:root)
  Binary           ${BIN_DST}
  Source           ${SOURCE_DIR}
  State dir        ${STATE_DIR}
  Audit log        ${LOG_DIR}/audit.log

  Useful commands:
    sudo cat ${TOKEN_FILE}                                 # reveal API token
    systemctl status radius-manager-api --no-pager
    journalctl -u radius-manager-api -f
    curl -H "Authorization: Bearer \$(sudo cat ${TOKEN_FILE})" \\
         http://${RM_INSTALL_BIND}/v1/server/info | jq

  Next step: create your first instance with
    POST http://${RM_INSTALL_BIND}/v1/instances/  body: {"name":"yourname"}

EOF
