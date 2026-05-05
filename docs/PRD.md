# PRD — FreeRADIUS Manager

| Field | Value |
|-------|-------|
| Versi | 1.0.0 |
| Tanggal | 2026-05-05 |
| Status | Active baseline (mandatory) |
| Owner | Tim Network Operations |
| Repo | `freeradius-manager` (bash scripts) + `freeradius-api` (Python/FastAPI per-instance) |

> **Catatan**: Dokumen ini adalah **baseline wajib** untuk semua perubahan ke depan. Setiap penambahan fitur, refactor, atau perubahan deploy harus dicocokkan dengan PRD ini.

---

## 1. Problem Statement

Tim ISP mengelola **multiple FreeRADIUS instances** di satu server untuk melayani beberapa client/tenant (ISP reseller). Saat ini:
- Provisioning instance dilakukan manual via CLI (`radius-manager.sh create/delete`).
- CRUD user RADIUS (PPPoE/Hotspot) dilakukan via REST API per-instance (Python/FastAPI) atau manual SQL.
- **Tidak ada integrasi otomatis** antara billing system dan RADIUS — operator harus manual create/suspend user saat customer bayar/expired.
- Disconnect user aktif harus manual via `radclient` atau API disconnect endpoint.
- Tidak ada single dashboard yang menampilkan status semua instance + user.

## 2. Goals

- Sediakan **REST API per-instance** (sudah ada via `freeradius-api`) untuk CRUD user RADIUS, query session, dan disconnect.
- Dokumentasikan API contract yang jelas agar **Billing System (Golang)** bisa mengintegrasikan provisioning otomatis.
- Billing system bisa:
  - Auto-create user RADIUS saat customer bayar/subscribe.
  - Auto-delete/suspend user saat customer expired.
  - Query status online/offline user.
  - Disconnect user aktif (force logout).
  - Assign user ke group (bandwidth profile).
- Maintain backward compatibility — `radius-manager.sh` tetap berfungsi untuk manage instance.

## 3. Non-Goals

- Bukan mengganti `radius-manager.sh` — script tetap dipakai untuk create/delete instance.
- Bukan membuat API gateway baru — billing langsung panggil REST API per-instance.
- Bukan multi-tenant billing — billing handle mapping customer → instance sendiri.
- Bukan monitoring real-time (SNMP/NetFlow) — hanya query session dari `radacct`.
- Bukan mengelola NAS/router dari billing — NAS tetap didaftarkan manual ke tabel `nas`.

## 4. Personas / Users

| Persona | Deskripsi | Kebutuhan utama |
|---------|-----------|-----------------|
| **Billing System (Golang)** | Service otomatis yang provision/suspend user | API CRUD user, group assignment, disconnect, status query |
| **NOC Operator** | Staf yang manage instance dan troubleshoot | CLI `radius-manager.sh`, Swagger UI per-instance |
| **Network Engineer** | Maintain FreeRADIUS config, NAS, pool | SSH access, manual SQL, radtest |

## 5. Current Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    RADIUS Server (Ubuntu/Debian)                  │
│                                                                   │
│  ┌──────────────────┐   ┌──────────────────┐                    │
│  │ FreeRADIUS        │   │ FreeRADIUS        │  ... (N instances)│
│  │ Instance: client1 │   │ Instance: client2 │                    │
│  │ Auth: 12345       │   │ Auth: 23456       │                    │
│  │ Acct: 12346       │   │ Acct: 23457       │                    │
│  │ CoA:  14345       │   │ CoA:  25456       │                    │
│  └────────┬─────────┘   └────────┬─────────┘                    │
│           │                       │                               │
│  ┌────────▼─────────┐   ┌────────▼─────────┐                    │
│  │ MariaDB: client1  │   │ MariaDB: client2  │                    │
│  │ (radcheck,radacct │   │ (radcheck,radacct │                    │
│  │  radusergroup,    │   │  radusergroup,    │                    │
│  │  radreply, nas)   │   │  radreply, nas)   │                    │
│  └────────┬─────────┘   └────────┬─────────┘                    │
│           │                       │                               │
│  ┌────────▼─────────┐   ┌────────▼─────────┐                    │
│  │ REST API: 8100    │   │ REST API: 8101    │                    │
│  │ (Python/FastAPI)  │   │ (Python/FastAPI)  │                    │
│  │ Swagger: /docs    │   │ Swagger: /docs    │                    │
│  └──────────────────┘   └──────────────────┘                    │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────┐    │
│  │ radius-manager.sh (create/delete/start/stop/list/info)    │    │
│  └──────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
         ▲                    ▲
         │                    │
    MikroTik NAS         MikroTik NAS
    (PPPoE/Hotspot)      (PPPoE/Hotspot)
