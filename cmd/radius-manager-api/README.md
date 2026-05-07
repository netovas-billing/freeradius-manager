# radius-manager-api

HTTP control plane for FreeRADIUS instance lifecycle on a single RADIUS Server VM.

This service is the Go-native counterpart to `radius-manager.sh` — both
coexist and share the same on-disk state files. See
[`docs/SRS-RadiusManagerAPI.md`](../../docs/SRS-RadiusManagerAPI.md) for
the full specification.

## Status

**v0.1.2** — Phase 2 (create/delete), Phase 3 (lifecycle: start/stop/restart/test),
and Phase 4 (audit log + OpenAPI export) of the rollout in SRS §13 are
implemented and unit-tested. End-to-end validation against real
Linux/MariaDB/FreeRADIUS is still pending — see SRS §17.

Working end-to-end (with a properly provisioned Linux + MariaDB + FreeRADIUS host):
- `GET /v1/server/health` — no auth, used by ERP for liveness polling
- `GET /v1/openapi.yaml` — public, returns the embedded OpenAPI 3 spec
- `GET /v1/server/info` — hostname, FreeRADIUS / MariaDB versions, capacity, count
- `GET /v1/instances` — list all instances on this server
- `GET /v1/instances/{name}` — single instance; pass `?include_secrets=true` to get plaintext DB and Swagger passwords
- `POST /v1/instances` — full create flow (port allocation under `flock(2)`, MariaDB DB + user + GRANT, schema import, FreeRADIUS template render, systemctl reload, `<name>-api.service` enable+start, `.instance_<name>` write). Failures roll back via `defer` cleanup chain.
- `DELETE /v1/instances/{name}` — full reverse-order teardown; idempotent (returns 200 with `already_deleted: true` if the instance was already gone)
- `POST /v1/instances/{name}/start` — delegates to `Systemctl.Start`
- `POST /v1/instances/{name}/stop` — delegates to `Systemctl.Stop`
- `POST /v1/instances/{name}/restart` — delegates to `Systemctl.Restart`
- `POST /v1/instances/{name}/test` — probes UDP RADIUS ports via `net.ListenPacket` and the API port via `net.Dial`

Deferred to v0.2.0:
- freeradius-api repo bootstrap per instance (`git clone` + `python -m venv` + `pip install`). Currently Phase 2 writes the `<name>-api.service` systemd unit but expects the API directory to be pre-provisioned (manually or via the existing bash `radius-manager.sh create`).
- `autoclearzombie.sh` + `autobackups3.sh` cron job management — kept on the bash side via `update.sh`; RM-API intentionally does not touch crontab (SRS RM-Q03).

Cannot be validated from a macOS dev box (see SRS §17 RM-V01..RM-V06):
- Real `systemctl` interaction with the generated `<name>-api.service`
- FreeRADIUS accepting the rendered `mods-available/sql_<name>` + virtual server configs
- Real MariaDB `CREATE DATABASE` / `GRANT` / schema import (currently `sqlmock` in unit tests)
- `flock(2)` interop on a shared `.port_registry` file with concurrent bash + Go callers
- End-to-end onboarding: ERP → `POST /v1/instances` → instance up → freeradius-api at returned URL → NAS register → PPPoE auth

## Quick start (local dev)

```bash
# build
go build -o /tmp/rm-api ./cmd/radius-manager-api

# point at any directory to act as the FreeRADIUS dir
mkdir -p /tmp/rm-api-dev
RM_API_LISTEN=127.0.0.1:9000 \
RM_API_TOKEN=devtoken \
RM_API_FREERADIUS_DIR=/tmp/rm-api-dev \
  /tmp/rm-api serve

# in another shell:
curl http://127.0.0.1:9000/v1/server/health
curl -H "Authorization: Bearer devtoken" http://127.0.0.1:9000/v1/server/info
curl -H "Authorization: Bearer devtoken" http://127.0.0.1:9000/v1/instances/
```

To exercise the read path with a fake instance:

