#!/usr/bin/env bash
# scripts/test-port-block.sh - uji perilaku blok port API di radius-manager.sh
# TANPA menjalankan skripnya sampai menyentuh sistem.
#
# Yang dijaga di sini semuanya berakhir sebagai KEGAGALAN SENYAP kalau lepas:
# instance lahir di port yang tidak ikut di-DSTNAT concentrator, provisioning
# tetap dilaporkan sukses, dan tidak ada galat di mana pun.
#
#   1. berkas setelan per-host dimuat otomatis (dulu cuma komentar, sehingga
#      skrip bash memakai 8100 sementara RM-API Go memakai blok lain - padahal
#      keduanya menulis .port_registry yang sama);
#   2. env yang sudah di-export di shell menang atas isi berkas;
#   3. berkas ADA tapi tak terbaca (0600 root, dijalankan non-root) harus
#      BERBUNYI, bukan diam-diam jatuh ke bawaan 8100;
#   4. HANYA kunci RM_API_ yang diambil - sourcing utuh dulu membiarkan kunci
#      senama di berkas env (PORT_REGISTRY, DB_HOST, ...) membajak skrip;
#   5. klem nilai persis sama dengan sisi Go, termasuk "0020100" dan "20 100";
#   6. penelusuran port tidak pernah keluar dari rentang mesin ini, dan
#      rentangnya 900 (base+100..base+999), bukan 1000 - 100 port di atasnya
#      milik VM TETANGGA;
#   7. capacity_max mempersempit rentang, persis seperti di Go.
#
# Usage: ./scripts/test-port-block.sh    (tidak butuh root, tidak butuh Docker)
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_DIR/radius-manager.sh"
TMP="$(mktemp -d)"
trap 'chmod -R u+rwX "$TMP" 2>/dev/null; rm -rf "$TMP"' EXIT

if [[ -t 1 ]]; then
    G=$'\033[32m'; R=$'\033[31m'; N=$'\033[0m'
else
    G=""; R=""; N=""
fi
FAILED=0
ok()   { printf "%s[ok]%s   %s\n" "$G" "$N" "$*"; }
fail() { printf "%s[FAIL]%s %s\n" "$R" "$N" "$*" >&2; FAILED=1; }

# Potong bagian konfigurasi skrip (sampai penanda akhir blok port API) supaya
# bisa dijalankan sendiri, lalu cetak nilai efektifnya.
MARKER='akhir konfigurasi blok port API'
HEADER_END="$(grep -n "$MARKER" "$SCRIPT" | head -n1 | cut -d: -f1)"
[ -n "$HEADER_END" ] || { fail "tidak menemukan penanda '$MARKER' di $SCRIPT"; exit 1; }
head -n "$HEADER_END" "$SCRIPT" > "$TMP/header.sh"
cat >> "$TMP/header.sh" <<'INNER'
echo "EFFECTIVE=$PORT_API_START"
echo "CAPACITY=$PORT_API_CAPACITY_MAX"
echo "WIDTH=$PORT_API_BLOCK_WIDTH"
echo "REGISTRY=$PORT_REGISTRY"
INNER

# run_header <nilai-RM_API_API_PORT_START> <berkas-env> -> seluruh keluaran
run_header() {
    RM_API_ENV_FILE="$2" RM_API_API_PORT_START="$1" bash "$TMP/header.sh" 2>/dev/null
}
# effective <nilai-env> <berkas-env> -> nilai PORT_API_START yang dipakai
effective() {
    run_header "$1" "${2:-/nonexistent}" | sed -n 's/^EFFECTIVE=//p'
}
expect_effective() {
    local desc=$1 want=$2 got
    got="$(effective "$3" "${4:-/nonexistent}")"
    if [ "$got" = "$want" ]; then ok "$desc (=$got)"; else fail "$desc: got '$got' want '$want'"; fi
}

NOFILE="/nonexistent-radius-manager-env"

# ---------------------------------------------------------------------------
# 1. Tabel klem nilai - HARUS sama persis dengan TestParseAPIPortStartParity
#    di internal/config/config_test.go. Dua sisi menulis satu .port_registry;
#    kalau satu sisi menerima nilai yang ditolak sisi lain, satu mesin memakai
#    DUA blok port berbeda dan port bertabrakan tanpa galat.
#    Bentuk nilai -> PORT_API_START efektif (bawaan 8100).
# ---------------------------------------------------------------------------
expect_effective "'' (tidak diisi) -> bawaan"          8100  ""
expect_effective "' ' (hanya spasi) -> bawaan"         8100  " "
expect_effective "'8100 ' (spasi di belakang)"         8100  "8100 "
expect_effective "' 8100' (spasi di depan)"            8100  " 8100"
expect_effective "'0020100' (nol di depan, desimal)"   20100 "0020100"
expect_effective "'20 100' (spasi di TENGAH) -> tolak" 8100  "20 100"
expect_effective "'delapanribu' -> bawaan"             8100  "delapanribu"
expect_effective "'0' -> bawaan"                       8100  "0"
expect_effective "'1023' (di bawah batas) -> bawaan"   8100  "1023"
expect_effective "'1024' (batas bawah) dipakai"        1024  "1024"
expect_effective "'64000' (batas atas) dipakai"        64000 "64000"
expect_effective "'64100' (di luar batas) -> bawaan"   8100  "64100"
expect_effective "'70000' (di luar batas) -> bawaan"   8100  "70000"
expect_effective "blok kanonik ERP dipakai"            20100 "20100"

