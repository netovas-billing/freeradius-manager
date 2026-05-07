#!/usr/bin/env bash
# scripts/test-install.sh - Boot a systemd-enabled Ubuntu 22.04 container,
# run install.sh inside it, validate, then re-run for idempotency.
#
# REQUIRES Docker on the host. Uses --privileged so PID 1 systemd boots
# correctly inside the container - this is for TEST ONLY, not production.
#
# Usage: ./scripts/test-install.sh
# Exits non-zero on any failure.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# geerlingguy/docker-ubuntu2204-ansible publishes both amd64 and arm64 images,
# so this works on both x86 hosts and Apple Silicon. It runs real /sbin/init.
IMAGE="${RM_TEST_IMAGE:-geerlingguy/docker-ubuntu2204-ansible:latest}"
NAME="rm-install-test-$$"

if [[ -t 1 ]]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; B=""; N=""
fi
log()    { printf "%s[test]%s %s\n" "$B" "$N" "$*"; }
ok()     { printf "%s[ok]%s   %s\n" "$G" "$N" "$*"; }
fail()   { printf "%s[FAIL]%s %s\n" "$R" "$N" "$*" >&2; exit 1; }

cleanup() {
    log "tearing down container $NAME"
    docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || fail "docker not on PATH"
docker info >/dev/null 2>&1 || fail "docker daemon not reachable"

log "pulling image $IMAGE"
docker pull "$IMAGE" >/dev/null

# Mount the repo read-write because install.sh writes ./bin and may pull
# git updates. systemd-in-docker requires --privileged + cgroup mount.
log "starting container $NAME (--privileged, repo mounted at /opt/freeradius-manager-src)"
# /tmp must NOT be noexec - apt postinst scripts and `go test` execute from
# /tmp. Real Ubuntu VMs don't mount /tmp noexec by default, so we mirror that.
docker run -d --name "$NAME" --privileged \
    --tmpfs /tmp:exec --tmpfs /run --tmpfs /run/lock \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw --cgroupns=host \
    -v "$REPO_DIR:/opt/freeradius-manager-src" \
    "$IMAGE" >/dev/null

log "waiting for container systemd to boot"
for i in $(seq 1 60); do
    state=$(docker exec "$NAME" systemctl is-system-running 2>&1 || true)
    case "$state" in
        running|degraded|starting) break ;;
    esac
    sleep 1
    [[ "$i" -eq 60 ]] && fail "container systemd never booted (last: $state)"
done
ok "container systemd ready (state=$state)"

# ---- First install (the real test). The install.sh inside the mounted repo
#      will detect it's running from a checkout (go.mod next to script) and
#      will skip the clone path entirely.
log "running install.sh inside container (this takes ~5-10min on first run)"
T0=$(date +%s)
docker exec -e DEBIAN_FRONTEND=noninteractive "$NAME" \
    bash -c 'cd /opt/freeradius-manager-src && bash install.sh' || \
    fail "install.sh failed"
T1=$(date +%s)
ok "install.sh completed in $((T1 - T0))s"

# ---- Validation
log "validating installed state"
docker exec "$NAME" systemctl is-active radius-manager-api >/dev/null \
    || fail "radius-manager-api not active"
ok "radius-manager-api is active"

docker exec "$NAME" systemctl is-active mariadb >/dev/null \
    || fail "mariadb not active"
ok "mariadb is active"

docker exec "$NAME" systemctl is-active freeradius >/dev/null \
    || fail "freeradius not active"
ok "freeradius is active"

# Health endpoint - unauthenticated by design.
docker exec "$NAME" bash -c 'for i in $(seq 1 20); do
    out=$(curl -fsS http://127.0.0.1:9000/v1/server/health 2>/dev/null) && \
        echo "$out" && exit 0
    sleep 1
done; exit 1' | grep -q healthy || fail "/v1/server/health did not return healthy"
ok "/v1/server/health returns healthy"

# Binary version smoke check.
docker exec "$NAME" /usr/local/bin/radius-manager-api version | grep -q "radius-manager-api" \
    || fail "binary 'version' command broken"
ok "radius-manager-api version OK"

# Token file perms.
perm=$(docker exec "$NAME" stat -c '%a %U:%G' /etc/radius-manager-api/token)
[[ "$perm" == "600 root:root" ]] || fail "token file perms wrong: $perm"
ok "token file perms = $perm"

# ---- Idempotency: re-run must complete fast with no errors.
log "re-running install.sh (idempotency check, must finish in <30s)"
T0=$(date +%s)
docker exec "$NAME" bash -c 'cd /opt/freeradius-manager-src && bash install.sh' \
    || fail "second install.sh run failed"
T1=$(date +%s)
DUR=$((T1 - T0))
[[ "$DUR" -le 30 ]] || fail "second run took ${DUR}s (>30s budget)"
ok "second run completed in ${DUR}s"

# ---- Stats for the report
log "post-install stats:"
docker exec "$NAME" du -sh /opt/freeradius-manager-src 2>/dev/null || true
docker exec "$NAME" du -sh /usr/local/bin/radius-manager-api 2>/dev/null || true
docker exec "$NAME" systemctl status radius-manager-api --no-pager 2>&1 | tail -5 || true

printf "\n%s================================================%s\n" "$G" "$N"
printf "%s  ALL TESTS PASSED  (install + idempotency)%s\n" "$G" "$N"
printf "%s================================================%s\n\n" "$G" "$N"
