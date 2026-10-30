#!/usr/bin/env bash
# Penjaga pertanyaan RM_API_API_PUBLISH_IP di installer.
#
# Kenapa ada: tanpa nilai itu setiap instance lahir dengan URL
# http://0.0.0.0:<port>, provisioning tetap dilaporkan sukses, dan peringatan
# di ringkasan installer terlewat (laporan pemilik 30 Okt 2026). Installer
# kini MENANYAKANNYA bila mesin punya IP publik — pemilahan "publik" dan
# validasi jawabannya dikunci di sini.
set -uo pipefail
cd "$(dirname "$0")/.."
gagal=0

# Fungsi diambil LANGSUNG dari install.sh, bukan disalin ke sini.
# shellcheck disable=SC1090
source <(sed -n '/^# >>> publish-ip/,/^# <<< publish-ip/p' install.sh)
type ip_publik_v4 >/dev/null 2>&1 || { echo "[GAGAL] blok publish-ip tak ditemukan di install.sh"; exit 1; }

publik()   { if ip_publik_v4 "$1"; then echo "[ok]   $1 publik"; else echo "[GAGAL] $1 mestinya publik"; gagal=1; fi; }
bukan()    { if ip_publik_v4 "$1"; then echo "[GAGAL] $1 mestinya BUKAN publik"; gagal=1; else echo "[ok]   $1 bukan publik"; fi; }
sah()      { if publish_ip_sah "$1"; then echo "[ok]   '$1' sah"; else echo "[GAGAL] '$1' mestinya sah"; gagal=1; fi; }
tolak()    { if publish_ip_sah "$1"; then echo "[GAGAL] '$1' mestinya ditolak"; gagal=1; else echo "[ok]   '$1' ditolak"; fi; }

publik 103.242.104.67
publik 38.58.112.29
publik 8.8.8.8
bukan 10.0.0.2
bukan 172.16.0.2
bukan 172.31.255.1
publik 172.32.0.1
bukan 192.168.1.3
bukan 100.64.0.1
bukan 127.0.0.1
bukan 169.254.1.1
bukan 0.0.0.0
bukan 224.0.0.1
bukan 300.1.1.1
bukan "bukan-ip"

sah 103.242.104.67
sah rm.contoh.id
tolak 0.0.0.0
tolak 127.0.0.1
tolak localhost
tolak ""
tolak "http://103.242.104.67"
tolak "103.242.104.67:9000"
tolak "1.2.3"

# Pertanyaannya harus membaca dari /dev/tty: installer dijalankan juga lewat
# `curl … | sudo bash`, dan di sana stdin adalah skrip itu sendiri.
if grep -q 'read -r -p "  RM_API_API_PUBLISH_IP' install.sh && grep -q '</dev/tty || publish_ip=""' install.sh; then
    echo "[ok]   pertanyaan membaca /dev/tty"
else
    echo "[GAGAL] pertanyaan tidak membaca /dev/tty"; gagal=1
fi

[ "$gagal" -eq 0 ] && echo "Semua uji publish-ip installer lolos." || echo "ADA YANG GAGAL."
exit "$gagal"