```

## 6. REST API per-Instance (freeradius-api)

### 6.1. Authentication

- HTTP Basic Auth (Swagger UI): `SWAGGER_USERNAME` / `SWAGGER_PASSWORD` dari `.env`
- Base URL: `http://<server_ip>:<API_PORT>/api/v1`

### 6.2. Endpoints

| Method | Path | Deskripsi |
|--------|------|-----------|
| **Users (radcheck)** | | |
| GET | `/users/users` | List semua user (radcheck entries) |
| GET | `/users/users/{username}` | Get user by username |
| POST | `/users/users` | Create user (radcheck entry) |
| PUT | `/users/users/{user_id}` | Update user by ID |
| DELETE | `/users/users/{user_id}` | Delete user + related radreply Framed-Pool |
| **User Groups (radusergroup)** | | |
| GET | `/radusergroup/user-group/` | List user-group assignments |
| POST | `/radusergroup/user-group/` | Assign user to group |
| PUT | `/radusergroup/user-group/{group_id}` | Update group assignment |
| DELETE | `/radusergroup/user-group/{group_id}` | Remove user from group |
| **Rad Reply** | | |
| GET | `/radreply/reply/` | List radreply entries |
| POST | `/radreply/reply/` | Create radreply entry |
| PUT | `/radreply/reply/{reply_id}` | Update radreply |
| DELETE | `/radreply/reply/{reply_id}` | Delete radreply |
| **Rad Group Reply** | | |
| GET | `/radgroupreply/` | List group reply attributes |
| POST | `/radgroupreply/` | Create group reply |
| PUT | `/radgroupreply/{id}` | Update group reply |
| DELETE | `/radgroupreply/{id}` | Delete group reply |
| **Rad Group Check** | | |
| GET | `/radgroupcheck/` | List group check attributes |
| POST | `/radgroupcheck/` | Create group check |
| PUT | `/radgroupcheck/{id}` | Update group check |
| DELETE | `/radgroupcheck/{id}` | Delete group check |
| **Accounting (radacct)** | | |
| GET | `/radacct/` | List accounting records (pagination, filter by username/date) |
| GET | `/radacct/{radacctid}` | Get specific accounting record |
| GET | `/radacct/status/{username}` | Get user online/offline status + last session |
| **Disconnect** | | |
| POST | `/disconnect` | Send CoA Disconnect-Request to NAS via radclient |
| **NAS** | | |
| GET | `/nas/` | List NAS entries |
| POST | `/nas/` | Create NAS entry |
| PUT | `/nas/{id}` | Update NAS |
| DELETE | `/nas/{id}` | Delete NAS |

### 6.3. Key Data Models

**RadCheck (User credentials):**
```json
{
  "id": 1,
  "username": "customer001",
  "attribute": "Cleartext-Password",
  "op": ":=",
  "value": "secretpass123"
}
```

**RadUserGroup (Bandwidth profile assignment):**
```json
{
  "id": 1,
  "username": "customer001",
  "groupname": "10Mbps",
  "priority": 1
}
```

**RadReply (Per-user reply attributes):**
```json
{
  "id": 1,
  "username": "customer001",
  "attribute": "Framed-Pool",
  "op": ":=",
  "value": "pool-10mbps"
}
```

**UserStatus (Online check):**
```json
{
  "username": "customer001",
  "is_online": true,
  "last_session": {
    "radacctid": 12345,
    "username": "customer001",
    "nasipaddress": "103.242.104.17",
    "acctstarttime": "2026-05-05T10:00:00",
    "acctstoptime": null,
    "framedipaddress": "10.10.1.50",
    "acctsessionid": "abc123"
  }
}
```