# Nilai bukan angka wajib berbunyi, bukan diam-diam dipakai-bawaan.
if RM_API_ENV_FILE="$NOFILE" RM_API_API_PORT_START="delapanribu" bash "$TMP/header.sh" 2>&1 >/dev/null |
        grep -q "bukan angka"; then
    ok "nilai bukan angka memberi peringatan"
else
    fail "nilai bukan angka TIDAK memberi peringatan"
fi
if RM_API_ENV_FILE="$NOFILE" RM_API_API_PORT_START="20 100" bash "$TMP/header.sh" 2>&1 >/dev/null |
        grep -q "bukan angka"; then
    ok "spasi di tengah memberi peringatan"
else
    fail "spasi di tengah TIDAK memberi peringatan"
fi

# ---------------------------------------------------------------------------
# 2. Berkas setelan per-host dimuat otomatis + env shell menang.
# ---------------------------------------------------------------------------
printf 'RM_API_API_PORT_START=20100\n' > "$TMP/env"
got="$(effective "" "$TMP/env")"
[ "$got" = "20100" ] && ok "berkas setelan per-host dimuat otomatis (=$got)" \
    || fail "berkas setelan per-host tidak dimuat: got '$got' want 20100"

got="$(effective "21100" "$TMP/env")"
[ "$got" = "21100" ] && ok "env shell menang atas berkas (=$got)" \
    || fail "env shell kalah dari berkas: got '$got' want 21100"

# capacity_max ikut dibaca dari berkas yang sama (dulu tidak pernah dibaca,
# jadi skrip mengalokasikan sampai ujung blok padahal ERP hanya mem-NAT
# sebanyak capacity).
printf 'RM_API_API_PORT_START=20100\nRM_API_CAPACITY_MAX=50\n' > "$TMP/env-cap"
got="$(run_header "" "$TMP/env-cap" | sed -n 's/^CAPACITY=//p')"
[ "$got" = "50" ] && ok "RM_API_CAPACITY_MAX dibaca dari berkas (=$got)" \
    || fail "RM_API_CAPACITY_MAX tidak dibaca: got '$got' want 50"

got="$(run_header "" "$NOFILE" | sed -n 's/^CAPACITY=//p')"
[ "$got" = "50" ] && ok "capacity bawaan sama dengan sisi Go (=$got)" \
    || fail "capacity bawaan: got '$got' want 50"

got="$(run_header "" "$NOFILE" | sed -n 's/^WIDTH=//p')"
[ "$got" = "900" ] && ok "lebar rentang instance = 900 (base+100..base+999)" \
    || fail "lebar rentang: got '$got' want 900 (1000 = 100 port milik VM tetangga)"

# ---------------------------------------------------------------------------
# 3. Berkas ADA tapi tidak terbaca -> WARN yang menyebut berkasnya.
#    (0600 root: eksekusi non-root dulu diam-diam memakai blok bawaan.)
# ---------------------------------------------------------------------------
if [ "$(id -u)" -eq 0 ]; then
    ok "uji berkas tak terbaca dilewati (dijalankan sebagai root)"
else
    printf 'RM_API_API_PORT_START=20100\n' > "$TMP/env-noread"
    chmod 000 "$TMP/env-noread"
    warn_out="$(RM_API_ENV_FILE="$TMP/env-noread" RM_API_API_PORT_START="" bash "$TMP/header.sh" 2>&1 >/dev/null)"
    if printf '%s' "$warn_out" | grep -q "env-noread" &&
       printf '%s' "$warn_out" | grep -qi "sudo"; then
        ok "berkas ada tapi tak terbaca -> WARN menyebut berkas + sudo"
    else
        fail "berkas tak terbaca TIDAK memberi WARN yang jelas: '$warn_out'"
    fi
    chmod 600 "$TMP/env-noread"
fi

