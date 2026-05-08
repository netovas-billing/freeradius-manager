# SRS Extension — Radius Manager API (Control Plane)

| Field | Value |
|-------|-------|
| Versi | 0.3.0 |
| Tanggal | 2026-05-08 |
| Status | 0.3.0 — Adds per-instance maintenance timers (§20) backed by the new `system.Maintenance` interface (RealMaintenance via systemd `.timer + .service`, SupervisordMaintenance via sleep-loop program). RM-Q03 is now resolved: autoclearzombie + autobackups3 are scheduled by RM-API at CreateInstance time instead of by `update.sh`. v0.2.0 Docker dev stack remains intact. |
| Parent | `docs/PRD.md` v1.0.0, `docs/SRS.md` v1.0.0 |
| Owner | Tim Network Operations + Tim Platform |
| Bahasa Implementasi | Go (1.22+) |
| Lokasi Kode | Mono-repo: `cmd/radius-manager-api/` + `internal/` + `pkg/types/` |

> **Catatan**: Dokumen ini adalah **extension** dari SRS v1.0.0. SRS lama tetap berlaku untuk kontrak `Billing/ERP → freeradius-api` (data plane per-instance). Dokumen ini menambahkan layer baru: **Radius Manager API** sebagai control plane untuk lifecycle instance.

---

## 1. Tujuan Dokumen

Mendefinisikan spec untuk **Radius Manager API** (selanjutnya: `RM-API`) — service Go native yang berjalan di setiap RADIUS Server VM, menyediakan HTTP API untuk operasi lifecycle instance (create, delete, info, start, stop, list).

Service ini menjadi **control plane** yang dipanggil oleh ERP, menggantikan/melengkapi pemanggilan manual `bash radius-manager.sh ...` via SSH.

### 1.1. Mengapa Dokumen Ini Diperlukan

PRD v1.0.0 §3 menulis "Bukan mengganti `radius-manager.sh`" sebagai Non-Goal. Konsekuensinya: ERP yang ingin onboard Mitra baru **harus minta ISP ops untuk SSH manual**. Ini bottleneck nyata yang menghalangi visi "Cloud RADIUS multi-server" yang ditargetkan.

Dokumen ini menjawab gap tersebut **tanpa menghapus** `radius-manager.sh` — bash script tetap ada sebagai fallback dan tool manual ops. Yang ditambah: API+library Go yang berbagi state file dengan bash script (`.instance_<nama>`, port registry).

### 1.2. Scope

**In-scope:**
- HTTP API kontrak antara ERP ↔ RM-API.
- Library Go internal yang reimplementasi logic `radius-manager.sh` secara native (tanpa shell-out untuk operasi inti).
- Multi-server registry di sisi ERP (control plane awareness).
- Onboarding lifecycle Mitra (state machine).
- Coexistence dengan `radius-manager.sh` existing.

**Out-of-scope** (di-cover dokumen lain atau roadmap berikutnya):
- VPN provisioning untuk Mitra (akan ada di SRS-VPNProvisioning, jika dibuat).
- Integrasi billing → freeradius-api per-instance (sudah di SRS.md v1.0.0).
- Web UI ERP (frontend concern).
- Migrasi data instance lama → format baru (asumsi: format `.instance_<nama>` tetap).

---

## 2. Konteks Sistem

### 2.1. Posisi RM-API dalam Arsitektur

```
                     ┌────────────────────────────────────────┐
                     │              ERP (Golang)              │
                     │        Control Plane Orchestrator      │
                     │                                        │
                     │  ┌──────────────────────────────────┐  │
                     │  │ Registry: radius_servers         │  │ ← Level 1 (NEW)
                     │  │   - api_url, api_token, capacity │  │
                     │  └────────────┬─────────────────────┘  │
                     │               │                         │
                     │  ┌────────────▼─────────────────────┐  │
                     │  │ Placement Logic                   │  │ ← Level 2 (NEW)
                     │  └────────────┬─────────────────────┘  │
                     │               │                         │
                     │  ┌────────────▼─────────────────────┐  │
                     │  │ Registry: radius_instances       │  │ ← Level 3 (existing SRS §6)
                     │  │   - swagger_creds, ports         │  │
                     │  └──────────────────────────────────┘  │
                     └────┬───────────────────────┬───────────┘
                          │                       │
                control   │              data     │
                 plane    │             plane     │
                          │                       │
                          ▼                       ▼
         ┌───────────────────────────────────────────────────┐
         │              RADIUS Server VM #N                   │
         │                                                    │
         │  ┌──────────────────┐                              │
         │  │ RM-API (Go)      │ :9000                        │
         │  │ - HTTP server    │                              │
         │  │ - Library calls  │ ← THIS DOCUMENT             │
         │  └────┬─────────────┘                              │
         │       │                                            │
         │       │ uses pkg/manager/ (Go library)             │
         │       │ (NO shell-out for core ops)                │
         │       │                                            │
         │       ├──► MariaDB (CRUD database, schema import)  │
         │       ├──► /etc/freeradius/3.0/ (write configs)    │
         │       ├──► systemctl (reload freeradius)           │
         │       └──► spawn freeradius-api process            │
         │                                                    │
         │  ┌──────────────────┐                              │
         │  │ radius-manager.sh│ ← existing, tetap berfungsi │
         │  │ (bash, manual)   │   sebagai fallback ops       │
         │  └──────────────────┘                              │
         │                                                    │
         │  ┌──────────────────┐ ┌──────────────────┐         │
         │  │ freeradius-api   │ │ freeradius-api   │  :8100+ │
         │  │ instance: A      │ │ instance: B      │         │
         │  └──────────────────┘ └──────────────────┘         │
         └───────────────────────────────────────────────────┘
```

### 2.2. Hubungan dengan Komponen Existing

| Komponen | Status | Hubungan dengan RM-API |
|----------|--------|------------------------|
| `radius-manager.sh` | Existing, tetap dipakai | Fallback manual; **tidak dipanggil** oleh RM-API. Membaca/menulis state file yang sama. |
| `freeradius-api` per-instance | Existing | RM-API yang **men-spawn** process freeradius-api saat create instance. Setelah running, ERP berkomunikasi langsung (tidak via RM-API). |
| `MariaDB` | Existing | RM-API CRUD database via `database/sql` Go (mysql driver) — bukan shell-out ke `mysql` client. |
| `FreeRADIUS service` | Existing | RM-API write config files + `systemctl reload freeradius` (shell-out untuk service control adalah OK). |
| `.instance_<nama>` file | Existing format | **Format file tidak berubah**. RM-API dan bash script keduanya baca-tulis dengan locking. |
| Port registry file | Existing format | Same — locking via `flock(2)`. |

### 2.3. Tiga Level Registry di ERP

Implementasi ERP perlu maintain tiga registry tingkat:

| Level | Tabel | Isi | Sumber data |
|-------|-------|-----|-------------|
| **Level 1** | `radius_servers` | Daftar RADIUS Server VM yang ada (api_url, token, capacity, region) | Manual input ISP admin |
| **Level 2** | (logic, bukan tabel) | Placement decision: "Mitra baru → server mana?" | Computed dari Level 1 + business rule |
| **Level 3** | `radius_instances` (sudah di SRS §6) | Per-instance API credentials & ports untuk billing ops | Output `POST /instances` ke RM-API |

