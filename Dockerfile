# syntax=docker/dockerfile:1.7

# ---------- Stage 1: Build the Go binary ----------
# We pin to bookworm (Debian) instead of alpine because the runtime image
# is debian-slim and matching glibc/libc avoids surprise mismatches with
# CGO-compiled deps (we keep CGO off here, but the alignment is cheap).
FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Cache module downloads in a separate layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the rest of the source.
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# Build a static binary. CGO_ENABLED=0 keeps it portable across libc
# variants and lets us avoid pulling glibc-dev into the builder.
ENV CGO_ENABLED=0
ENV GOFLAGS=-trimpath
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -ldflags='-s -w' -o /out/radius-manager-api ./cmd/radius-manager-api


# ---------- Stage 2: Runtime image ----------
# debian:bookworm-slim is required because freeradius/freeradius-mysql are
# only packaged for glibc-based distros, and this is also the base most
# commonly used in production VMs.
FROM debian:bookworm-slim AS runtime

ARG DEBIAN_FRONTEND=noninteractive

# Single RUN to keep layers compact. We need:
#   freeradius / freeradius-mysql : the RADIUS daemon + SQL backend module.
#   mariadb-client                : entrypoint waits on the MariaDB service.
#   python3 / python3-venv / pip  : per-instance freeradius-api venv setup.
#   git                           : RM-API clones the freeradius-api repo.
#   supervisor                    : option-B PID 1 (no systemd in container).
#   curl, ca-certificates         : healthchecks + outbound HTTPS for git.
#   iproute2                      : `ss`/`ip` for debugging from inside.
#   gosu                          : drop privileges in entrypoint if needed.
#   tini                          : tiny init for clean signal forwarding.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        freeradius \
        freeradius-mysql \
        mariadb-client \
        python3 \
        python3-venv \
        python3-pip \
        git \
        supervisor \
        curl \
        ca-certificates \
        iproute2 \
        gosu \
        tini \
 && rm -rf /var/lib/apt/lists/*

# Pre-create directories the binary writes to.
RUN mkdir -p \
        /etc/radius-manager-api \
        /etc/supervisor/conf.d \
        /var/lib/radius-manager-api \
        /var/log/radius-manager-api \
        /var/log/supervisor \
        /run/supervisor \
 && chmod 0700 /etc/radius-manager-api \
 && chmod 0755 /etc/supervisor/conf.d \
 && chmod 0755 /var/log/radius-manager-api

# Empty port registry, owned by root:freerad so the daemon can read it
# but only root (rm-api) writes.
RUN touch /etc/freeradius/3.0/.port_registry \
 && chown root:freerad /etc/freeradius/3.0/.port_registry \
 && chmod 0664 /etc/freeradius/3.0/.port_registry

# Keep the stock default + inner-tunnel sites enabled. They listen on
# 1812/1813 (not in the 10000+ range used by per-instance virtual
# servers) and are required for the eap module to instantiate cleanly.
# Per-instance virtual servers and module configs are added at runtime
# by RM-API and coexist alongside the defaults.

# Copy in the supervisord master config + entrypoint + binary.
COPY deployments/docker/supervisord.conf /etc/supervisor/supervisord.conf
COPY entrypoint.sh /entrypoint.sh
COPY --from=builder /out/radius-manager-api /usr/local/bin/radius-manager-api
RUN chmod 0755 /entrypoint.sh /usr/local/bin/radius-manager-api

# Sensible defaults that the entrypoint will override when the user has
# explicitly set them.
ENV RM_API_LISTEN=0.0.0.0:9000 \
    RM_API_TOKEN_FILE=/etc/radius-manager-api/token \
    RM_API_FREERADIUS_DIR=/etc/freeradius/3.0 \
    RM_API_STATE_DIR=/var/lib/radius-manager-api \
    RM_API_AUDIT_LOG=/var/log/radius-manager-api/audit.log \
    RM_API_API_DIR_BASE=/var/lib/radius-manager-api/instances \
    RM_API_LOG_FORMAT=text \
    RM_API_SYSTEMD_BACKEND=supervisord

EXPOSE 9000 1812/udp 1813/udp

# tini reaps zombies / forwards signals; supervisord then reaps its own
# children. tini -> supervisord -> {freeradius, rm-api, per-instance APIs}.
ENTRYPOINT ["/usr/bin/tini", "--", "/entrypoint.sh"]
CMD ["serve"]
