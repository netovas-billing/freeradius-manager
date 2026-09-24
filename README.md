# FreeRADIUS Multi-Instance Manager

Kumpulan tooling untuk mengelola beberapa instance FreeRADIUS + REST API secara otomatis di satu server.

Repo ini berisi **dua implementasi yang co-exist**:

| Tool | Bahasa | Untuk apa |
|------|--------|-----------|
| `radius-manager.sh` | Bash | Tool manual ops (legacy + tetap dipakai). Berjalan langsung di host RADIUS VM. |
| `radius-manager-api` | Go (di `cmd/radius-manager-api/`) | HTTP control plane untuk dipanggil ERP/billing system. Mendukung Docker dev stack untuk testing lokal dari Mac/Windows. |

Keduanya **menulis ke state file yang sama** (`.instance_<nama>` + `.port_registry`) dengan `flock(2)`, jadi bisa dipakai bergantian. Lihat:

- [`docs/PRD.md`](docs/PRD.md) — Product Requirements (Billing → freeradius-api integration baseline).
- [`docs/SRS.md`](docs/SRS.md) — Software Requirements untuk integrasi billing.
- [`docs/SRS-RadiusManagerAPI.md`](docs/SRS-RadiusManagerAPI.md) — SRS extension untuk Go control plane (v0.2.0).
- [`cmd/radius-manager-api/README.md`](cmd/radius-manager-api/README.md) — Quick start + Docker dev stack.

## Quick start

```bash
# Run unit tests (no infra required)
make test

# Bring up the full Docker stack (rm-api + freeradius + mariadb)
make docker-up

# End-to-end smoke test (create → verify → delete → audit log check)
make e2e

# See all targets
make help
```

## Pasang di server (produksi)

Debian/Ubuntu bersih — **`install.sh`**, bukan `docker compose`. Berkas
`docker-compose.dev.yml` hanya untuk uji coba di laptop (`make docker-up`).

```bash
sudo RM_INSTALL_BIND=0.0.0.0:9000 bash install.sh
```

Installer memasang MariaDB + FreeRADIUS + Go, membangun biner, menulis unit
systemd, lalu menguji `/v1/server/health` sendiri. Idempoten.

Setelah itu isi **`/etc/radius-manager-api/env`** (dibuat installer, 0600,
tidak pernah ditimpa saat install ulang):

| Variabel | Kenapa penting |
|---|---|
| `RM_API_API_PUBLISH_IP` | Alamat yang **diumumkan ke backend**. Nilai ini tersimpan ke `radius_servers.url` setiap instance baru. Kosong → jatuh ke `0.0.0.0`, provisioning tetap "berhasil" tapi instance-nya tak bisa dihubungi siapa pun. Isi IP publik concentrator bila backend masuk lewat DSTNAT. |
| `RM_API_LISTEN` | Alamat bind. **Portnya bagian dari blok yang dialokasikan ERP** (= `base`, lihat di bawah). Jangan diikat langsung ke IP tunnel — alamat itu baru ada setelah VPN naik, sehingga service gagal start saat boot. Pakai `0.0.0.0` + firewall. |
| `RM_API_CAPACITY_MAX` | Jumlah instance yang boleh hidup di mesin ini. Ikut menentukan rentang port yang perlu di-NAT di concentrator — kalau dinaikkan, paste ulang skrip concentrator-nya. |
| `RM_API_API_PORT_START` | Awal blok port HTTP freeradius-api di mesin ini (naik satu-satu per instance). **Angkanya ditentukan ERP** — lihat di bawah. Di luar `1024–64000` atau bukan angka → ditolak, kembali ke bawaan `8100` dengan `WARN`; nilai efektifnya dicatat di log tiap service start dan diumumkan di `/v1/server/info`. |

> **Blok portnya ditentukan ERP, jangan dikarang.** Satu VPN concentrator bisa
> menaungi lebih dari satu VM RADIUS, dan backend menjangkau tiap VM lewat
> DSTNAT di IP publik concentrator. NAT-nya 1:1 (tanpa `to-ports`) karena URL
> instance yang diterbitkan RM-API sudah memuat nomor portnya sendiri. Karena
> itu ERP yang mengalokasikan blok tiap VM:
>
> | | Nilai |
> |---|---|
> | `base` | `20000 + k*1000` (`k` = urutan VM di concentrator: 0, 1, 2, …) |
> | `RM_API_LISTEN` | `base` → `20000`, `21000`, `22000`, … |
> | `RM_API_API_PORT_START` | `base + 100` → `20100`, `21100`, `22100`, … |
> | lebar blok VM | `1000` port — `[base, base+999]` |
> | rentang port instance | `[base+100, base+999]` = **900** port (100 port pertama milik `RM_API_LISTEN` + kontrol) |
>
> **Satu-satunya sumber nilai yang benar** adalah skrip pemasangan yang
> diterbitkan menu **Server RADIUS Manager → Setup Script** di ERP: skrip itu
> mengisi `RM_API_LISTEN` + `RM_API_API_PORT_START` sesuai aturan DSTNAT yang
> dipasang di concentrator. Mengisi angka lain (mis. `8100`) membuat instance
> lahir di luar rentang yang di-NAT — provisioning tetap dilaporkan **berhasil**,
> tapi permintaan backend mendarat di VM yang keliru atau tidak sampai sama
> sekali, tanpa satu galat pun.
>
> Verifikasi blok yang benar-benar dipakai mesin ini lewat `/v1/server/info`
> (field `listen` dan `api_port_start`).
>
> Port UDP RADIUS (10000–59000) tidak terpengaruh: lewat tunnel langsung, tidak
> pernah di-NAT, jadi tidak pernah bertabrakan antar-VM.