`radius_instances.server_id` adalah FK ke `radius_servers.id`. Setiap instance harus tahu di server mana dia berada.

---

## 3. Onboarding Lifecycle (State Machine Mitra)

Onboarding Mitra adalah dependency tree 4-step. Sebelumnya manual; setelah RM-API ada, Step 3 dan Step 4 jadi otomatis.

```
    [Mitra Created in ERP]
            │
            ▼
    ┌───────────────────┐    Manual / VPN provisioner
    │ Step 1: VPN Acct  │    (out-of-scope dokumen ini)
    └────────┬──────────┘
             │ Mitra dapat VPN cred + IP private
             ▼
    ┌───────────────────┐    Manual oleh Mitra (Mikrotik config)
    │ Step 2: Connect   │
    └────────┬──────────┘
             │ Tunnel up, NAS reachable di IP private
             ▼
    ┌───────────────────┐    OTOMATIS via RM-API
    │ Step 3: Create    │    POST /instances → output credentials
    │      Instance     │    Dipicu otomatis oleh ERP
    └────────┬──────────┘
             │ FreeRADIUS instance ready, credentials di Level 3 registry
             ▼
    ┌───────────────────┐    OTOMATIS via freeradius-api per-instance
    │ Step 4: Register  │    POST /nas/ ke instance baru
    │      NAS          │    Dipicu otomatis oleh ERP setelah Step 3 success
    └────────┬──────────┘
             │ NAS terdaftar dengan IP VPN + secret
             ▼
        [Mitra ACTIVE]
        Siap provision user PPPoE
```

### 3.1. Status Transitions (untuk ERP)

| Status | Trigger transition | Aksi |
|--------|-------------------|------|
| `created` | Mitra entry baru di ERP | (none) |
| `vpn_pending` | Admin trigger VPN provision | (out-of-scope) |
| `vpn_ready` | VPN credentials returned, IP private alokasi | Mitra notified untuk config Mikrotik |
| `connectivity_verified` | Ping test ke IP Mitra success | Trigger Step 3 |
| `instance_provisioning` | `POST /instances` sent ke RM-API | Wait response (sync ~60s) atau poll job |
| `instance_ready` | RM-API return 201 + credentials | Save ke `radius_instances`. Trigger Step 4. |
| `nas_registering` | `POST /nas/` ke freeradius-api | Wait response |
| `active` | NAS registered | Mitra ready |
| `failed_step_N` | Error di salah satu step | Mark untuk manual recovery oleh ops |

---

## 4. Functional Requirements

### 4.1. Endpoint Inventory

| ID | Method | Path | Deskripsi |
|----|--------|------|-----------|
| **RM-F01** | `POST` | `/v1/instances` | Create instance baru (DB + config + freeradius-api spawn). |
| **RM-F02** | `GET` | `/v1/instances` | List semua instance di server ini (nama, status, port). |
| **RM-F03** | `GET` | `/v1/instances/{name}` | Info detail instance (credentials, port, status). |
| **RM-F04** | `DELETE` | `/v1/instances/{name}` | Delete instance (stop process, drop DB optional, hapus config). |
| **RM-F05** | `POST` | `/v1/instances/{name}/start` | Start instance (enable + start freeradius-api process). |
| **RM-F06** | `POST` | `/v1/instances/{name}/stop` | Stop instance (stop freeradius-api process, disable). |
| **RM-F07** | `POST` | `/v1/instances/{name}/restart` | Restart freeradius-api process untuk instance ini. |
| **RM-F08** | `POST` | `/v1/instances/{name}/test` | Test instance (port listen check + Access-Request dummy). |
| **RM-F09** | `GET` | `/v1/server/info` | Info server: hostname, RADIUS version, capacity, current count, uptime. |
| **RM-F10** | `GET` | `/v1/server/health` | Health check (untuk ERP polling). |

### 4.2. Detail Endpoint

#### 4.2.1. RM-F01 — Create Instance

**Request:**

```http
POST /v1/instances HTTP/1.1
Host: 10.254.252.2:9000
Authorization: Bearer <api_token>
Content-Type: application/json

{
  "name": "mitra_x",
  "db_password": null,
  "with_db": true
}
```

| Field | Type | Required | Default | Catatan |
|-------|------|----------|---------|---------|
| `name` | string | ✅ | — | Alphanumeric + underscore, max 32 char. Reserved: `default`, `inner-tunnel`. |
| `db_password` | string \| null | — | auto-generate | Kalau null, RM-API generate random 24-char. |
| `with_db` | bool | — | `true` | Kalau false, asumsi DB sudah exist (recovery scenario). |

**Response (success):**

```http
HTTP/1.1 201 Created
Content-Type: application/json

{
  "name": "mitra_x",
  "status": "running",
  "ports": {
    "auth": 18234,
    "acct": 18235,
    "coa": 20234,
    "inner": 23234,
    "api": 8112
  },
  "database": {
    "host": "localhost",
    "port": 3306,
    "name": "mitra_x",
    "user": "mitra_x",
    "password": "<generated_24_chars>"
  },
  "swagger": {
    "username": "admin",
    "password": "<generated>"
  },
  "api_url": "http://10.254.252.2:8112",
  "swagger_url": "http://10.254.252.2:8112/docs",
  "created_at": "2026-05-07T10:23:45Z"
}
```

**Response (error — name conflict):**

```http
HTTP/1.1 409 Conflict
Content-Type: application/json

{
  "error": "instance_exists",
  "message": "Instance 'mitra_x' already exists",
  "instance": "mitra_x"
}
```

**Behavior:**
- Operasi sync (HTTP request blocking sampai selesai). Estimated 30-60s.
- ERP HTTP client timeout WAJIB ≥ 90s.
- Jika gagal di tengah, RM-API harus rollback (lihat §10).

#### 4.2.2. RM-F03 — Get Instance Info

**Request:**

```http
GET /v1/instances/mitra_x HTTP/1.1
Authorization: Bearer <api_token>
```

**Response:**

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "name": "mitra_x",
  "status": "running",
  "enabled": true,
  "ports": {
    "auth":   {"port": 18234, "listening": true},
    "acct":   {"port": 18235, "listening": true},
    "coa":    {"port": 20234, "listening": true},
    "api":    {"port": 8112,  "listening": true, "process_alive": true}
  },
  "database": {
    "host": "localhost",
    "port": 3306,
    "name": "mitra_x",
    "user": "mitra_x",
    "password_known": true
  },
  "swagger": {
    "username": "admin",
    "password_known": true
  },
  "api_url": "http://10.254.252.2:8112",
  "created_at": "2026-05-07T10:23:45Z"
}
```

**Catatan**:
- `password` dikembalikan plaintext **hanya jika** request menyertakan query `?include_secrets=true` dan token berhak. Default: hanya `password_known: bool`.
- ERP **harus** simpan secret hasil `POST /instances` — jangan andalkan GET untuk mengambilnya kembali.

#### 4.2.3. RM-F04 — Delete Instance

**Request:**

```http
DELETE /v1/instances/mitra_x?with_db=true HTTP/1.1
Authorization: Bearer <api_token>
```

| Query param | Default | Catatan |
|-------------|---------|---------|
| `with_db` | `false` | Kalau true, drop database juga. **Destructive** — wajib double-confirm di ERP UI. |

**Response:**

```http
HTTP/1.1 200 OK
{
  "name": "mitra_x",
  "deleted_at": "2026-05-07T11:00:00Z",
  "database_dropped": true
}
```

#### 4.2.4. RM-F09 — Server Info

```http
GET /v1/server/info
Authorization: Bearer <api_token>

