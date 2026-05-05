# SRS — FreeRADIUS Manager: Integration Specification for Billing System

| Field | Value |
|-------|-------|
| Versi | 1.0.0 |
| Tanggal | 2026-05-05 |
| Status | Draft |
| Parent PRD | `docs/PRD.md` v1.0.0 |
| Owner | Tim Network Operations + Tim Billing |
| Bahasa Implementasi Billing | Golang |
| Bahasa Implementasi RADIUS API | Python (FastAPI) |

---

## 1. Tujuan Dokumen

Dokumen ini mendefinisikan **Software Requirements Specification (SRS)** untuk integrasi antara **Billing System (Golang)** sebagai consumer dan **FreeRADIUS REST API per-instance** sebagai provider. Fokus pada:
- API contract yang harus dipenuhi oleh setiap instance FreeRADIUS API.
- Data flow dan sequence diagram untuk provisioning/suspension.
- Golang client SDK specification.
- Multi-instance management strategy.
- Error handling dan retry strategy.

---

## 2. System Context

### 2.1. High-Level Architecture

```
┌──────────────┐     ┌─────────────────────┐     ┌────────────────────────┐
│   Customer   │     │   Billing System    │     │  RADIUS Server          │
│   Portal     │────►│   (Golang)          │     │                         │
│              │     │                     │     │  ┌─────────────────┐    │
└──────────────┘     │  - Customer CRUD    │────►│  │ Instance: A     │    │
                     │  - Invoice/Payment  │     │  │ API: 8100       │    │
                     │  - RADIUS Provision │     │  │ DB: MariaDB     │    │
                     │  - VPN Provision    │     │  └─────────────────┘    │
                     │                     │     │  ┌─────────────────┐    │
                     │  Instance Registry  │────►│  │ Instance: B     │    │
                     │  (which customer    │     │  │ API: 8101       │    │
                     │   → which instance) │     │  │ DB: MariaDB     │    │
                     └─────────────────────┘     │  └─────────────────┘    │
                                                  └────────────────────────┘
                                                           │
                                                           ▼
                                                  ┌────────────────────────┐
                                                  │  MikroTik NAS (PPPoE)  │
                                                  │  Auth → FreeRADIUS     │
                                                  │  Acct → FreeRADIUS     │
                                                  └────────────────────────┘
```

### 2.2. Integration Boundary

- **Billing** bertanggung jawab atas: customer lifecycle, payment, instance routing, RADIUS user provisioning.
- **FreeRADIUS API** bertanggung jawab atas: CRUD radcheck/radreply/radusergroup, accounting query, disconnect.
- **Boundary**: Billing memanggil REST API per-instance via HTTP Basic Auth. Tidak ada shared database.

---

## 3. Functional Requirements

### 3.1. Provisioning Flow (Customer Baru / Bayar)

**Trigger**: Customer membayar invoice atau admin activate subscription.

**Sequence:**

```
Billing                     FreeRADIUS API (instance X)         MikroTik NAS
  │                                  │                              │
  │  POST /users/users               │                              │
  │  { username, attribute:          │                              │
  │    "Cleartext-Password",         │                              │
  │    op: ":=", value: "pass" }     │                              │
  │─────────────────────────────────►│                              │
  │      201 { id, username, ... }   │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  POST /radusergroup/user-group/  │                              │
  │  { username, groupname: "10Mbps",│                              │
  │    priority: 1 }                 │                              │
  │─────────────────────────────────►│                              │
  │      201 { id, ... }             │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  (Optional) POST /radreply/reply/│                              │
  │  { username, attribute:          │                              │
  │    "Framed-Pool", op: ":=",      │                              │
  │    value: "pool-10m" }           │                              │
  │─────────────────────────────────►│                              │
  │      201 { id, ... }             │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  Store: radcheck_id, group_id    │                              │
  │                                  │                              │
  │                                  │   Customer connects PPPoE    │
  │                                  │◄─────────────────────────────│
  │                                  │   Access-Accept (password OK)│
  │                                  │─────────────────────────────►│
```