```bash
sudo nano /etc/radius-manager-api/env
sudo systemctl restart radius-manager-api
```

Kalau mesin ini hidup di dalam VPN concentrator, sambungkan tunnelnya dengan
[`scripts/vpn-client-setup.sh`](scripts/vpn-client-setup.sh).

## Persyaratan

- OS: Ubuntu/Debian
- Root access
- Paket berikut (auto-install jika belum ada):
  - `freeradius`, `freeradius-utils`, `freeradius-mysql`
  - `mariadb-client`
  - `python3`, `python3-venv`
  - `git`, `openssl`, `iproute2`

---

## radius-manager.sh

Script utama untuk membuat, mengelola, dan menghapus instance FreeRADIUS beserta database dan REST API-nya.

### Yang dilakukan saat `create`

1. Membuat database & user MariaDB khusus instance
2. Mengimport schema FreeRADIUS ke database
3. Membuat SQL module (`sql_<nama>`) dan EAP module (`eap_<nama>`)
4. Membuat virtual server & inner tunnel dengan port acak yang belum terpakai
5. Meng-clone repo [freeradius-api](https://github.com/heirro/freeradius-api) ke `/root/<nama>-api/`
6. Mengisi `.env` API dengan credentials database dan Swagger secara otomatis
7. Mengisi credentials di `autoclearzombie.sh` + membuat cron job (tiap 30 menit)
8. Mengisi credentials di `autobackups3.sh` + membuat cron job backup S3 (tiap hari jam 02:00)
9. Membuat dan mengaktifkan systemd service untuk REST API

### Port yang di-assign per instance

| Port | Fungsi |
|------|--------|
| `AUTH_PORT` | RADIUS Authentication |
| `AUTH_PORT + 1` | RADIUS Accounting |
| `AUTH_PORT + 2000` | CoA (Change of Authorization) |
| `AUTH_PORT + 5000` | Inner Tunnel (EAP) |
| `API_PORT` | REST API (mulai dari `RM_API_API_PORT_START`, mis. `20100`; bawaan `8100` bila tidak diisi) |

Port dipilih secara acak (range 10000–59000) dan dicek agar tidak bentrok dengan port yang sudah dipakai.

`radius-manager.sh` dan `radius-manager-api` berbagi `.port_registry`, jadi blok
port API-nya harus sama di satu mesin. Skrip ini **memuat sendiri**
`/etc/radius-manager-api/env` kalau ada dan bisa dibaca, jadi cukup:

```bash
sudo bash radius-manager.sh create <nama>
```

Env yang sudah di-export di shell tetap menang atas isi berkas (berguna saat uji
coba), dan nilai yang bukan angka atau di luar `1024–64000` diklem ke bawaan
dengan peringatan — sama seperti sisi Go (termasuk `0020100` yang diterima dan
`20 100` yang ditolak). Hanya kunci berawalan `RM_API_` yang diambil dari berkas
itu, supaya kunci senama (`PORT_REGISTRY`, `DB_HOST`, …) tidak membajak skrip;
berkas yang ADA tapi tak terbaca (0600 root, dijalankan non-root) memberi `WARN`
alih-alih diam-diam memakai bawaan. Alokasi berhenti di
`api_port_start + min(900, RM_API_CAPACITY_MAX)` — 900, bukan 1000, karena pagar
selebar 1000 dari `base+100` merambah ke `[base+1000, base+1099]` yang MILIK VM
TETANGGA (dimulai tepat di `RM_API_LISTEN`-nya): lebih baik
gagal terang-terangan daripada menerbitkan instance di port yang tidak ikut
di-DSTNAT atau yang mendarat di VM lain.

Uji perilaku ini tanpa menyentuh sistem: `./scripts/test-port-block.sh`.

### File info instance

Setiap instance menyimpan info di `/etc/freeradius/3.0/.instance_<nama>`, berisi:
- Credentials database
- Port yang digunakan
- URL API

---

### Perintah

```bash
sudo bash radius-manager.sh <command> [options]
```

#### `create`

```bash
sudo bash radius-manager.sh create <nama> [db_pass]
```

Membuat instance baru lengkap (FreeRADIUS + database + REST API).

- `<nama>` — dipakai sebagai nama instance, database, dan user MariaDB
- `[db_pass]` — opsional, password database (auto-generate jika tidak diisi)

```bash
# Contoh
sudo bash radius-manager.sh create replaymedia
sudo bash radius-manager.sh create baimnabil MyPass123
```

#### `delete`

```bash
sudo bash radius-manager.sh delete <nama> [--with-db]
```

Menghapus config FreeRADIUS dan API service.

- `--with-db` — hapus juga database & user MariaDB (ada konfirmasi)

```bash
sudo bash radius-manager.sh delete replaymedia
sudo bash radius-manager.sh delete replaymedia --with-db
```

#### `start` / `stop`

```bash
sudo bash radius-manager.sh start <nama>
sudo bash radius-manager.sh stop  <nama>
```

Mengaktifkan atau menonaktifkan instance (tanpa menghapus config).

#### `restart`

```bash
sudo bash radius-manager.sh restart
```

Restart FreeRADIUS (semua instance aktif ikut restart).

#### `list`

```bash
sudo bash radius-manager.sh list
```

Menampilkan semua instance beserta status port dan service API.

#### `info`

```bash
sudo bash radius-manager.sh info <nama>
```

Menampilkan detail credentials dan port instance.

#### `test`

```bash
sudo bash radius-manager.sh test <nama>
```

Mengecek port dan mengirim Access-Request test ke instance.

#### `test-disconnect`

```bash
sudo bash radius-manager.sh test-disconnect <nama> <username> <session-id>
```

Mengirim CoA Disconnect-Request ke instance.

---

### Menambahkan NAS (MikroTik/Router)

NAS didaftarkan langsung ke tabel `nas` di database instance:

```sql
USE <nama>;
INSERT INTO nas (nasname, shortname, type, secret, server)
VALUES ('IP_MIKROTIK', 'nama_nas', 'other', 'secret_radius', '<nama>');
```

### Konfigurasi S3 Backup

Ubah variabel berikut di bagian CONFIGURATION `radius-manager.sh` sesuai setup rclone:

```bash
S3_REMOTE="ljns3"          # Nama remote rclone
S3_BUCKET="backup-db"      # Nama bucket
S3_BACKUP_ROOT="radiusdb"  # Root path di bucket (folder per instance: radiusdb/<nama>)
S3_BACKUP_SCHEDULE="0 2 * * *"  # Jadwal cron (default: tiap hari jam 02:00)
```

Pastikan `rclone` sudah terinstall dan remote `ljns3` sudah dikonfigurasi:

```bash
apt install rclone
rclone config  # Setup remote ljns3
```

---

## update.sh

Script untuk meng-update semua instance API secara otomatis via `git pull`, cocok dijalankan sebagai cron job.

### Yang dilakukan

1. Membaca semua instance dari `/etc/freeradius/3.0/.instance_*`
2. Menjalankan `git pull` di direktori API masing-masing instance
3. Jika **ada update**:
   - Re-patch credentials di `autoclearzombie.sh` (agar tidak tertimpa hasil pull)
   - Re-patch credentials di `autobackups3.sh` (agar tidak tertimpa hasil pull)
   - Restart systemd service instance tersebut
4. Jika **Already up to date** — skip, service tidak diganggu

### Setup Cron Job

```bash
chmod +x /root/update-api.sh

# Buka crontab
crontab -e
```

Tambahkan baris berikut (tiap jam):

```
0 * * * * /root/update-api.sh >> /var/log/update-api.log 2>&1
```

### Melihat Log Update

```bash
tail -f /var/log/update-api.log
```

Contoh output:

```
[2026-03-13 10:00:01] [INFO]  --- Checking: replaymedia (/root/replaymedia-api) ---
[2026-03-13 10:00:02] [INFO]  Git pull: Already up to date.
[2026-03-13 10:00:02] [INFO]  Tidak ada perubahan, skip restart
[2026-03-13 10:00:03] [INFO]  --- Checking: baimnabil (/root/baimnabil-api) ---
[2026-03-13 10:00:05] [INFO]  Git pull: Updating a3f1c2d..9b8e4f1
[2026-03-13 10:00:05] [OK]    autoclearzombie.sh di-patch ulang
[2026-03-13 10:00:07] [OK]    Service baimnabil-api berhasil di-restart
[2026-03-13 10:00:07] [INFO]  --- Done ---
```

---

## Struktur File

```
/etc/freeradius/3.0/
├── .instance_<nama>          # Info & credentials setiap instance
├── .port_registry            # Registry port yang sudah terpakai
├── mods-available/
│   ├── sql_<nama>            # SQL module per instance
│   └── eap_<nama>            # EAP module per instance
├── mods-enabled/
│   ├── sql_<nama> -> ...
│   └── eap_<nama> -> ...
├── sites-available/
│   ├── <nama>                # Virtual server per instance
│   └── inner-tunnel-<nama>
└── sites-enabled/
    ├── <nama> -> ...
    └── inner-tunnel-<nama> -> ...

/root/
├── radius-manager.sh
├── update-api.sh
└── <nama>-api/               # Clone repo API per instance
    ├── .env                  # Credentials (auto-generated, chmod 600)
    ├── autoclearzombie.sh    # Auto clear zombie sessions (auto-patched)
    └── venv/

/etc/systemd/system/
└── <nama>-api.service        # Service systemd per instance

/var/log/freeradius/
└── radacct-<nama>/           # Log accounting per instance

/var/log/
└── update-api.log            # Log git pull & restart otomatis
```