**DisconnectRequest:**
```json
{
  "username": "customer001",
  "nas_ip": "103.242.104.17",
  "port": 3799,
  "radius_secret": "ljnsecrad"
}
```

## 7. External Integration: Billing System (Golang)

### 7.1. Context

Billing System (Golang) — project yang sama yang juga integrasi ke VPN Secret Generator — akan memanggil REST API per-instance FreeRADIUS untuk:
- Auto-provision user PPPoE/Hotspot saat customer bayar.
- Auto-suspend (delete user) saat customer expired.
- Query status online/offline.
- Force disconnect user aktif.
- Assign/change bandwidth group.

### 7.2. Integration Architecture

```
┌─────────────────┐                          ┌──────────────────────────┐
│  Billing System │    HTTP Basic Auth        │  FreeRADIUS API          │
│  (Golang)       │─────────────────────────► │  Instance: client1       │
│                 │    POST /users/users       │  Port: 8100              │
│                 │    GET  /radacct/status/   │                          │
│                 │    POST /disconnect        │  ┌──────────┐           │
│                 │    POST /radusergroup/     │  │ MariaDB  │           │
│                 │                            │  └──────────┘           │
│                 │                            └──────────────────────────┘
│                 │                            ┌──────────────────────────┐
│                 │    HTTP Basic Auth         │  FreeRADIUS API          │
│                 │─────────────────────────► │  Instance: client2       │
│                 │                            │  Port: 8101              │
│                 │                            └──────────────────────────┘
└─────────────────┘
```

### 7.3. Integration Requirements

- **INT-01**: Billing MUST authenticate via HTTP Basic Auth per-instance (username/password dari `.env` instance).
- **INT-02**: Billing MUST maintain mapping: `customer → instance (base_url + credentials)`.
- **INT-03**: Billing MUST create user via `POST /users/users` dengan `attribute=Cleartext-Password`, `op=:=`, `value=<password>`.
- **INT-04**: Billing MUST assign user ke group (bandwidth) via `POST /radusergroup/user-group/`.
- **INT-05**: Billing MUST bisa set Framed-Pool via `POST /radreply/reply/` dengan `attribute=Framed-Pool`.
- **INT-06**: Billing MUST bisa delete user via `DELETE /users/users/{user_id}` saat suspend.
- **INT-07**: Billing MUST bisa disconnect user aktif via `POST /disconnect` setelah delete (force logout).
- **INT-08**: Billing SHOULD query status via `GET /radacct/status/{username}` sebelum disconnect.
- **INT-09**: Billing MUST handle multi-instance: satu billing bisa manage user di banyak RADIUS instance.
- **INT-10**: Billing MUST store `user_id` (radcheck ID) yang dikembalikan API untuk operasi update/delete selanjutnya.

### 7.4. Provisioning Workflow (Billing → RADIUS)

1. Customer bayar → Billing determine instance (berdasarkan area/reseller).
2. `POST /users/users` → create radcheck entry (username + password).
3. `POST /radusergroup/user-group/` → assign ke bandwidth group.
4. (Optional) `POST /radreply/reply/` → set Framed-Pool atau attribute lain.
5. Store mapping: `billing_customer_id ↔ radius_instance + radcheck_id + username`.

### 7.5. Suspension Workflow (Billing → RADIUS)

1. Customer expired → Billing lookup instance + user_id.
2. `GET /radacct/status/{username}` → check if online.
3. If online: `POST /disconnect` → force logout dari NAS.
4. `DELETE /users/users/{user_id}` → hapus dari radcheck (+ radreply Framed-Pool auto-deleted).
5. (Optional) `DELETE /radusergroup/user-group/{group_id}` → hapus group assignment.
6. Mark customer as suspended di billing DB.

## 8. Non-Functional Requirements

- **NFR-01**: REST API per-instance MUST respond < 500ms untuk CRUD operations.
- **NFR-02**: REST API MUST handle concurrent requests (uvicorn 4 workers default).
- **NFR-03**: Disconnect via radclient MUST timeout dalam 10s.
- **NFR-04**: Billing MUST handle instance unavailable gracefully (retry + alert).
- **NFR-05**: Credentials (Basic Auth) MUST NOT logged plaintext di billing logs.

## 9. Security Requirements