**Requirements:**
- **SRS-F01**: Billing MUST create radcheck entry dengan `attribute=Cleartext-Password`, `op=:=`.
- **SRS-F02**: Billing MUST store returned `id` untuk operasi update/delete selanjutnya.
- **SRS-F03**: Billing MUST assign user ke group setelah create user.
- **SRS-F04**: Billing MAY set Framed-Pool via radreply jika instance menggunakan IP pool per-group.
- **SRS-F05**: Username format SHOULD konsisten: `pppoe-<customer_id>` atau sesuai konvensi ISP.

### 3.2. Suspension Flow (Customer Expired)

**Trigger**: Subscription expired, invoice overdue > threshold.

**Sequence:**

```
Billing                     FreeRADIUS API (instance X)         MikroTik NAS
  │                                  │                              │
  │  GET /radacct/status/{username}  │                              │
  │─────────────────────────────────►│                              │
  │  { is_online: true,              │                              │
  │    last_session: { nasip, sid }} │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  POST /disconnect                │                              │
  │  { username, nas_ip, port,       │                              │
  │    radius_secret }               │                              │
  │─────────────────────────────────►│                              │
  │                                  │  radclient disconnect        │
  │                                  │─────────────────────────────►│
  │                                  │  Disconnect-ACK              │
  │                                  │◄─────────────────────────────│
  │  { success: true }               │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  DELETE /users/users/{user_id}   │                              │
  │─────────────────────────────────►│                              │
  │  { message: "deleted" }          │                              │
  │◄─────────────────────────────────│                              │
  │                                  │                              │
  │  DELETE /radusergroup/user-group/ │                              │
  │         {group_id}               │                              │
  │─────────────────────────────────►│                              │
  │  { message: "deleted" }          │                              │
  │◄─────────────────────────────────│                              │
```