# ---------------------------------------------------------------------------
# 4. Sourcing utuh tidak boleh membajak skrip: kunci NON-RM_API_ di berkas env
#    harus tidak berpengaruh. PORT_REGISTRY yang dibajak = pendaftaran port
#    ditulis ke berkas lain, dua jalur membagikan port yang sama, tabrakan
#    senyap.
# ---------------------------------------------------------------------------
cat > "$TMP/env-hijack" <<'INNER'
PORT_REGISTRY=/tmp/jahat
DB_HOST=jahat.example
API_DIR_BASE=/tmp/jahat-api
FREERADIUS_DIR=/tmp/jahat-fr
RM_API_API_PORT_START=20100
INNER
hijack_out="$(run_header "" "$TMP/env-hijack")"
got="$(printf '%s' "$hijack_out" | sed -n 's/^REGISTRY=//p')"
case "$got" in
    /tmp/jahat*) fail "PORT_REGISTRY dibajak berkas env: '$got'" ;;
    *) ok "kunci non-RM_API_ di berkas env tidak berpengaruh (registry=$got)" ;;
esac
got="$(printf '%s' "$hijack_out" | sed -n 's/^EFFECTIVE=//p')"
[ "$got" = "20100" ] && ok "kunci RM_API_ tetap terbaca dari berkas yang sama (=$got)" \
    || fail "kunci RM_API_ tidak terbaca: got '$got' want 20100"

# Bentuk baris yang lazim di berkas env harus dibaca seperti saat di-source:
# kutip pembungkus, komentar di ujung baris, `export`, dan baris komentar.
cat > "$TMP/env-forms" <<'INNER'
# komentar di awal berkas
export RM_API_API_PORT_START="21100"   # blok VM ke-1
RM_API_CAPACITY_MAX=60 # jumlah instance
INNER
forms_out="$(run_header "" "$TMP/env-forms")"
got="$(printf '%s' "$forms_out" | sed -n 's/^EFFECTIVE=//p')"
[ "$got" = "21100" ] && ok "kutip + komentar + export terbaca benar (=$got)" \
    || fail "bentuk baris env: got '$got' want 21100"
got="$(printf '%s' "$forms_out" | sed -n 's/^CAPACITY=//p')"
[ "$got" = "60" ] && ok "komentar di ujung baris tidak ikut jadi nilai (=$got)" \
    || fail "komentar ujung baris: got '$got' want 60"

# ---------------------------------------------------------------------------
# 5. Penelusuran port tidak boleh keluar dari rentang.
# ---------------------------------------------------------------------------
awk '/^api_port_fence_end\(\) \{/,/^\}/' "$SCRIPT"  > "$TMP/fn.sh"
awk '/^find_available_api_port\(\) \{/,/^\}/' "$SCRIPT" >> "$TMP/fn.sh"

# make_block <start> <width> <capacity>
make_block() {
    cat > "$TMP/block.sh" <<INNER
set -uo pipefail
PORT_API_START=$1
PORT_API_BLOCK_WIDTH=$2
PORT_API_CAPACITY_MAX=$3
PORT_REGISTRY="\$1"
INNER
    cat "$TMP/fn.sh" >> "$TMP/block.sh"
    echo 'find_available_api_port' >> "$TMP/block.sh"
}

make_block 20100 3 0
: > "$TMP/registry"
out="$(bash "$TMP/block.sh" "$TMP/registry" 2>/dev/null)"
[ "$out" = "20100" ] && ok "port pertama di awal rentang (=$out)" \
    || fail "port pertama: got '$out' want 20100"

printf '20100 # a api\n20101 # b api\n20102 # c api\n' > "$TMP/registry"
if out="$(bash "$TMP/block.sh" "$TMP/registry" 2>/dev/null)"; then
    fail "rentang habis tapi tetap mengalokasikan port '$out' di luar rentang"
else
    ok "rentang habis -> gagal terang-terangan (bukan port di luar rentang)"
fi

# Skenario temuan 1: rentang 900 terpakai penuh, capacity_max 1000 (lebih lebar
# dari rentang). Dulu pagarnya 1000 dan ini mengembalikan 21000 - itu
# RM_API_LISTEN milik VM slot berikutnya, bukan port mesin ini.
make_block 20100 900 1000
seq 20100 20999 | awk '{print $1 " # inst api"}' > "$TMP/registry"
if out="$(bash "$TMP/block.sh" "$TMP/registry" 2>/dev/null)"; then
    fail "20100-20999 penuh tapi mengalokasikan '$out' (21000 = RM-API VM tetangga)"
else
    ok "20100-20999 penuh -> gagal, tidak merambah ke 21000 (blok VM tetangga)"
fi

# capacity_max lebih sempit dari rentang: ERP hanya mem-NAT sebanyak capacity.
make_block 20100 900 2
printf '20100 # a api\n20101 # b api\n' > "$TMP/registry"
if out="$(bash "$TMP/block.sh" "$TMP/registry" 2>/dev/null)"; then
    fail "capacity_max=2 tapi alokasi ke-3 lolos di port '$out'"
else
    ok "capacity_max mempersempit rentang (alokasi ke-3 ditolak)"
fi

if [ "$FAILED" -eq 0 ]; then
    printf "\n%sSemua uji blok port lolos.%s\n" "$G" "$N"
else
    printf "\n%sAda uji blok port yang gagal.%s\n" "$R" "$N" >&2
fi
exit "$FAILED"
