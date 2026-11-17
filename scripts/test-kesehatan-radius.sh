#!/usr/bin/env bash
# scripts/test-kesehatan-radius.sh — uji pemeriksaan kesehatan FreeRADIUS di
# radius-manager.sh TANPA root, tanpa FreeRADIUS, tanpa Docker.
#
# Latar: insiden 17 Nov 2026 (pindah RADIUS LintasHome PALI). ERP hanya
# memeriksa freeradius-api (sehat) lalu menyatakan "Siap dipasang" padahal
# FreeRADIUS MATI. freeradius-api kini punya GET /api/v1/radius/health yang
# menyapa FreeRADIUS lewat 127.0.0.1 dengan RADIUS_HEALTH_SECRET; yang dijaga di
# sini:
#
#   1. jaringan_client_dinamis berperilaku SAMA dengan internal/config (kosong
#      → 0.0.0.0/0, "off" → mati) — dulu "off" diteruskan mentah ke
#      `ipaddr = off` dan FreeRADIUS gagal parse;
#   2. blok client kesehatan bash IDENTIK dengan template Go (penanda
#      netovas-health di internal/templates/templates/virtual_server.tmpl);
#   3. create_virtual_server menyisipkannya di cabang PERTAMA dynclients;
#   4. `aktifkan-kesehatan` untuk instance lama: idempoten, memvalidasi config
#      dan memulihkan bila gagal, TIDAK me-restart FreeRADIUS tanpa --restart,
#      dan menulis .env HANYA bila FreeRADIUS benar menjawab (atau mati).
#
# Usage: ./scripts/test-kesehatan-radius.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_DIR/radius-manager.sh"
TMPL="$REPO_DIR/internal/templates/templates/virtual_server.tmpl"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ -t 1 ]]; then G=$'\033[32m'; R=$'\033[31m'; N=$'\033[0m'; else G=""; R=""; N=""; fi
FAILED=0
ok()   { printf "%s[ok]%s   %s\n" "$G" "$N" "$*"; }
fail() { printf "%s[FAIL]%s %s\n" "$R" "$N" "$*" >&2; FAILED=1; }