```bash
cat > /tmp/rm-api-dev/.instance_demo <<'EOF'
ADMIN_USERNAME=demo
DB_HOST=localhost
DB_PORT=3306
DB_NAME=demo
DB_USER=demo
DB_PASS=secret
AUTH_PORT=12345
ACCT_PORT=12346
COA_PORT=14345
INNER_PORT=17345
API_PORT=8100
SWAGGER_USERNAME=admin
SWAGGER_PASSWORD=swagger
WEB_API_URL=http://127.0.0.1:8100/docs
CREATED=2026-05-07T10:00:00Z
EOF

curl -H "Authorization: Bearer devtoken" \
  "http://127.0.0.1:9000/v1/instances/demo?include_secrets=true"
```

## Testing

### Unit tests (no infrastructure required)

```bash
go test ./...                    # ~70 tests, runs in <2s
go test -race ./...              # same with race detector
```

### Integration tests against real MariaDB

Two paths, both validate the same `DBManager` SQL contract:

#### Path A — testcontainers-go (auto-spawn)

```bash
go test -tags=integration -timeout=180s ./internal/manager/...
```

Spawns a fresh `mariadb:10.11` container per test, cleans it up at the end.
Requires Docker daemon running. ~13s for the full suite (3 tests, each
spinning up its own container — slower but maximally isolated).

#### Path B — docker-compose (long-running container)

For interactive debugging or repeated runs:

```bash
docker compose -f docker-compose.test.yml up -d
RM_TEST_DB_DSN='root:testrootpw@tcp(127.0.0.1:13306)/' \
  go test -tags=integration -timeout=60s ./internal/manager/...
docker compose -f docker-compose.test.yml down -v
```

Tests reuse the long-running container (~0.4s per run), and you can
inspect it with `mysql -h 127.0.0.1 -P 13306 -u root -ptestrootpw`
between runs.

The integration test detects `RM_TEST_DB_DSN` and skips spawning its own
container when set.

## Local end-to-end with Docker

The repo ships a Docker stack at `docker-compose.dev.yml` that brings up
RM-API + FreeRADIUS + MariaDB in two containers, so you can exercise the
full create/delete flow on macOS or any non-Linux host without a VM.

**Why this exists**: `RealSystemctl` shells out to `systemctl(1)`, which
isn't available in vanilla containers. The dev image runs **supervisord
as PID 1** and ships a `SupervisordSystemctl` backend
(`internal/system/supervisord.go`) that translates the same systemd unit
content RM-API generates into supervisord program configs at runtime.
Production Linux deploys still use real `systemctl` — backend selection
is via `RM_API_SYSTEMD_BACKEND` (`systemd` default, `supervisord` for
this dev stack).

### Pre-requisite

Docker Desktop / Docker Engine with `docker compose` v2.

### One-shot smoke test

```bash
docker compose -f docker-compose.dev.yml up -d --build
sleep 30
TOKEN=$(docker compose -f docker-compose.dev.yml exec -T rm-api cat /etc/radius-manager-api/token)

# Create an instance — returns 201 with credentials.
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"demo"}' http://127.0.0.1:9000/v1/instances/

# Read the freshly-created instance — shows ports + status.
curl -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:9000/v1/instances/demo?include_secrets=true"

# Tear down (removes containers AND named volumes).
docker compose -f docker-compose.dev.yml down -v
```

A successful create returns JSON with `name`, `status: "running"`,
`ports.{auth,acct,coa,inner,api}`, `database.{host,port,name,user,password}`,
`swagger.{username,password}`, and an `api_url`. The freshly-created
freeradius-api process is supervised by supervisord inside the container.

### Inspecting the running stack

```bash
# All three programs (freeradius + rm-api + per-instance API).
docker compose -f docker-compose.dev.yml exec rm-api supervisorctl status

# Live RM-API logs.
docker compose -f docker-compose.dev.yml logs -f rm-api

# Inside the container: per-instance config files.
docker compose -f docker-compose.dev.yml exec rm-api ls /etc/supervisor/conf.d/
docker compose -f docker-compose.dev.yml exec rm-api ls /etc/freeradius/3.0/sites-enabled/

# MariaDB shell (host port 13307 to avoid clashing with docker-compose.test.yml).
mariadb -h 127.0.0.1 -P 13307 -u root -pdevrootpw
```