**Requirements:**
- **SRS-F10**: Billing MUST check online status before disconnect.
- **SRS-F11**: Billing MUST disconnect user BEFORE deleting from radcheck (otherwise NAS won't know to kick).
- **SRS-F12**: Billing MUST handle disconnect failure gracefully (NAS unreachable → still delete user, NAS will reject next reauth).
- **SRS-F13**: Billing MUST delete radcheck entry to prevent re-authentication.
- **SRS-F14**: Billing SHOULD also delete radusergroup entry for clean state.

### 3.3. Bandwidth Change (Upgrade/Downgrade)

**Trigger**: Customer upgrades/downgrades plan.

**Sequence:**
1. `PUT /radusergroup/user-group/{group_id}` → change groupname.
2. If online: `POST /disconnect` → force reconnect to apply new profile.
3. Customer reconnects → gets new bandwidth from group reply attributes.

**Requirements:**
- **SRS-F20**: Billing MUST update group assignment via PUT.
- **SRS-F21**: Billing MUST disconnect user after bandwidth change to force re-auth with new profile.

### 3.4. Status Query / Reconciliation

**Requirements:**
- **SRS-F30**: Billing SHOULD run reconciliation periodically to detect drift (user exists in billing but not in RADIUS, or vice versa).
- **SRS-F31**: Billing MUST use `GET /users/users` to list all radcheck entries per instance.
- **SRS-F32**: Billing MUST use `GET /radacct/status/{username}` for online/offline check.

---

## 4. Non-Functional Requirements

### 4.1. Performance

- **SRS-NF01**: FreeRADIUS API MUST respond < 500ms untuk CRUD operations.
- **SRS-NF02**: Disconnect MUST timeout dalam 10s (radclient timeout).
- **SRS-NF03**: Billing client MUST set HTTP timeout 15s untuk disconnect, 5s untuk CRUD.

### 4.2. Availability

- **SRS-NF10**: FreeRADIUS API per-instance MUST available saat FreeRADIUS service running.
- **SRS-NF11**: Billing MUST implement per-instance circuit breaker: setelah 3 failures, stop calling instance selama 60s.
- **SRS-NF12**: Billing MUST queue failed operations dan retry saat instance kembali available.

### 4.3. Multi-Instance

- **SRS-NF20**: Billing MUST support N instances (no hardcoded limit).
- **SRS-NF21**: Billing MUST route request ke correct instance berdasarkan customer → instance mapping.
- **SRS-NF22**: Instance credentials MUST stored encrypted di billing config/DB.

---

## 5. Security Requirements

- **SRS-S01**: Komunikasi Billing → RADIUS API MUST over private network (tidak exposed ke public internet).
- **SRS-S02**: HTTP Basic Auth credentials MUST stored encrypted di billing secrets.
- **SRS-S03**: Billing MUST NOT log Basic Auth credentials atau user passwords.
- **SRS-S04**: RADIUS secret (untuk disconnect) MUST stored encrypted.
- **SRS-S05**: Future: migrate ke API key auth atau mTLS untuk service-to-service.

---

## 6. Data Model: Billing Side

Billing system MUST maintain mapping tables:

```sql
-- Instance registry
CREATE TABLE radius_instances (
    id              BIGSERIAL PRIMARY KEY,
    name            VARCHAR(64) NOT NULL UNIQUE,  -- e.g., "client1"
    base_url        VARCHAR(255) NOT NULL,        -- e.g., "http://10.0.0.5:8100/api/v1"
    auth_username   VARCHAR(64) NOT NULL,         -- Swagger/Basic Auth user
    auth_password   TEXT NOT NULL,                -- encrypted
    nas_ip          INET NOT NULL,                -- for disconnect
    radius_secret   TEXT NOT NULL,                -- encrypted, for disconnect
    coa_port        INT NOT NULL DEFAULT 3799,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Customer → RADIUS mapping
CREATE TABLE radius_subscriptions (
    id                  BIGSERIAL PRIMARY KEY,
    customer_id         BIGINT NOT NULL REFERENCES customers(id),
    instance_id         BIGINT NOT NULL REFERENCES radius_instances(id),

    -- RADIUS references
    radius_username     VARCHAR(64) NOT NULL,
    radcheck_id         INT,                -- from API response
    radusergroup_id     INT,                -- from API response
    radreply_pool_id    INT,                -- from API response (Framed-Pool)
    bandwidth_group     VARCHAR(64),        -- e.g., "10Mbps"

    -- Lifecycle
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
        -- pending, active, suspended, terminated
    provisioned_at      TIMESTAMPTZ,
    suspended_at        TIMESTAMPTZ,
    terminated_at       TIMESTAMPTZ,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_rad_subs_customer ON radius_subscriptions(customer_id);
CREATE INDEX idx_rad_subs_instance ON radius_subscriptions(instance_id);
CREATE INDEX idx_rad_subs_status ON radius_subscriptions(status);
CREATE UNIQUE INDEX idx_rad_subs_username_instance
    ON radius_subscriptions(radius_username, instance_id)
    WHERE status IN ('pending', 'active');
```

---

## 7. Golang Client SDK Specification

### 7.1. Package Structure

```
pkg/radius/
├── client.go          // HTTP client, Basic Auth, base URL
├── types.go           // Request/Response structs
├── users.go           // CRUD radcheck (users)
├── groups.go          // CRUD radusergroup
├── reply.go           // CRUD radreply
├── accounting.go      // Query radacct, user status
├── disconnect.go      // CoA disconnect
├── errors.go          // Error types
├── registry.go        // Multi-instance routing
└── client_test.go     // Unit tests
```

### 7.2. Core Types

```go
package radius

import "time"

// --- Radcheck (User) ---

type RadCheckEntry struct {
    ID        int    `json:"id"`
    Username  string `json:"username"`
    Attribute string `json:"attribute"`
    Op        string `json:"op"`
    Value     string `json:"value"`
}

type CreateUserRequest struct {
    Username  string `json:"username"`
    Attribute string `json:"attribute"` // "Cleartext-Password"
    Op        string `json:"op"`        // ":="
    Value     string `json:"value"`     // the password
}

// --- RadUserGroup ---

type RadUserGroup struct {
    ID        int    `json:"id"`
    Username  string `json:"username"`
    Groupname string `json:"groupname"`
    Priority  int    `json:"priority"`
}

type CreateUserGroupRequest struct {
    Username  string `json:"username"`
    Groupname string `json:"groupname"`
    Priority  int    `json:"priority"`
}

// --- RadReply ---

type RadReply struct {
    ID        int    `json:"id"`
    Username  string `json:"username"`
    Attribute string `json:"attribute"`
    Op        string `json:"op"`
    Value     string `json:"value"`
}

type CreateRadReplyRequest struct {
    Username  string `json:"username"`
    Attribute string `json:"attribute"` // e.g., "Framed-Pool"
    Op        string `json:"op"`        // ":="
    Value     string `json:"value"`     // e.g., "pool-10m"
}

// --- Accounting ---

type UserStatus struct {
    Username    string      `json:"username"`
    IsOnline    bool        `json:"is_online"`
    LastSession *RadAcct    `json:"last_session"`
}

type RadAcct struct {
    RadAcctID       int        `json:"radacctid"`
    Username        string     `json:"username"`
    NASIPAddress    string     `json:"nasipaddress"`
    AcctStartTime   *time.Time `json:"acctstarttime"`
    AcctStopTime    *time.Time `json:"acctstoptime"`
    FramedIPAddress string     `json:"framedipaddress"`
    AcctSessionID   string     `json:"acctsessionid"`
}

// --- Disconnect ---

type DisconnectRequest struct {
    Username     string `json:"username"`
    NasIP        string `json:"nas_ip,omitempty"`
    Port         int    `json:"port,omitempty"`
    RadiusSecret string `json:"radius_secret,omitempty"`
}

type DisconnectResponse struct {
    Success    bool   `json:"success"`
    Username   string `json:"username"`
    NasIP      string `json:"nas_ip"`
    Port       int    `json:"port"`
    Output     string `json:"output"`
    ReturnCode int    `json:"return_code"`
}
```

### 7.3. Client Interface

```go
package radius

import (
    "context"
    "net/http"
    "time"
)

type InstanceClient struct {
    baseURL    string
    username   string
    password   string
    nasIP      string
    secret     string
    coaPort    int
    httpClient *http.Client
}

func NewInstanceClient(baseURL, username, password string, opts ...Option) *InstanceClient

// User CRUD
func (c *InstanceClient) CreateUser(ctx context.Context, req CreateUserRequest) (*RadCheckEntry, error)
func (c *InstanceClient) GetUser(ctx context.Context, username string) ([]RadCheckEntry, error)
func (c *InstanceClient) ListUsers(ctx context.Context) ([]RadCheckEntry, error)
func (c *InstanceClient) UpdateUser(ctx context.Context, userID int, req CreateUserRequest) ([]RadCheckEntry, error)
func (c *InstanceClient) DeleteUser(ctx context.Context, userID int) error

// Group CRUD
func (c *InstanceClient) AssignGroup(ctx context.Context, req CreateUserGroupRequest) (*RadUserGroup, error)
func (c *InstanceClient) UpdateGroup(ctx context.Context, groupID int, req UpdateGroupRequest) (*RadUserGroup, error)
func (c *InstanceClient) DeleteGroup(ctx context.Context, groupID int) error
func (c *InstanceClient) ListGroups(ctx context.Context, username string) ([]RadUserGroup, error)

// Reply CRUD
func (c *InstanceClient) CreateReply(ctx context.Context, req CreateRadReplyRequest) (*RadReply, error)
func (c *InstanceClient) DeleteReply(ctx context.Context, replyID int) error

// Accounting & Status
func (c *InstanceClient) GetUserStatus(ctx context.Context, username string) (*UserStatus, error)
func (c *InstanceClient) ListAccounting(ctx context.Context, params AcctParams) ([]RadAcct, error)

// Disconnect
func (c *InstanceClient) DisconnectUser(ctx context.Context, username string) (*DisconnectResponse, error)
```

### 7.4. Multi-Instance Registry

```go
package radius

type Registry struct {
    instances map[string]*InstanceClient // key: instance name
}

func NewRegistry() *Registry
func (r *Registry) Register(name string, client *InstanceClient)
func (r *Registry) Get(name string) (*InstanceClient, error)
func (r *Registry) List() []string
```

---

## 8. Error Handling & Retry Strategy

### 8.1. Error Classification

| HTTP Status | Meaning | Billing Action |
|-------------|---------|---------------|
| 200/201 | Success | Process response |
| 400 | Bad request / validation | Do NOT retry. Fix input. |
| 401 | Auth failed | Do NOT retry. Check credentials. Alert. |
| 404 | User/resource not found | Do NOT retry. Mark as already deleted. |
| 500 | Internal server error | Retry with backoff (max 3x). |
| 503 | radclient not found | Do NOT retry. Alert ops. |
| 504 | Disconnect timeout (NAS unreachable) | Retry once. If still fails, proceed with delete anyway. |
| Timeout | Network issue | Retry with backoff (max 3x). |

### 8.2. Retry Configuration

```go
type RetryConfig struct {
    MaxAttempts   int           // default: 3
    InitialDelay  time.Duration // default: 1s
    MaxDelay      time.Duration // default: 15s
    BackoffFactor float64       // default: 2.0
}
```

---

## 9. Testing Requirements

### 9.1. Unit Tests (Golang Client)

- Mock HTTP server untuk semua API methods.
- Test multi-instance routing.
- Test error handling per HTTP status.
- Test retry logic.

### 9.2. Integration Tests

- Billing → RADIUS API (staging instance) end-to-end.
- Create user → verify via `GET /users/users/{username}`.
- Assign group → verify via `GET /radusergroup/user-group/?username=X`.
- Delete user → verify 404 on subsequent GET.
- Disconnect → verify radclient output.

### 9.3. Contract Tests

- RADIUS API MUST maintain backward compatibility untuk response format.
- Breaking changes MUST communicated 2 minggu sebelumnya.

---

## 10. Migration & Rollout Plan

### Phase 1: Documentation & Preparation
1. Export Swagger/OpenAPI spec dari setiap instance (`/docs` → JSON).
2. Verify semua instance accessible dari billing server (network).
3. Create instance registry di billing DB.
4. Generate/store credentials securely.

### Phase 2: Golang Client Development
1. Implement `pkg/radius/` client SDK.
2. Unit test dengan mock server.
3. Integration test terhadap staging RADIUS instance.

### Phase 3: Billing Integration
1. Implement provisioning flow di billing business logic.
2. Implement suspension flow.
3. Implement bandwidth change flow.
4. Implement reconciliation job (daily cron).

### Phase 4: Production Rollout
1. Deploy billing with RADIUS integration (feature flag OFF).
2. Enable for 1 test customer per instance.
3. Verify user appears in `radcheck` + can authenticate via PPPoE.
4. Gradual rollout ke semua customer.

---

## 11. Comparison: VPN Secret Generator vs FreeRADIUS Integration

| Aspect | VPN Secret Generator | FreeRADIUS API |
|--------|---------------------|----------------|
| Auth | API Key (Bearer) | HTTP Basic Auth |
| Instances | Single | Multiple (per-tenant) |
| Protocol | HTTPS | HTTP (private network) |
| User model | `vpn_secrets` (username+password+profile) | `radcheck` (username+attribute+op+value) |
| Group/Profile | PPP Profile (RouterOS) | `radusergroup` → `radgroupreply` |
| Disconnect | Delete secret = RouterOS removes | Explicit `radclient` disconnect |
| IP allocation | Auto from NAS pool | Framed-Pool via radreply |
| Rate limit | Per-tier (60/300/120 rpm) | None (private network) |

Billing Golang client MUST implement **both** integrations as separate packages:
- `pkg/vpnsg/` — VPN Secret Generator client
- `pkg/radius/` — FreeRADIUS API client

---

## 12. Open Questions

| # | Question | Status |
|---|----------|--------|
| Q1 | Apakah perlu HTTPS/TLS untuk RADIUS API? (currently HTTP) | Recommended via reverse proxy |
| Q2 | Apakah billing perlu create NAS entry via API? | Probably not — NAS managed by network engineer |
| Q3 | Bagaimana handle instance yang down saat billing batch? | Queue + retry + alert |
| Q4 | Apakah perlu "suspend" mode (disable user tanpa delete)? | Bisa via change password ke random / remove from group |
| Q5 | Concurrent disconnect limit? (radclient is blocking) | uvicorn 4 workers = max 4 concurrent disconnects |

---

## 13. Revision History

| Versi | Tanggal | Perubahan |
|-------|---------|-----------|
| 1.0.0 | 2026-05-05 | Initial SRS draft |