# Potong dari `nama() {` sampai SEBELUM definisi fungsi berikutnya — bukan `}`
# pertama di kolom 0: heredoc create_virtual_server memuat `}` di kolom 0.
ambil_fungsi() {
    awk -v f="$1" '
        a && /^[A-Za-z_][A-Za-z0-9_]*\(\) \{/ { exit }
        a && /^# >>> / { exit }
        $0 ~ "^"f"\\(\\) \\{" { a = 1 }
        a { print }' "$SCRIPT"
}
{
    awk '/^# >>> kesehatan-radius/{a=1} a{print} /^# <<< kesehatan-radius/{exit}' "$SCRIPT"
    ambil_fungsi create_virtual_server
    ambil_fungsi load_instance_info
    ambil_fungsi generate_password
} > "$TMP/fungsi.sh"
for f in jaringan_client_dinamis blok_kesehatan_client blok_kesehatan_server sisipkan_blok_kesehatan aktifkan_kesehatan \
         kesehatan_bisa_dipasang blok_kesehatan_lengkap \
         create_virtual_server load_instance_info generate_password; do
    grep -q "^${f}() {" "$TMP/fungsi.sh" || { fail "fungsi $f tidak ditemukan di $SCRIPT"; exit 1; }
done

# shellcheck disable=SC1091
. "$TMP/fungsi.sh"
info()    { echo "INFO: $*"; }
success() { echo "OK: $*"; }
warning() { echo "WARN: $*"; }
error()   { echo "ERR: $*"; }
ln()    { :; }
chown() { :; }

FREERADIUS_DIR="$TMP/raddb"
SITES_AVAILABLE="$FREERADIUS_DIR/sites-available"
SITES_ENABLED="$FREERADIUS_DIR/sites-enabled"
API_DIR_BASE="$TMP/root"
FR_USER=x FR_GROUP=x
mkdir -p "$SITES_AVAILABLE" "$SITES_ENABLED" "$API_DIR_BASE"

# -- 1. jaringan_client_dinamis ---------------------------------------------
cek_jaringan() {
    local masuk="$1" mau="$2" dapat
    dapat="$(RM_API_DYNAMIC_CLIENT_NET="$masuk" jaringan_client_dinamis)"
    [ "$dapat" = "$mau" ] && ok "RM_API_DYNAMIC_CLIENT_NET='$masuk' → '$mau'" \
        || fail "RM_API_DYNAMIC_CLIENT_NET='$masuk' → '$dapat', mau '$mau'"
}
cek_jaringan ""                    "0.0.0.0/0"
cek_jaringan "  "                  "0.0.0.0/0"
cek_jaringan "off"                 ""
cek_jaringan "OFF"                 ""
cek_jaringan " mati "              ""
cek_jaringan "-"                   ""
cek_jaringan "none"                ""
cek_jaringan " 172.31.199.0/24 "   "172.31.199.0/24"
RM_API_DYNAMIC_CLIENT_NET="" kesehatan_bisa_dipasang && ok "kesehatan: net bawaan (0.0.0.0/0) → dipasang" || fail "kesehatan: net bawaan tak dipasang"
RM_API_DYNAMIC_CLIENT_NET="172.16.0.0/12" kesehatan_bisa_dipasang && ok "kesehatan: net sempit → TETAP dipasang (client 127.0.0.0/8 sendiri)" \
    || fail "kesehatan: net sempit tak dipasang"
RM_API_DYNAMIC_CLIENT_NET="off" kesehatan_bisa_dipasang && fail "kesehatan: NAS dinamis mati tetap dipasang" || ok "kesehatan: NAS dinamis mati → tidak"
RM_API_DYNAMIC_CLIENT_NET="127.0.0.0/8" kesehatan_bisa_dipasang && fail "kesehatan: net 127.0.0.0/8 (duplicate client) tetap dipasang" \
    || ok "kesehatan: net persis 127.0.0.0/8 → tidak (prefiks bentrok)"

# -- 2. paritas blok bash ↔ template Go --------------------------------------
SEC="Sehat0123456789abcdefABCDEF0123"
ganti_go() { sed -e 's/{{ \.InstanceName }}/radius_test1/g' -e "s/{{ \.HealthSecret }}/${SEC}/g" -e 's/{{ \.AuthPort }}/14368/g'; }
awk '/^    # >>> netovas-health-client$/{a=1} a{print} /^    # <<< netovas-health-client$/{exit}' "$TMPL" | ganti_go > "$TMP/go.client"
blok_kesehatan_client radius_test1 > "$TMP/bash.client"
if [ -s "$TMP/go.client" ] && diff -u "$TMP/go.client" "$TMP/bash.client" > "$TMP/beda"; then
    ok "blok client kesehatan bash identik dengan template Go"
else
    fail "blok client kesehatan bash ≠ template Go:"; cat "$TMP/beda" >&2
fi
awk '/^# >>> netovas-health$/{a=1} a{print} /^# <<< netovas-health$/{exit}' "$TMPL" | ganti_go > "$TMP/go.server"
blok_kesehatan_server radius_test1 "$SEC" 14368 > "$TMP/bash.server"
if [ -s "$TMP/go.server" ] && diff -u "$TMP/go.server" "$TMP/bash.server" > "$TMP/beda"; then
    ok "blok server kesehatan bash identik dengan template Go"
else
    fail "blok server kesehatan bash ≠ template Go:"; cat "$TMP/beda" >&2
fi

# -- 3. create_virtual_server ------------------------------------------------
buat_site() { # buat_site <nama> <port> <secret> <jaringan>
    RM_API_DYNAMIC_CLIENT_NET="$4" create_virtual_server "$1" "$2" "$3" > /dev/null
}
buat_site baru1 11000 "$SEC" ""
F="$SITES_AVAILABLE/baru1"
blok_kesehatan_lengkap "$F" && ok "create: tepat satu pasang penanda client & server" \
    || fail "create: penanda kesehatan tidak lengkap/ganda"
dl="$(awk '/^clients dynclist_baru1 \{/{a=1} a{print} a && /^\}$/{exit}' "$F")"
grep -q "client dyn_baru1 {" <<<"$dl" && grep -q "client dynhealth_baru1 {" <<<"$dl" \
    && grep -q "ipaddr          = 127.0.0.0/8" <<<"$dl" \
    && ok "create: client 127.0.0.0/8 di DALAM daftar dynclist_baru1" || fail "create: client kesehatan tak di daftar dynclist: $dl"
grep -q "(&Packet-Dst-Port == 11000)" "$F" && ok "create: client hanya dibuat untuk port auth 11000" \
    || fail "create: Packet-Dst-Port auth tak ditanam"
grep -q 'FreeRADIUS-Client-Virtual-Server = "netovas_health_baru1"' "$F" \
    && ok "create: client kesehatan diproses netovas_health_baru1 (bukan virtual server instance)" \
    || fail "create: client kesehatan diproses virtual server lain"
dyn="$(awk '/^server dynclients_baru1 \{/{a=1} a{print} a && /^\}$/{exit}' "$F")"
grep -q '127.0.0.1' <<<"$dyn" && fail "create: dynclients_baru1 masih memuat cabang loopback lama" \
    || ok "create: dynclients_baru1 tanpa cabang loopback"
[ "$(secret_kesehatan_dari_config "$F")" = "$SEC" ] && ok "create: secret terbaca kembali dari config" \
    || fail "create: secret di config tidak terbaca kembali"
grep -qE '^[^#]*ipaddr[[:space:]]*=[[:space:]]*127\.0\.0\.1([^/]|$)' "$F" \
    && fail "create: client STATIS 127.0.0.1 dipancarkan (duplikat dgn nas → FreeRADIUS tak start)" \
    || ok "create: tak ada client statis 127.0.0.1"

buat_site sempit1 11030 "$SEC" "172.16.0.0/12"
blok_kesehatan_lengkap "$SITES_AVAILABLE/sempit1" && grep -q "ipaddr          = 172.16.0.0/12" "$SITES_AVAILABLE/sempit1" \
    && ok "create net sempit: NAS dinamis 172.16.0.0/12 + client kesehatan 127.0.0.0/8" \
    || fail "create net sempit: kesehatan tak lengkap"

buat_site mati1 11010 "$SEC" "off"
F="$SITES_AVAILABLE/mati1"
if grep -q "dynclients_\|netovas-health\|ipaddr *= *off" "$F"; then
    fail "create NAS dinamis 'off': masih memancarkan dynclients/kesehatan/ipaddr=off"
else
    ok "create NAS dinamis 'off': tanpa dynclients & kesehatan"
fi
buat_site tanpa1 11020 "" ""
grep -q "netovas-health" "$SITES_AVAILABLE/tanpa1" && fail "create tanpa secret: blok kesehatan muncul" \
    || ok "create tanpa secret: tak ada blok kesehatan"

# -- 4. aktifkan-kesehatan (instance LAMA) -----------------------------------
# Tiruan dunia luar. FreeRADIUS "memuat" secret kesehatan dari config hanya
# saat restart_freeradius; radclient menjawab hanya bila secret-nya termuat.
LOADED="$TMP/loaded"; : > "$LOADED"
FR_XC_OK=1; FR_AKTIF=0; NAS_LOKAL=0
freeradius() { if [ "$FR_XC_OK" = 1 ]; then echo "Configuration appears to be OK"; else echo "ERROR: rusak"; fi; }
restart_freeradius() {
    echo "RESTART-FR" >> "$TMP/aksi.log"
    : > "$LOADED"
    local s
    for s in "$SITES_AVAILABLE"/*; do secret_kesehatan_dari_config "$s" >> "$LOADED"; done
    return 0
}
radclient() {
    cat > /dev/null
    echo "radclient $*" >> "$TMP/argv.log"
    local secret="" tujuan=""
    while [ $# -gt 0 ]; do
        case "$1" in
            -S) secret=$(cat "$2"); shift 2 ;;
            -r|-t) shift 2 ;;
            -*) shift ;;
            *) [ -z "$tujuan" ] && tujuan=$1; shift ;;
        esac
    done
    if [ "$FR_AKTIF" = 0 ] && [ "$NAS_LOKAL" = 0 ] && [ -n "$secret" ] && grep -qxF "$secret" "$LOADED"; then
        echo "Received Access-Reject Id 1 from ${tujuan} to 127.0.0.1:5555 length 20"
    else
        echo "(0) No reply from server for ID 1 socket 3"
    fi
}
systemctl() {
    echo "systemctl $*" >> "$TMP/aksi.log"
    case "$1" in is-active) return "$FR_AKTIF" ;; esac
    return 0
}
curl() { echo "curl $*" >> "$TMP/argv.log"; cat > /dev/null; echo 200; }
mysql_cmd() { echo "$NAS_LOKAL"; }

siapkan_lama() { # siapkan_lama <nama> <port> — instance lama: dynclients tanpa blok kesehatan
    local A=$1 P=$2
    buat_site "$A" "$P" "" ""
    printf 'ADMIN_USERNAME=%s\nDB_NAME=%s\nAUTH_PORT=%s\nAPI_PORT=20100\n' "$A" "$A" "$P" > "$FREERADIUS_DIR/.instance_${A}"
    mkdir -p "$API_DIR_BASE/${A}-api"
    printf 'PORT=20100\nBASIC_AUTH_USER=admin\nBASIC_AUTH_PASSWORD=x\nDB_NAME=%s\n' "$A" > "$API_DIR_BASE/${A}-api/.env"
}
jalankan() { : > "$TMP/aksi.log"; aktifkan_kesehatan "$@" > "$TMP/keluaran" 2>&1; echo $?; }

siapkan_lama lama1 12000
cp "$SITES_AVAILABLE/lama1" "$TMP/lama1.asli"
ENV1="$API_DIR_BASE/lama1-api/.env"

# 4a. tanpa --restart: config dipasang+divalidasi, FreeRADIUS & API tak disentuh, .env tak ditulis.
rc=$(jalankan lama1)
S1="$(secret_kesehatan_dari_config "$SITES_AVAILABLE/lama1")"
[ -n "$S1" ] && ok "4a: client kesehatan disisipkan ke instance lama" || fail "4a: tak disisipkan: $(cat "$TMP/keluaran")"
ls "$FREERADIUS_DIR/.cadangan-kesehatan/"lama1.* >/dev/null 2>&1 && ok "4a: cadangan config dibuat" || fail "4a: tak ada cadangan"
grep -q "RESTART-FR\|restart lama1-api" "$TMP/aksi.log" && fail "4a: me-restart tanpa --restart: $(cat "$TMP/aksi.log")" \
    || ok "4a: tanpa --restart tidak me-restart FreeRADIUS/API"
grep -q "^RADIUS_HEALTH" "$ENV1" && fail "4a: .env ditulis padahal FreeRADIUS belum memuat client (→ 503 palsu)" \
    || ok "4a: .env BELUM ditulis selama client belum termuat"
[ "$rc" = 0 ] && grep -q -- "--restart" "$TMP/keluaran" && ok "4a: keluar 0 + petunjuk --restart" \
    || fail "4a: rc=$rc / tanpa petunjuk --restart"

# 4b. ulang tanpa --restart: idempoten.
rc=$(jalankan lama1)
blok_kesehatan_lengkap "$SITES_AVAILABLE/lama1" \
    && [ "$(secret_kesehatan_dari_config "$SITES_AVAILABLE/lama1")" = "$S1" ] \
    && ok "4b: diulang → tetap satu blok, secret sama" || fail "4b: tidak idempoten"

# 4c. --restart: FreeRADIUS di-restart SEKALI, .env ditulis dgn secret config, API di-restart.
rc=$(jalankan lama1 --restart)
[ "$(grep -c RESTART-FR "$TMP/aksi.log")" = 1 ] && ok "4c: --restart me-restart FreeRADIUS sekali" \
    || fail "4c: restart FreeRADIUS $(grep -c RESTART-FR "$TMP/aksi.log")x"
[ "$(grep '^RADIUS_HEALTH_ADDR=' "$ENV1" | cut -d= -f2-)" = "127.0.0.1:12000" ] \
    && [ "$(grep '^RADIUS_HEALTH_SECRET=' "$ENV1" | cut -d= -f2-)" = "$S1" ] \
    && ok "4c: .env = 127.0.0.1:<auth> + secret yang SAMA dengan config" || fail "4c: .env salah: $(cat "$ENV1")"
grep -q "restart lama1-api" "$TMP/aksi.log" && ok "4c: lama1-api di-restart" || fail "4c: API tak di-restart"
[ "$rc" = 0 ] && ok "4c: keluar 0" || fail "4c: rc=$rc: $(cat "$TMP/keluaran")"

# 4d. ulang dengan --restart padahal semua sudah menjawab: tak ada restart apa pun.
rc=$(jalankan lama1 --restart)
grep -q "RESTART-FR\|restart lama1-api" "$TMP/aksi.log" && fail "4d: restart tanpa perlu: $(cat "$TMP/aksi.log")" \
    || ok "4d: semua sudah sehat → tak ada restart"
[ "$(grep -c '^RADIUS_HEALTH_SECRET=' "$ENV1")" = 1 ] && ok "4d: .env tak menggandakan kunci" || fail "4d: kunci ganda"

# 4e. freeradius -XC gagal → config dipulihkan byte-per-byte, keluar ≠ 0.
siapkan_lama lama2 12010
cp "$SITES_AVAILABLE/lama2" "$TMP/lama2.asli"
FR_XC_OK=0
rc=$(jalankan lama2 --restart)
FR_XC_OK=1
cmp -s "$TMP/lama2.asli" "$SITES_AVAILABLE/lama2" && [ "$rc" != 0 ] \
    && ok "4e: config gagal validasi → dipulihkan, keluar $rc" || fail "4e: tidak dipulihkan (rc=$rc)"
grep -q "RESTART-FR" "$TMP/aksi.log" && fail "4e: me-restart config yang rusak" || ok "4e: tak me-restart config rusak"

# 4f. instance tanpa NAS dinamis → dilewati, tak disentuh.
siapkan_lama mati2 12020
RM_API_DYNAMIC_CLIENT_NET=off create_virtual_server mati2 12020 "" > /dev/null
cp "$SITES_AVAILABLE/mati2" "$TMP/mati2.asli"
rc=$(jalankan mati2)
cmp -s "$TMP/mati2.asli" "$SITES_AVAILABLE/mati2" && grep -q "TANPA NAS dinamis" "$TMP/keluaran" \
    && ok "4f: instance tanpa NAS dinamis dilewati dgn penjelasan" || fail "4f: $(cat "$TMP/keluaran")"

# 4g. FreeRADIUS hidup, client termuat, tapi tak menjawab (baris nas 127.0.0.1)
#     → galat yang menyebut penyebabnya, .env TIDAK ditulis.
siapkan_lama lama3 12030
jalankan lama3 --restart > /dev/null
sed -i '/^RADIUS_HEALTH/d;/Pemeriksaan kesehatan FreeRADIUS/,+2d' "$API_DIR_BASE/lama3-api/.env"
NAS_LOKAL=1
rc=$(jalankan lama3)
NAS_LOKAL=0
grep -q "^RADIUS_HEALTH" "$API_DIR_BASE/lama3-api/.env" && fail "4g: .env ditulis padahal tak menjawab" \
    || ok "4g: tak menjawab → .env tidak ditulis"
[ "$rc" != 0 ] && grep -q "127.0.0.1/localhost" "$TMP/keluaran" && ok "4g: keluar $rc + menyebut baris nas 127.0.0.1" \
    || fail "4g: rc=$rc: $(cat "$TMP/keluaran")"

# 4h. FreeRADIUS MATI → .env tetap ditulis (API jujur 503, bukan 501 "tak didukung").
siapkan_lama lama4 12040
FR_AKTIF=3
rc=$(jalankan lama4)
FR_AKTIF=0
grep -q "^RADIUS_HEALTH_SECRET=" "$API_DIR_BASE/lama4-api/.env" && ok "4h: FreeRADIUS mati → .env tetap ditulis" \
    || fail "4h: .env tak ditulis saat FreeRADIUS mati: $(cat "$TMP/keluaran")"

# 4i. runtime Python lama → dilewati.
siapkan_lama py1 12050
mkdir -p "$API_DIR_BASE/py1-api/venv"
cp "$SITES_AVAILABLE/py1" "$TMP/py1.asli"
rc=$(jalankan py1)
cmp -s "$TMP/py1.asli" "$SITES_AVAILABLE/py1" && ok "4i: instance Python lama tak disentuh" || fail "4i: instance Python diubah"

# 4j. blok TIDAK LENGKAP (bentuk lama / suntingan tangan) → galat, tak disentuh.
siapkan_lama sisa1 12060
printf '        # >>> netovas-health\n        # <<< netovas-health\n' >> "$SITES_AVAILABLE/sisa1"
cp "$SITES_AVAILABLE/sisa1" "$TMP/sisa1.asli"
rc=$(jalankan sisa1)
cmp -s "$TMP/sisa1.asli" "$SITES_AVAILABLE/sisa1" && [ "$rc" != 0 ] && grep -q "TIDAK LENGKAP" "$TMP/keluaran" \
    && ok "4j: blok tak lengkap → galat, config tak disentuh" || fail "4j: rc=$rc: $(cat "$TMP/keluaran")"

# 4k. secret & sandi API TIDAK pernah jadi argumen proses (terlihat di `ps`).
if grep -qF "$S1" "$TMP/argv.log" || grep -q -- " -u " "$TMP/argv.log"; then
    fail "4k: secret/sandi muncul di argumen radclient/curl: $(grep -F -e "$S1" -e ' -u ' "$TMP/argv.log" | head -2)"
else
    ok "4k: secret kesehatan & sandi API tak pernah jadi argumen proses"
fi
grep -q -- "-S " "$TMP/argv.log" && ok "4k: radclient memakai -S <berkas>" || fail "4k: radclient tak memakai -S"

# -- 5. cabang create menulis secret yang SAMA ke config & .env ----------------
grep -q 'create_virtual_server "$ADMIN_USERNAME" "$AUTH_PORT" "$HEALTH_SECRET"' "$SCRIPT" \
    && grep -q 'setup_api .*"$AUTH_PORT" "$HEALTH_SECRET"' "$SCRIPT" \
    && ok "create: HEALTH_SECRET yang sama diteruskan ke config & setup_api" \
    || fail "create: HEALTH_SECRET tidak diteruskan ke keduanya"

if [ "$FAILED" -ne 0 ]; then
    echo "${R}GAGAL${N}"
    exit 1
fi
echo "${G}semua lulus${N}"
