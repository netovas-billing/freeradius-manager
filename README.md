# FreeRADIUS Multi-Instance Manager

Kumpulan script untuk mengelola beberapa instance FreeRADIUS + REST API secara otomatis di satu server.

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
7. Mengisi credentials di `autoclearzombie.sh` secara otomatis
8. Membuat dan mengaktifkan systemd service untuk REST API

### Port yang di-assign per instance

| Port | Fungsi |
|------|--------|
| `AUTH_PORT` | RADIUS Authentication |
| `AUTH_PORT + 1` | RADIUS Accounting |
| `AUTH_PORT + 2000` | CoA (Change of Authorization) |
| `AUTH_PORT + 5000` | Inner Tunnel (EAP) |
| `API_PORT` | REST API (mulai dari 8100) |

Port dipilih secara acak (range 10000–59000) dan dicek agar tidak bentrok dengan port yang sudah dipakai.

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

---

## update-api.sh

Script untuk meng-update semua instance API secara otomatis via `git pull`, cocok dijalankan sebagai cron job.

### Yang dilakukan

1. Membaca semua instance dari `/etc/freeradius/3.0/.instance_*`
2. Menjalankan `git pull` di direktori API masing-masing instance
3. Jika **ada update**:
   - Re-patch credentials di `autoclearzombie.sh` (agar tidak tertimpa hasil pull)
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