→ 200 OK
{
  "hostname": "radius-jkt-01",
  "vpn_ip": "10.254.252.2",
  "freeradius_version": "3.0.26",
  "mariadb_version": "10.11.5",
  "capacity_max": 50,
  "instances_count": 12,
  "uptime_seconds": 345600,
  "rm_api_version": "0.1.0"
}
```

#### 4.2.5. RM-F10 — Health Check

```http
GET /v1/server/health

→ 200 OK
{
  "status": "healthy",
  "checks": {
    "freeradius": "running",
    "mariadb": "reachable",
    "disk_free_gb": 42.5,
    "cpu_load_1m": 0.65
  }
}

→ 503 Service Unavailable (jika ada check gagal)
{
  "status": "degraded",
  "checks": { ... },
  "issues": ["mariadb: connection refused"]
}
```

**Catatan**: `/health` tidak butuh auth (untuk monitoring tools). Endpoint info lain butuh auth.

### 4.3. Common Error Responses

| HTTP | Error code | Kapan |
|------|-----------|-------|
| 400 | `invalid_input` | Validation gagal (nama invalid, dll). |
| 401 | `unauthorized` | Bearer token salah/tidak ada. |
| 404 | `instance_not_found` | Instance tidak exist. |
| 409 | `instance_exists` | Create dengan nama yang sudah ada. |
| 409 | `port_exhausted` | Tidak ada port available untuk allocate. |
| 422 | `dependency_check_failed` | FreeRADIUS service down, MariaDB unreachable. |
| 500 | `internal_error` | Bug atau error tak terduga. Cek logs. |
| 503 | `service_unavailable` | RM-API sedang restart/maintenance. |
| 504 | `operation_timeout` | Operasi melebihi internal timeout (misal MariaDB hang). |

---

## 5. Authentication & Authorization

### 5.1. Authentication Model: Bearer Token (v1)

- **Mekanisme**: HTTP `Authorization: Bearer <token>`.
- **Token**: random 64-char hex, di-generate saat install via `radius-manager-api init` command.
- **Storage di server**: hash bcrypt di `/etc/radius-manager-api/tokens.json` (atau env-var untuk single-token deployment).
- **Storage di ERP**: encrypted di kolom `radius_servers.api_token`.

### 5.2. Token Scopes (v2 — future)

Untuk v1, satu token = full access. Future:

| Scope | Endpoint allowed |
|-------|------------------|
| `instance:read` | GET `/v1/instances`, `/v1/instances/{name}` |
| `instance:write` | POST/DELETE `/v1/instances/*` |
| `server:read` | GET `/v1/server/*` |
| `*` | Semua |

### 5.3. Tidak Ada di v1

- mTLS (deferred ke v2).
- OAuth2 (overkill untuk service-to-service internal).
- IP allowlist (sudah dijaga di network layer via VPN PTP).

---

## 6. Network & Deployment

### 6.1. Listen Configuration

- **Default port**: `9000`.
- **Default bind**: `<vpn_ip>:9000` (bukan `0.0.0.0:9000`) — hanya accept dari private network.
- **Override**: env `RM_API_LISTEN=10.254.252.2:9000`.

### 6.2. Jalur Network ERP → RM-API

ERP harus reach RM-API via jalur private. Opsi:

| Opsi | Setup | Trade-off |
|------|-------|-----------|
| **A. ERP di dalam VPN** ✅ recommended | ERP server jadi node di VPN PTP yang sama | Re-use infra, paling aman |
| **B. ERP via dedicated tunnel** | WireGuard/IPsec terpisah ERP ↔ RADIUS VM | Isolasi lebih baik, kompleksitas lebih tinggi |
| **C. ERP via public + TLS** | RM-API expose port 9000 ke internet dengan TLS | ❌ tidak recommended — surface attack besar |

### 6.3. Systemd Unit

```ini
# /etc/systemd/system/radius-manager-api.service
[Unit]
Description=Radius Manager API (Control Plane)
After=network.target mariadb.service freeradius.service
Wants=mariadb.service

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/radius-manager-api serve
Restart=on-failure
RestartSec=5s
Environment="RM_API_LISTEN=10.254.252.2:9000"
Environment="RM_API_TOKEN_FILE=/etc/radius-manager-api/tokens.json"

[Install]
WantedBy=multi-user.target
```

### 6.4. Installation Flow

```bash
# 1. Build & deploy binary
scp radius-manager-api root@<radius-vm>:/usr/local/bin/

# 2. Init config + generate token
radius-manager-api init --listen 10.254.252.2:9000
# Output:
# [OK] Generated config at /etc/radius-manager-api/config.yaml
# [OK] Generated token: aBc123...XyZ (SAVE THIS — copy ke ERP)
# [OK] Systemd unit installed

# 3. Enable + start
systemctl enable --now radius-manager-api

# 4. Verify
curl http://10.254.252.2:9000/v1/server/health
```

---

## 7. Library Design (`internal/manager/`)

### 7.1. Tujuan

Logic operasi instance di-implementasi sebagai Go package yang reusable. HTTP layer (`internal/api/`) hanya menjadi wrapper tipis di atas library ini. Library bisa juga dipakai oleh tool CLI Go masa depan (jika `radius-manager.sh` di-deprecate).

### 7.2. Public Interface

```go
package manager

import "context"

type Manager interface {
    CreateInstance(ctx context.Context, req CreateInstanceRequest) (*Instance, error)
    DeleteInstance(ctx context.Context, name string, withDB bool) error
    GetInstance(ctx context.Context, name string) (*Instance, error)
    ListInstances(ctx context.Context) ([]Instance, error)
    StartInstance(ctx context.Context, name string) error
    StopInstance(ctx context.Context, name string) error
    RestartInstance(ctx context.Context, name string) error
    TestInstance(ctx context.Context, name string) (*TestResult, error)
    ServerInfo(ctx context.Context) (*ServerInfo, error)
    HealthCheck(ctx context.Context) (*Health, error)
}

type Instance struct {
    Name      string
    Status    InstanceStatus  // "running", "stopped", "error"
    Enabled   bool
    Ports     Ports
    Database  Database
    Swagger   Credentials
    APIURL    string
    CreatedAt time.Time
}
```

### 7.3. Sub-package Layout

```
internal/manager/
├── manager.go          // Manager interface + impl
├── instance.go         // CreateInstance, DeleteInstance core logic
├── database.go         // CreateDB, DropDB, ImportSchema (database/sql)
├── ports.go            // FindAvailablePort, PortRegistry (flock)
├── freeradius.go       // WriteConfig, ReloadService (templates + systemctl)
├── apiprocess.go       // Spawn freeradius-api uvicorn, manage PID
├── statefile.go        // Read/write .instance_<nama> file
├── lock.go             // flock(2) wrapper
└── manager_test.go     // unit tests dengan testify
```

### 7.4. Templating untuk Konfigurasi FreeRADIUS

Pakai `text/template` Go. File template di `internal/templates/*.tmpl`:

```
internal/templates/
├── sql_module.tmpl         // /etc/freeradius/3.0/mods-available/sql_<nama>
├── eap_module.tmpl
├── inner_tunnel.tmpl
└── virtual_server.tmpl
```

Contoh `sql_module.tmpl`:

```
sql {
    driver = "rlm_sql_mysql"
    dialect = "mysql"
    server = "{{ .DBHost }}"
    port = {{ .DBPort }}
    login = "{{ .DBUser }}"
    password = "{{ .DBPass }}"
    radius_db = "{{ .DBName }}"
    # ... etc
}
```

### 7.5. Shell-out: Allowed vs Not Allowed

| Operation | Native Go | Shell-out |
|-----------|-----------|-----------|
| MariaDB CRUD database | ✅ via `database/sql` | ❌ |
| MariaDB import schema | ✅ via `database/sql` (parse `.sql` file) | ❌ |
| Write FreeRADIUS config files | ✅ via `text/template` + `os.WriteFile` | ❌ |
| Random password generation | ✅ via `crypto/rand` | ❌ |
| systemctl reload/restart freeradius | — | ✅ `os/exec` (tidak ada Go API untuk systemd) |
| Spawn freeradius-api process | — | ✅ `os/exec` (atau systemd unit per-instance) |
| `radclient` untuk test/disconnect | — | ✅ `os/exec` (tool eksternal) |
| Check port listen | ✅ via `net.Listen` test | (atau shell-out `ss` jika perlu) |

---

## 8. State Management & Coexistence

### 8.1. Shared State Files

RM-API dan `radius-manager.sh` **wajib menggunakan format file yang sama**:

| File | Format | Locking |
|------|--------|---------|
| `/etc/freeradius/3.0/.instance_<nama>` | KEY=VALUE shell-style | `flock(2)` |
| Port registry (e.g., `/var/lib/radius-manager/ports.txt`) | satu port per baris | `flock(2)` |

### 8.2. Format `.instance_<nama>` (Tetap Sama dengan PRD Lampiran A)

```
ADMIN_USERNAME=mitra_x
DB_HOST=localhost
DB_PORT=3306
DB_NAME=mitra_x
DB_USER=mitra_x
DB_PASS=<generated>
AUTH_PORT=18234
ACCT_PORT=18235
COA_PORT=20234
INNER_PORT=23234
API_PORT=8112
SWAGGER_USERNAME=admin
SWAGGER_PASSWORD=<generated>
WEB_API_URL=http://10.254.252.2:8112/docs
CREATED=2026-05-07T10:23:45Z
```

Go parser membaca file ini dengan strict KEY=VALUE format (tidak eval shell). Ini menjamin compatibility dengan bash script.

### 8.3. Locking Strategy

- **Port allocation**: WAJIB `flock(LOCK_EX)` selama `find_available_port + register_port` (race kalau dua create paralel).
- **Read instance file**: `flock(LOCK_SH)` untuk read.
- **Write instance file**: `flock(LOCK_EX)`.
- **Bash dan Go menggunakan same file**: `flock(2)` adalah advisory lock di kernel, bekerja cross-process & cross-language.

### 8.4. Coexistence Rules

| Scenario | Resolusi |
|----------|----------|
| Bash create + RM-API create paralel dengan nama beda | OK (port allocation safe via flock) |
| Bash create + RM-API create dengan nama SAMA | Yang kedua dapat error "instance exists" — file check setelah lock acquire |
| Bash delete saat RM-API sedang start instance yang sama | RM-API operasi gagal saat write file (file hilang) — return 500, ERP retry |
| Manual edit `.instance_<nama>` saat RM-API running | RM-API baca ulang setiap operasi (no caching state) |

---

## 9. Non-Functional Requirements

| ID | Requirement |
|----|-------------|
| **RM-NF01** | `POST /instances` (create end-to-end) MUST selesai dalam < 90 detik (95th percentile). |
| **RM-NF02** | `GET /instances/{name}` MUST < 200ms (95th percentile). |
| **RM-NF03** | `GET /server/health` MUST < 100ms (cek ringan). |
| **RM-NF04** | RM-API MUST handle 4 concurrent create requests tanpa data race. |
| **RM-NF05** | RM-API MUST graceful shutdown — terima SIGTERM, selesaikan in-flight request, tutup HTTP server dalam 30s. |
| **RM-NF06** | Memory footprint idle MUST < 50MB. |
| **RM-NF07** | Binary size MUST < 30MB (single static binary, tanpa CGO kalau bisa). |
| **RM-NF08** | Logging MUST structured (JSON via `slog`) untuk parsing log centralized. |
| **RM-NF09** | RM-API MUST tahan restart kapan saja — tidak corrupt state file. |

---

## 10. Failure Modes & Rollback

### 10.1. Partial Create Failure

`POST /instances` adalah operasi multi-step. Jika gagal di tengah, RM-API WAJIB rollback:

```
Step 1: Allocate ports               → on fail: release ports
Step 2: Create MariaDB DB + user     → on fail: drop DB, release ports
Step 3: Import schema                → on fail: drop DB, release ports
Step 4: Generate FreeRADIUS configs  → on fail: drop DB, delete configs, release ports
Step 5: Reload FreeRADIUS            → on fail: revert configs, drop DB, release ports
Step 6: Spawn freeradius-api         → on fail: kill process, revert all
Step 7: Write .instance_<nama>       → on fail: cleanup all
Step 8: Mark success → return 201
```

Implementasi: pakai pattern `defer` Go dengan `success := false; defer func() { if !success { rollback() } }()`.

### 10.2. Idempotency

| Endpoint | Idempotent? | Behavior pada retry |
|----------|-------------|---------------------|
| `POST /instances` | ❌ secara natural | ERP MUST handle 409 sebagai "sudah created" — fetch info, jangan retry create |
| `DELETE /instances/{name}` | ✅ | Delete tidak ada → return 200 dengan `already_deleted: true` |
| `POST /start`, `/stop`, `/restart` | ✅ | Operasi safe untuk retry |

### 10.3. Crash Recovery

Jika RM-API crash di tengah create:
- Port allocation: tetap di port registry (jadi "leaked")
- DB partial: tetap ada
- Config files: tetap ada
- `.instance_<nama>` file: mungkin tidak ada (Step 7 belum selesai)

**Recovery procedure**: RM-API saat startup MUST:
1. Scan port registry vs `.instance_*` files.
2. Detect orphan: port registered tapi tidak ada `.instance_*` matching.
3. Auto-cleanup orphan kalau usia > 5 menit.
4. Log warning untuk audit.

---

## 11. Security Requirements

| ID | Requirement |
|----|-------------|
| **RM-S01** | RM-API MUST run as root (butuh akses systemctl, /etc/freeradius, MariaDB admin). |
| **RM-S02** | RM-API MUST bind ke private IP saja (default), tidak `0.0.0.0`. |
| **RM-S03** | API token MUST stored sebagai bcrypt hash di server (kecuali single-token mode via env). |
| **RM-S04** | Password (DB, Swagger) MUST tidak pernah di-log plaintext. |
| **RM-S05** | RM-API MUST validate input nama instance dengan regex `^[a-z0-9_]{1,32}$` untuk cegah path injection. |
| **RM-S06** | RM-API MUST tolak nama reserved: `default`, `inner-tunnel`, `control`, `status`, `radius`, `mysql`. |
| **RM-S07** | Endpoint `/health` MUST tidak expose info sensitif (versi service granular, hostname penuh). |
| **RM-S08** | Audit log MUST mencatat: timestamp, token-id (bukan token), action, target instance, hasil. |
| **RM-S09** | Log MUST rotated (logrotate) dan disimpan minimal 90 hari (compliance). |

---

## 12. Testing Requirements

### 12.1. Unit Tests

- `internal/manager/ports_test.go`: race test dengan goroutines untuk port allocation.
- `internal/manager/database_test.go`: mock `*sql.DB` untuk CRUD logic.
- `internal/manager/statefile_test.go`: round-trip read/write `.instance_*` file.
- `internal/api/*_test.go`: HTTP handler test dengan `httptest`.

Coverage target: ≥ 70% untuk `internal/manager/`.

### 12.2. Integration Tests

- Spin up MariaDB di Docker, jalankan full create → verify DB exist + radcheck table populated → delete → verify cleanup.
- Test concurrent create (4 goroutine).
- Test rollback: inject failure di Step 4 → verify Step 1-3 di-rollback.

### 12.3. Compatibility Test

- Create instance via RM-API → verify `radius-manager.sh info <nama>` masih bisa baca info dengan benar.
- Create via bash → verify `GET /v1/instances/<nama>` di RM-API return correct data.

---

## 13. Migration & Rollout Plan

### Phase 0 — Preparation (1 minggu)
- Setup Go module + struktur folder.
- Build & deploy skeleton ke staging RADIUS VM.
- Generate token, test `/health` dari ERP staging.

### Phase 1 — Read-only API (1 minggu)
- Implement `GET /instances`, `/instances/{name}`, `/server/info`, `/server/health`.
- ERP team mulai integrasi: register staging server di ERP, list existing instances.
- **No write operations** — masih aman karena cuma baca.

### Phase 2 — Create + Delete (2 minggu)
- Implement `POST /instances` dengan rollback lengkap.
- Implement `DELETE /instances/{name}`.
- Test paralel dengan bash script di staging.
- Compatibility test: create via API → bash baca, dan sebaliknya.

### Phase 3 — Lifecycle Ops (1 minggu)
- Implement start/stop/restart/test.
- Hardening: timeout, retry, error messages.
- Audit log + structured logging.

### Phase 4 — Production Rollout (1 minggu)
- Deploy ke 1 RADIUS VM production (paling kecil dulu).
- Feature flag di ERP: `use_rm_api_for_server_id=N`.
- 1 Mitra baru di-onboard via API end-to-end. Validate.
- Roll forward ke semua server gradually.

**Total**: ~6 minggu calendar (dengan 1 dev fokus + review checkpoints).

### 13.5. Implementation Status

Status aktual implementasi Go di mono-repo (`cmd/radius-manager-api/` + `internal/`).

| Phase | Scope | Status | Tanggal | Catatan |
|-------|-------|--------|---------|---------|
| Phase 0 | Setup (module, struktur folder, skeleton) | ✅ | 2026-05-07 | Go 1.22+, `cmd/radius-manager-api/`, `internal/{api,manager,system,templates,schema,config}/`, `pkg/types/`. |
| Phase 1 | Read-only API (`/server/*`, `GET /instances*`) | ✅ | 2026-05-07 | `internal/manager/read.go` + handler chi di `internal/api/handlers.go`. |
| Phase 2 | Create + Delete instance | ✅ | 2026-05-07 | `create.go`/`delete.go` dengan transactional rollback (defer + cleanup slice). **Catatan**: P2.4 schema file (`internal/schema/migrations/001_init.sql`) masih placeholder — BLOCKED pada otorisasi fetch eksternal dari upstream FreeRADIUS. |
| Phase 3 | Lifecycle ops (start/stop/restart/test) | ✅ | 2026-05-07 | `lifecycle.go` delegates ke `Systemctl` interface; `TestInstance` probes UDP via `net.ListenPacket` dan TCP via `net.Dial`. |
| Phase 4 | Audit log + OpenAPI export | ✅ | 2026-05-07 | Partial: middleware `internal/api/audit.go` tulis 1 baris JSON per mutating request; `openapi.yaml` di-embed dan disajikan di `GET /v1/openapi.yaml` (public). Integration test melawan Docker MariaDB **masih pending**. |

Di luar phase plan, supporting infra yang sudah ada:
- `internal/system/`: interface `Systemctl`, `FreeRADIUS`, `Filesystem` + implementasi real (`os/exec`, `os` file APIs) dan mock (failure injection) untuk unit test.
- `internal/manager/ports.go`: `PortRegistry` dengan `flock(2)` untuk race-safe `AllocateAuthPort` / `AllocateAPIPort` / `Unregister`.
- `internal/manager/secrets.go`: `GeneratePassword` via `crypto/rand`.
- `internal/manager/statefile.go`: read/write `.instance_<name>` dalam format KEY=VALUE bash-compatible.
- `internal/manager/database.go`: `DBManager` dengan `CreateDatabase`, `CreateUserAndGrant` (localhost + remote), `DropDatabase`, `ImportSchema` (idempotency probe via `SHOW TABLES LIKE 'radcheck'`).
- `internal/templates/`: `//go:embed` loader untuk `sql_module.tmpl`, `eap_module.tmpl`, `inner_tunnel.tmpl`, `virtual_server.tmpl` (port dari heredoc bash di `radius-manager.sh`).

---

## 14. Implications untuk Dokumen Existing

Dokumen ini **tidak memodifikasi** PRD.md atau SRS.md. Tetapi ada beberapa bagian yang sebaiknya **di-revisi di versi PRD/SRS berikutnya** (v1.1.0):

| Dokumen | Bagian | Yang perlu di-update |
|---------|--------|----------------------|
| PRD.md | §3 Non-Goals | Hapus bullet "Bukan mengganti `radius-manager.sh`" — diganti: "`radius-manager.sh` tetap ada sebagai engine + tool manual; akses programmatic via RM-API." |
| PRD.md | §5 Architecture | Tambah komponen RM-API di diagram. Tambah VPN tunnel sebagai layer eksplisit antara NAS dan RADIUS Server. |
| PRD.md | §10 | Update tabel `radius-manager.sh` commands: tambah catatan "API equivalent: lihat SRS-RadiusManagerAPI.md". |
| SRS.md | §2 System Context | Tambah ERP control plane responsibility: "ERP juga manage instance lifecycle via RM-API, bukan hanya user CRUD." |
| SRS.md | §6 Data Model | Tambah tabel `radius_servers` (Level 1 registry). Update `radius_instances` dengan FK `server_id`. |

---

## 15. Open Questions

### 15.1. Resolved (locked-in untuk v0.1.0)

| # | Question | Decision | Lokasi spec |
|---|----------|----------|-------------|
| RM-Q01 | freeradius-api: child process vs systemd unit? | **Systemd unit per instance** (`radius-api-<nama>.service` di-generate oleh RM-API, di-`systemctl enable --now`) | §7.5, §6.3 |
| RM-Q02 | Swagger UI di RM-API? | **Tidak** — generate `openapi.yaml` saja, embed via `//go:embed`. Tidak ada `/docs` runtime. | §4 (kontrak ada di doc ini) |
| RM-Q06 | Schema MariaDB import strategy? | **Embed `.sql` via Go `embed` package**. File `internal/schema/*.sql` di-compile ke binary. Migration via incremental file naming (`001_init.sql`, dst). | §7.5, §11 |
| (impl) | HTTP framework | **chi** (`github.com/go-chi/chi/v5`) — lightweight, net/http compatible. | §7 |
| RM-Q03 | autoclearzombie + autobackups3 cron jobs perlu di-port ke RM-API? | **Resolved di v0.3.0**: implemented via `Maintenance` interface (`internal/system/maintenance.go`) dengan backend RealMaintenance (systemd `.timer + .service` pair) dan SupervisordMaintenance (sleep-loop program). Per-instance timer setup otomatis saat CreateInstance ketika `RM_API_MAINTENANCE_BACKEND` di-set. Lihat §20. | §20 |
| (impl) | freeradius-api repo strategy (clone + venv per instance) | **Template-once + copy per instance**. Implementation deferred ke v0.2.0. Phase 2 saat ini hanya generate systemd unit yang reference `apiDir` yang diharapkan; bootstrap `git clone` + `python -m venv` belum dilakukan oleh RM-API. | §17, §18 |

### 15.2. Masih Open

| # | Question | Catatan |
|---|----------|---------|
| RM-Q04 | Rate limiting di RM-API? | Untuk v1 tidak perlu (private network, low call rate). v2 jika ERP melakukan batch onboarding. |
| RM-Q05 | Versioning binary RM-API vs format `.instance_*`? | Saran: semver ketat. Format file punya versi sendiri (`SCHEMA_VERSION=1` di file). |
| RM-Q07 | Metrics endpoint (Prometheus `/metrics`)? | Recommended untuk v2 — observability penting untuk multi-server fleet. |

---

## 16. Revision History

| Versi | Tanggal | Perubahan |
|-------|---------|-----------|
| 0.1.0 | 2026-05-07 | Initial draft. Spec endpoint, library design, coexistence rules, rollout plan. |
| 0.1.1 | 2026-05-07 | Lock 4 keputusan kunci: systemd unit per instance (RM-Q01), no Swagger UI (RM-Q02), embed schema.sql (RM-Q06), chi framework. |
| 0.1.2 | 2026-05-07 | Phase 2 (create/delete), Phase 3 (lifecycle: start/stop/restart/test), dan Phase 4 (audit log + OpenAPI export) selesai diimplementasi dan ter-cover unit test. Tambah §13.5 Implementation Status, §17 Real-Environment Validation TODO, §18 Known Deviations from `radius-manager.sh`. Resolve RM-Q03 (cron jobs di-skip) dan freeradius-api repo strategy (deferred ke v0.2.0). |
| 0.2.0 | 2026-05-07 | Tambah §19 Docker Local Dev Stack: image multi-stage Debian-slim dengan supervisord sebagai PID 1, `SupervisordSystemctl`/`SupervisordFreeRADIUS` di `internal/system/supervisord.go`, env var `RM_API_SYSTEMD_BACKEND` untuk seleksi backend, env var `RM_API_INSTANCE_DB_HOST/PORT` untuk konfigurasi DB host per-instance (default tetap `localhost:3306`). Schema migration `001_init.sql` di-replace dari placeholder ke canonical FreeRADIUS MySQL schema (radacct, radcheck, radreply, radusergroup, radgroupcheck, radgroupreply, radpostauth, nas, nasreload). Entrypoint script auto-tambah `multiStatements=true` ke DSN. RM-V01 partially closed (E2E flow `POST /v1/instances` → instance up → DB schema imported → `<name>-api` running) sudah lulus di Docker dev stack; RM-V02..RM-V06 masih perlu Linux real VM. |
| 0.3.0 | 2026-05-08 | Tambah §20 Maintenance Timers: `system.Maintenance` interface (`InstallJob`/`RemoveJob`/`ListJobs`) dengan backend `RealMaintenance` (systemd `.timer + .service` pair) dan `SupervisordMaintenance` (sleep-loop program). `manager.MaintenanceManager` orchestrate dua job per instance — `<name>-zombie` (every 15m) + `<name>-backup` (daily, hanya kalau S3 dikonfigurasi). `bootstrap.PatchScripts` rewrite `^DB_*=...$` / `^REMOTE=`/`^BUCKET=`/`^BACKUP_PATH=` lines di `autoclearzombie.sh` dan `autobackups3.sh` (mirror dari sed step di radius-manager.sh:setup_api). Env vars baru: `RM_API_MAINTENANCE_BACKEND`, `RM_API_S3_REMOTE`, `RM_API_S3_BUCKET`, `RM_API_S3_BACKUP_ROOT`. RM-Q03 resolved (sebelumnya skip), RM-D03 ditandai migrated (cron lewat `update.sh` digantikan oleh systemd timer per-instance yang di-install otomatis oleh RM-API). |

---

## 17. Real-Environment Validation TODO

Hal-hal yang **tidak bisa di-test** dari mesin dev macOS dan harus diverifikasi langsung di Linux RADIUS VM (staging) sebelum cut release production:

| # | Yang divalidasi | Kenapa butuh Linux/real env |
|---|-----------------|------------------------------|
| RM-V01 | `systemctl daemon-reload` + `enable` + `start` real untuk unit `<name>-api.service` yang di-generate oleh RM-API | systemd tidak tersedia di macOS; mock hanya cek shape pemanggilan. |
| RM-V02 | `systemctl reload freeradius` setelah RM-API write config — apakah FreeRADIUS accept rendered configs (`sql_<name>`, virtual server, EAP) tanpa parse error? | Butuh FreeRADIUS 3.0.x yang real; `unlang`/`mods-available` syntax hanya ter-validasi oleh `radiusd -X`. |
| RM-V03 | Real MariaDB `CREATE DATABASE` / `CREATE USER` + `GRANT` (localhost + remote) / schema import dari `001_init.sql` | Saat ini `database_test.go` pakai `sqlmock`. Perlu MariaDB 10.11+ real untuk konfirmasi DDL + grant. |
| RM-V04 | End-to-end flow: ERP `POST /v1/instances` → instance fully up → `freeradius-api` reachable di `api_url` yang di-return → register NAS → user PPPoE bisa auth | Butuh stack lengkap (Go RM-API + freeradius + mariadb + freeradius-api uvicorn) berjalan. |
| RM-V05 | `flock(2)` interop antara Go RM-API dan `radius-manager.sh` bash di file `.port_registry` yang **shared** | `flock(2)` adalah advisory lock kernel-level — perlu di-test cross-process pada filesystem Linux yang sama (bukan macOS APFS). |
| RM-V06 | Format `.instance_<name>` yang ditulis Go masih bisa di-parse oleh `load_instance_info` di `radius-manager.sh` (round-trip bash ↔ Go) | Compatibility test §12.3. Bash parser pakai `source`-style atau grep-line; perlu konfirmasi tidak ada quoting/escape mismatch. |

Sampai semua item RM-V01..RM-V06 hijau di staging RADIUS VM, RM-API **belum boleh** di-promote ke production server pertama.

---

## 18. Known Deviations from `radius-manager.sh`

Hal-hal di mana implementasi Go di RM-API **secara sengaja berbeda** dengan bash script. Tujuan: transparansi untuk ops yang reading kedua codebase.

| # | Area | `radius-manager.sh` (bash) | RM-API (Go) | Behavior change? |
|---|------|----------------------------|-------------|------------------|
| RM-D01 | Random port pick | Loop dengan `$RANDOM` (16-bit, distribusi tidak rata) | `crypto/rand` (uniform across range) | Tidak — keduanya tetap pick port di range yang sama, hanya distribusi statistik berbeda. |
| RM-D02 | DB password generation | Pipeline `openssl rand -base64 ... \| tr ... \| head -c ...` | `crypto/rand` direct ke alphabet whitelist | Tidak — entropy equivalent, output character set sama. Implementasi Go lebih sederhana, no shell-out. |
| RM-D03 | Cron job patching `autoclearzombie.sh` + `autobackups3.sh` | Bash `update.sh` patch crontab system-wide saat `update` | **Migrated di v0.3.0**: RM-API install systemd `.timer + .service` per instance via `MaintenanceManager` (lihat §20). Crontab tidak dipakai. Bash flow tetap kompatibel sebagai fallback (operator yang masih pakai `update.sh` cron tetap bekerja, hanya saja RM-API timer akan duplicate eksekusi — operator harus pilih satu source-of-truth). | Ya: RM-API sekarang install timer otomatis. |
| RM-D04 | Python venv setup + `git clone` freeradius-api per instance | Bash do `git clone` + `python -m venv` + `pip install` saat create | **Belum direplikasi** oleh RM-API. Saat ini Phase 2 hanya tulis systemd unit yang reference direktori API yang diharapkan. Bootstrap repo deferred ke v0.2.0. | Ya, sementara: operator harus pre-provision direktori API (atau pakai bash create dulu). Akan ditutup di v0.2.0. |
| RM-D05 | Format port registry | Bash tulis `<port> # <admin> <kind>` per baris | Go **preserve format yang sama** (parsing + write); `flock(2)` tetap dipakai | Tidak — format file 1:1 compatible dengan bash. |

---

## Lampiran A — Quick Reference: ERP-side Pseudo-code

```go
// Onboarding Mitra baru — full flow
func OnboardMitra(ctx context.Context, mitra Mitra) error {
    // Step 3: Create instance via RM-API
    server, err := pickRadiusServer(ctx, mitra.Region)
    if err != nil {
        return fmt.Errorf("placement: %w", err)
    }

    rmClient := rmapi.NewClient(server.APIURL, server.APIToken)
    instance, err := rmClient.CreateInstance(ctx, rmapi.CreateInstanceRequest{
        Name: mitraInstanceName(mitra),
    })
    if err != nil {
        return fmt.Errorf("create instance: %w", err)
    }

    // Save Level 3 registry
    if err := db.SaveRadiusInstance(ctx, RadiusInstance{
        ServerID:        server.ID,
        Name:            instance.Name,
        APIURL:          instance.APIURL,
        SwaggerUsername: instance.Swagger.Username,
        SwaggerPassword: instance.Swagger.Password, // encrypt before insert
        AuthPort:        instance.Ports.Auth,
        // ...
    }); err != nil {
        return fmt.Errorf("save instance registry: %w", err)
    }

    // Step 4: Register NAS via freeradius-api per-instance
    fraClient := fra.NewClient(instance.APIURL,
        instance.Swagger.Username, instance.Swagger.Password)
    if err := fraClient.CreateNAS(ctx, fra.CreateNASRequest{
        NASName:    mitra.NASName,
        NASIP:      mitra.VPNPrivateIP,
        Secret:     mitra.RADIUSSecret,
        Shortname:  mitra.NASShortname,
    }); err != nil {
        return fmt.Errorf("register NAS: %w", err)
    }

    return db.UpdateMitraStatus(ctx, mitra.ID, "active")
}
```

---

## 19. Docker Local Dev Stack

### 19.1. Tujuan

Memungkinkan reviewer / developer menjalankan flow `POST /v1/instances` end-to-end dari laptop macOS atau host Linux non-systemd tanpa harus provision VM. Stack ini **bukan** untuk produksi — production VM tetap pakai systemd.

Sumber: `Dockerfile`, `entrypoint.sh`, `deployments/docker/supervisord.conf`, `docker-compose.dev.yml`. Usage: lihat `cmd/radius-manager-api/README.md` bagian "Local end-to-end with Docker".

### 19.2. Arsitektur

```
docker-compose.dev.yml
├── mariadb (mariadb:10.11)              port host 13307 → container 3306
└── rm-api (build: ./Dockerfile)          port host 9000  → container 9000
    │
    └── supervisord (PID 1)
        ├── freeradius        (always running, port 1812/1813)
        ├── rm-api            (always running, the Go binary `serve`)
        └── <name>-api        (added at runtime when ERP calls POST /v1/instances)
```

Per-instance freeradius-api processes are not pre-defined in `supervisord.conf`. RM-API tulis file `[program:<name>-api]` ke `/etc/supervisor/conf.d/<name>-api.conf` saat create, lalu shell `supervisorctl reread && supervisorctl update && supervisorctl start <name>-api`.

### 19.3. Trade-offs: Supervisord vs Systemd

| Aspek | Supervisord (Docker dev) | Systemd (Linux VM produksi) |
|-------|--------------------------|------------------------------|
| Container support | Ya (jalan sebagai child PID 1) | Tidak (butuh `--privileged` + cgroup mounts; ditolak) |
| Restart-on-crash | Ya (`autorestart=true`) | Ya (`Restart=always`) |
| Boot-time enable | Implicit (`autostart=true`) | Eksplisit (`systemctl enable`) |
| Log integration | Stdout/stderr per program ke `/var/log/supervisor/<prog>.{out,err}.log` | journald + syslog |
| Operasi `is-active` | Parse output `supervisorctl status` (look for `RUNNING`) | `systemctl is-active --quiet` exit code |
| Familiarity (ops Indonesia) | Rendah (jarang di RADIUS VM) | Tinggi (default Debian/Ubuntu) |

Pemilihan tetap default ke systemd. Operator dev pakai env var:

```bash
RM_API_SYSTEMD_BACKEND=supervisord    # Docker dev
RM_API_SYSTEMD_BACKEND=systemd         # default; production
```

### 19.4. Implementation Notes

- `internal/system/supervisord.go` — translasi systemd unit (subset: `ExecStart`, `WorkingDirectory`, `User`, `Restart`) ke `[program:...]` block. Tidak parse `[Install]` (tidak relevan untuk supervisord).
- `internal/system/supervisord_test.go` — TDD coverage untuk WriteUnit + RemoveUnit + lifecycle calls + IsActive parsing. Pakai shell script fake sebagai stand-in `supervisorctl` agar test tidak butuh daemon real.
- `entrypoint.sh` — generate token (idempoten), tunggu MariaDB ready, compose DSN dari `RM_API_DB_*` (auto-tambah `multiStatements=true` agar canonical FreeRADIUS schema yang multi-CREATE-TABLE bisa di-Exec sekali). `serve` → exec supervisord; `init`/`version` → exec biner langsung.
- `internal/schema/migrations/001_init.sql` — diisi schema canonical FreeRADIUS MySQL (radacct, radcheck, radreply, radusergroup, radgroupcheck, radgroupreply, radpostauth, nas, nasreload). Sebelumnya cuma `SELECT 1 AS schema_placeholder`.

### 19.5. Coexistence dengan `radius-manager.sh`

Tidak ada interaksi. Bash script tidak ada di image (image ini ditujukan untuk RM-API saja). Operator yang ingin reproduksi flow bash di Docker harus install bash dependencies sendiri — bukan target stack ini.

### 19.6. Image Footprint

- Final image size: ~363 MB (di bawah budget 600 MB di task spec).
- Builder stage di-discard — multistage build hanya menyalin biner ke runtime image.
- Tidak ada `apt` cache di final layer (`rm -rf /var/lib/apt/lists/*`).
- BuildKit cache mounts (`/root/.cache/go-build`, `/go/pkg/mod`) untuk iteratif build cepat di mesin developer.

### 19.7. Hal yang Belum Tercover

- VPN provisioning — stack tidak ada interface VPN (irrelevant untuk dev).
- TLS — listener default plaintext HTTP. Production wajib pasang reverse proxy / mTLS via dokumen lain.
- Multi-instance scaling — satu container, satu freeradius. Tidak menggambarkan sharded production deploy.
- Persistensi `.port_registry` cross-restart — saat ini volume mount `/etc/freeradius/3.0` persist, tapi belum ada test untuk crash-recovery.

---

## 20. Maintenance Timers (Autoclearzombie + Autobackups3)

### 20.1. Tujuan

Menggantikan cron entries yang sebelumnya di-manage oleh `radius-manager.sh:setup_api()` + `update.sh` dengan scheduling backend yang explicit, audit-friendly, dan per-instance. Dua maintenance script yang berasal dari freeradius-api repo:

| Script | Frekuensi | Job name | Tujuan |
|--------|-----------|----------|--------|
| `autoclearzombie.sh` | every 15 minutes | `<instance>-zombie` | Cleanup stale `radacct` sessions (kill PPPoE leftovers). |
| `autobackups3.sh` | `@daily` | `<instance>-backup` | Dump per-instance MariaDB lalu push ke S3 via `rclone`. |

Backup job hanya di-install kalau `RM_API_S3_REMOTE` di-set. Kalau kosong, hanya `<instance>-zombie` yang aktif (sesuai prinsip: zombie cleanup is essential, S3 backup optional).

### 20.2. Flow CreateInstance (v0.3.0)

```
Step 6b: bootstrap.SetupInstance       — git clone template, copy ke <APIDir>, venv + .env
Step 6b': bootstrap.PatchScripts       — sed-equivalent rewrite DB_* + REMOTE/BUCKET/BACKUP_PATH
                                          di autoclearzombie.sh & autobackups3.sh (idempoten,
                                          skip kalau script tidak ada)
Step 6c:  Maintenance.SetupForInstance — InstallJob untuk <instance>-zombie + <instance>-backup
                                          dengan env DB_HOST/PORT/USER/PASS/NAME (+ REMOTE/BUCKET/
                                          BACKUP_PATH untuk backup). Belt-and-suspenders: env
                                          override + script default keduanya benar.
```

DeleteInstance memanggil `Maintenance.TeardownForInstance` BEFORE menyentuh systemd unit / DB drop, sehingga timer tidak fire mid-delete (mis. `autobackups3` race dengan `DROP DATABASE`).

### 20.3. Backend Selection

| Backend | Implementasi | Kapan dipakai |
|---------|--------------|----------------|
| `systemd` (default) | `system.RealMaintenance`: tulis `<job>.timer` + `<job>.service` ke `/etc/systemd/system`, `daemon-reload`, `enable --now`. Schedule `every 15m` → `OnCalendar=*:0/15`; `daily` → `OnCalendar=daily`. | Production Linux VM. |
| `supervisord` | `system.SupervisordMaintenance`: tulis `[program:<job>]` dengan `command=/bin/sh -c 'while true; do <command>; sleep <N>; done'`. | Docker dev stack (no systemd). |
| `none` | Tidak konstruksi `MaintenanceManager` sama sekali. CreateInstance skip step 6c. | Operator yang masih pakai `update.sh` cron, atau env minimal. |

Selection via env `RM_API_MAINTENANCE_BACKEND`. Default `systemd` karena production target.

### 20.4. Env Vars

| Env | Default | Effect |
|-----|---------|--------|
| `RM_API_MAINTENANCE_BACKEND` | `systemd` | `systemd` / `supervisord` / `none`. |
| `RM_API_S3_REMOTE` | (empty) | rclone remote name (mis. `ljns3`). Empty → backup timer di-skip; zombie timer tetap di-install. |
| `RM_API_S3_BUCKET` | (empty) | S3 bucket (mis. `backup-db`). |
| `RM_API_S3_BACKUP_ROOT` | `radiusdb` | Prefix path; per-instance suffix di-append jadi `<root>/<instance>`. |

### 20.5. Trade-off: Kenapa Systemd Timer

Pilihan adalah **Option B (systemd timer)** vs alternatif (cron, child process, internal Go ticker):

| Alternatif | Pro | Con — kenapa rejected |
|------------|-----|----------------------|
| Cron via `crontab -e` (bash existing) | Simpel | System-wide crontab ditulis manual, susah di-audit per instance, conflict kalau ada >1 admin manage. RM-API butuh idempoten + per-instance teardown. |
| Child process Go (goroutine) | No external deps | RM-API restart → semua timer reset, bisa miss eksekusi. Tidak survive process crash. |
| Systemd timer (chosen) | Sudah dependency RM-API (`<name>-api.service` juga systemd), audit-friendly (`systemctl list-timers`), survive RM-API restart, per-instance teardown clean. | Tidak jalan di Docker — diatasi dengan SupervisordMaintenance. |
| Supervisord sleep-loop | Jalan di Docker | Schedule kasar (sleep-loop bukan calendar-aware), tapi acceptable untuk dev. |

Conclusion: systemd di production, supervisord fallback di Docker dev, dan selection lewat env. Sama dengan pattern yang sudah ada untuk `Systemctl` (RM_API_SYSTEMD_BACKEND).

### 20.6. Idempotency & Cleanup

- `InstallJob` dengan name yang sudah ada akan overwrite unit / program block.
- `RemoveJob` dengan name yang tidak ada return nil (tidak error).
- `Maintenance.TeardownForInstance` ALWAYS panggil RemoveJob untuk dua nama (`<name>-zombie`, `<name>-backup`) — aman dipanggil walaupun salah satu/keduanya tidak pernah di-install.
- `bootstrap.PatchScripts` skip silently kalau file tidak ada (mis. freeradius-api template revisi lama tidak ship `autobackups3.sh`).