- **SEC-01**: REST API per-instance dilindungi HTTP Basic Auth.
- **SEC-02**: Password user RADIUS disimpan sebagai `Cleartext-Password` (requirement FreeRADIUS untuk CHAP/MS-CHAP).
- **SEC-03**: Komunikasi Billing → RADIUS API SHOULD over private network / VPN (tidak exposed ke public).
- **SEC-04**: Setiap instance punya credentials terpisah — compromise satu instance tidak affect yang lain.
- **SEC-05**: `radius-manager.sh` hanya bisa dijalankan sebagai root.

## 10. Instance Management (radius-manager.sh)

| Command | Fungsi |
|---------|--------|
| `create <nama> [db_pass]` | Buat instance lengkap (FreeRADIUS + DB + API) |
| `delete <nama> [--with-db]` | Hapus instance |
| `start <nama>` | Aktifkan instance |
| `stop <nama>` | Nonaktifkan instance |
| `restart` | Restart FreeRADIUS (semua instance) |
| `list` | Tampilkan semua instance + status |
| `info <nama>` | Detail credentials + port |
| `test <nama>` | Test port + Access-Request |
| `test-disconnect <nama> <user> <sid>` | CoA Disconnect test |

## 11. Risks & Open Questions

| # | Item | Status |
|---|------|--------|
| R-01 | Cleartext-Password di DB — risiko jika DB compromised | Accepted (FreeRADIUS requirement untuk CHAP) |
| R-02 | HTTP Basic Auth tanpa TLS — credential bisa di-sniff | Mitigasi: private network / VPN |
| R-03 | No rate limiting di REST API | Mitigasi: private network, billing self-limit |
| Q-01 | Apakah perlu API key auth (selain Basic Auth) untuk billing? | Belum diputuskan |
| Q-02 | Apakah perlu bulk create/delete endpoint? | Billing bisa loop, tapi bulk lebih efisien |
| Q-03 | Apakah perlu event/webhook saat user connect/disconnect? | Out of scope (butuh RADIUS accounting hook) |
| Q-04 | Naming convention username RADIUS: format apa? | Belum diputuskan (e.g., `pppoe-<customer_id>`) |

## 12. Future Roadmap

| Prio | Item | Catatan |
|------|------|---------|
| P0 | Dokumentasi API contract per-instance (Swagger export) | Pra-syarat billing integration |
| P0 | TLS/HTTPS untuk REST API (reverse proxy) | Security |
| P1 | Bulk create/delete endpoint | Billing batch efficiency |
| P1 | API key auth (selain Basic Auth) | Better security for service-to-service |
| P2 | Centralized API gateway (optional) | Single endpoint untuk semua instance |
| P2 | Webhook on accounting events | Real-time billing sync |
| P3 | Web dashboard untuk manage semua instance | Operator convenience |

---

## Lampiran A — Instance Info File Format

File: `/etc/freeradius/3.0/.instance_<nama>`

```
ADMIN_USERNAME=<nama>
DB_HOST=localhost
DB_PORT=3306
DB_NAME=<nama>
DB_USER=<nama>
DB_PASS=<generated>
AUTH_PORT=<random>
ACCT_PORT=<auth+1>
COA_PORT=<auth+2000>
INNER_PORT=<auth+5000>
API_PORT=<8100+>
SWAGGER_USERNAME=admin
SWAGGER_PASSWORD=<generated>
WEB_API_URL=http://<ip>:<api_port>/docs
CREATED=<timestamp>
```

## Lampiran B — Billing Instance Registry (Golang side)

Billing MUST maintain config/DB table untuk mapping instance:

```go
type RadiusInstance struct {
    ID              int64  `json:"id"`
    Name            string `json:"name"`            // e.g., "client1"
    BaseURL         string `json:"base_url"`        // e.g., "http://10.0.0.5:8100/api/v1"
    SwaggerUsername string `json:"swagger_username"`
    SwaggerPassword string `json:"swagger_password"` // encrypted
    NasIP           string `json:"nas_ip"`           // for disconnect
    RadiusSecret    string `json:"radius_secret"`    // for disconnect
    CoAPort         int    `json:"coa_port"`
    IsActive        bool   `json:"is_active"`
}
```