### Tear down + clean volumes

```bash
docker compose -f docker-compose.dev.yml down -v
```

`-v` is required to remove the named volumes (token, freeradius dir,
state dir, MariaDB data) — without it, the next `up` reuses any
previously-bootstrapped state.

### Important: this is a dev stack only

The supervisord backend is selected purely so the same RM-API binary can
run inside a container without systemd. Production RADIUS Server VMs
should leave `RM_API_SYSTEMD_BACKEND` unset (defaults to `systemd`) so
the per-instance `<name>-api.service` units are real systemd units
managed by `systemctl`, integrated into journald, etc.

The `docker-compose.dev.yml` stack also wires the per-instance DB host
to the in-network MariaDB service via `RM_API_INSTANCE_DB_HOST=mariadb`.
On a single-VM production install this stays `localhost` (the default).

## Production deploy (RADIUS Server VM)

```bash
# build for the target (assuming linux/amd64)
GOOS=linux GOARCH=amd64 go build -o radius-manager-api ./cmd/radius-manager-api

# scp to server then run installer (as root)
scp radius-manager-api deployments/install.sh deployments/systemd \
    root@<radius-vm>:/tmp/

ssh root@<radius-vm> '
  mkdir -p /tmp/install/deployments
  mv /tmp/install.sh /tmp/install/deployments/
  mv /tmp/systemd /tmp/install/deployments/
  mv /tmp/radius-manager-api /tmp/install/
  cd /tmp/install && ./deployments/install.sh --listen 10.254.252.2:9000
'
```

The installer prints the API token at the end — copy this into ERP's
`radius_servers.api_token` for that VM.

## Layout

```
cmd/radius-manager-api/   # entry point: serve, init, version
internal/api/             # chi router + HTTP handlers
internal/manager/         # core lifecycle library (the equivalent of radius-manager.sh)
internal/config/          # env loading
internal/templates/       # FreeRADIUS config templates (embed.FS)
internal/schema/          # MariaDB migration files (embed.FS)
pkg/types/                # shared wire types — importable by ERP
deployments/systemd/      # systemd unit
deployments/install.sh    # one-shot installer
```

## Audit log

Mutating requests (`POST`, `PUT`, `PATCH`, `DELETE`) emit one JSON line per
request to the audit writer (configurable; defaults to a file under the
RM-API state dir, falls back to stderr if the file cannot be opened).
Read-only requests (`GET`) are not audited — use the standard request log
for those.

Each audit record contains:

| Field | Type | Description |
|-------|------|-------------|
| `ts` | RFC3339 string | Timestamp at request completion |
| `subject` | string | Token subject populated by the auth middleware (token id, not the token itself) |
| `method` | string | HTTP method (`POST` / `DELETE` / etc.) |
| `path` | string | Request path, e.g. `/v1/instances/mitra_x/start` |
| `instance` | string | Instance name extracted from the URL when applicable, else `""` |
| `status` | int | HTTP status code returned |
| `dur_ms` | int | Wall-clock duration of the handler in milliseconds |

Example line:

```json
{"ts":"2026-05-07T10:23:46Z","subject":"erp-prod","method":"POST","path":"/v1/instances","instance":"mitra_x","status":201,"dur_ms":48213}
```

The log is append-only and intended to be rotated by `logrotate` on the
host (see SRS RM-S08, RM-S09 for compliance / retention requirements).

## Coexistence with `radius-manager.sh`

Both tools read/write the same `.instance_<name>` files (format in
PRD Lampiran A) and share the same port registry. `flock(2)` ensures
no race condition when both try to allocate ports.

As of v0.1.2, RM-API and the bash script are functionally equivalent
for create / delete / lifecycle. The bash script stays in the repo as
a manual ops fallback and as the canonical home for cron-driven
maintenance (`autoclearzombie.sh`, `autobackups3.sh`) which RM-API
does not replicate.
