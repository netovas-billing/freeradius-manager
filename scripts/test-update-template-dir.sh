#!/usr/bin/env bash
# Pencarian direktori template Go di update.sh.
#
# KENAPA ADA: tiga tempat memakai nama variabel dan bawaan yang BERBEDA —
# update.sh & radius-manager.sh memakai API_GO_TEMPLATE_DIR (/var/lib/radius-manager/...),
# sedangkan control plane Go memakai RM_API_GO_TEMPLATE_DIR (/var/lib/radius-manager-API/...).
# Beda satu kata, dan akibatnya update.sh MELEWATI SEMUA INSTANCE di mesin yang
# instance-nya dibuat lewat ERP. Tes ini mengunci bahwa keduanya ketemu.
set -uo pipefail
cd "$(dirname "$0")/.."

# Ambil hanya blok fungsinya, tanpa menjalankan sisa update.sh.
FN=$(mktemp); trap 'rm -rf "$FN" "$TMP"' EXIT
sed -n '/^baca_setelan_host() {/,/^}$/p;/^pilih_template_dir() {/,/^}$/p' update.sh > "$FN"
TMP=$(mktemp -d)

gagal=0
uji() {
  local nama="$1" mau="$2" dapat="$3"
  if [ "$dapat" = "$mau" ]; then echo "[ok]   $nama"
  else echo "[GAGAL] $nama: dapat '$dapat', mau '$mau'"; gagal=1; fi
}

jalan() { # $1=env file, sisanya diekspor pemanggil
  ( set +u; RM_API_ENV_FILE="$1"; . "$FN"; pilih_template_dir )
}

mkdir -p "$TMP/rmapi/.git" "$TMP/bash/.git" "$TMP/kustom/.git" "$TMP/env/.git"
: > "$TMP/kosong.env"
printf 'RM_API_GO_TEMPLATE_DIR=%s\n' "$TMP/env" > "$TMP/isi.env"

# 1. Pemanggil menyebut jalurnya -> menang atas segalanya.
out=$(API_GO_TEMPLATE_DIR="$TMP/kustom" jalan "$TMP/isi.env")
uji "API_GO_TEMPLATE_DIR menang" "$TMP/kustom" "$out"

# 2. Berkas setelan per-host dipakai kalau pemanggil diam.
out=$(jalan "$TMP/isi.env")
uji "RM_API_GO_TEMPLATE_DIR dari berkas setelan" "$TMP/env" "$out"

# 3. Variabel lingkungan RM_API_* juga dihormati.
out=$(RM_API_GO_TEMPLATE_DIR="$TMP/kustom" jalan "$TMP/kosong.env")
uji "RM_API_GO_TEMPLATE_DIR dari lingkungan" "$TMP/kustom" "$out"

# 4. Tanpa petunjuk apa pun: yang PUNYA .git yang dipilih. Ini kasus yang dulu
#    gagal — instance dibuat RM-API, update.sh mencari di jalur bash.
#    Nilainya dipatok PERSIS: itu sekaligus mengunci bahwa jalur RM-API ADA di
#    daftar kandidat DAN dicoba LEBIH DULU. Tanpa patokan ini, menghapus
#    kandidat RM-API tidak membuat tes merah — jalur bash akan mengisi tempatnya
#    dan seolah-olah semuanya baik.
out=$(jalan "$TMP/kosong.env")
uji "tanpa petunjuk -> jalur RM-API dicoba lebih dulu" \
  "/var/lib/radius-manager-api/freeradius-api-go-template" "$out"

# 5. Jalur yang disebut tapi TANPA .git tidak boleh dipilih diam-diam.
out=$(API_GO_TEMPLATE_DIR="$TMP/tak-ada" jalan "$TMP/isi.env")
uji "jalur tanpa .git dilewati, jatuh ke berkas setelan" "$TMP/env" "$out"

# 6. Tak ada satu pun kandidat ber-.git -> kembalikan kandidat PERTAMA dan
#    laporkan gagal, supaya pesan galatnya menyebut jalur yang masuk akal.
out=$(API_GO_TEMPLATE_DIR="$TMP/tak-ada" jalan "$TMP/kosong.env"); rc=$?
uji "tanpa kandidat: sebut jalur yang diminta" "$TMP/tak-ada" "$out"
[ "$rc" -ne 0 ] && echo "[ok]   tanpa kandidat: status keluar bukan 0" \
  || { echo "[GAGAL] tanpa kandidat: status keluar 0"; gagal=1; }

[ "$gagal" -eq 0 ] && echo "Semua uji pencarian template dir lolos." || echo "ADA YANG GAGAL."
exit "$gagal"
