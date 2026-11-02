#!/usr/bin/env bash
# scripts/test-runtime-api.sh - uji penolakan runtime Python di sisi skrip
# TANPA menjalankan radius-manager.sh/install.sh sampai menyentuh sistem.
#
# Runtime Python sudah DIBUANG (30 Okt 2026). Yang dijaga di sini:
#
#   1. periksa_runtime_api (radius-manager.sh) menerima kosong/"go" (tak peka
#      huruf/spasi) dan MENOLAK nilai lain (mis. "python") dengan pesan yang
#      menyebut cara memperbaiki;
#   2. di cabang `create`, urutannya periksa_runtime_api → ensure_go_template →
#      create_database → register_port → restart_freeradius. Kalau urutan ini
#      lepas, repo Go privat tanpa kredensial / toolchain absen kembali
#      meninggalkan DB, port, dan config FreeRADIUS untuk instance setengah jadi;
#   3. pemeriksaan RM_API_RUNTIME di install.sh menolak "python" (juga dengan
#      awalan `export `) SEBELUM biner ditimpa, dan membiarkan kosong/"go".
#
# Usage: ./scripts/test-runtime-api.sh    (tidak butuh root, tidak butuh Docker)
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_DIR/radius-manager.sh"
INSTALL="$REPO_DIR/install.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ -t 1 ]]; then
    G=$'\033[32m'; R=$'\033[31m'; N=$'\033[0m'
else
    G=""; R=""; N=""
fi
FAILED=0
ok()   { printf "%s[ok]%s   %s\n" "$G" "$N" "$*"; }
fail() { printf "%s[FAIL]%s %s\n" "$R" "$N" "$*" >&2; FAILED=1; }

# -- 1. periksa_runtime_api --------------------------------------------------
# Potong fungsinya saja (sampai `}` pertama di kolom 0) supaya bisa diuji
# tanpa memuat seluruh skrip.
awk '/^periksa_runtime_api\(\) \{/{f=1} f{print} f&&/^\}/{exit}' "$SCRIPT" > "$TMP/fn.sh"
[ -s "$TMP/fn.sh" ] || { fail "periksa_runtime_api tidak ditemukan di $SCRIPT"; exit 1; }

# cek_runtime <nilai> -> keluaran "rc=<n>" + pesan galat
cek_runtime() {
    API_RUNTIME="$1" bash -c '
        error() { echo "ERR: $*"; }
        . "$1"
        periksa_runtime_api; echo "rc=$?"
    ' _ "$TMP/fn.sh" 2>&1
}

for v in "" "go" "GO" " go "; do
    out="$(cek_runtime "$v")"
    if grep -q '^rc=0$' <<<"$out"; then
        ok "API_RUNTIME='$v' diterima"
    else
        fail "API_RUNTIME='$v' mestinya diterima, keluaran: $out"
    fi
done
for v in "python" "Python" "py"; do
    out="$(cek_runtime "$v")"
    if grep -q '^rc=1$' <<<"$out" && grep -q "Hapus/unset API_RUNTIME" <<<"$out"; then
        ok "API_RUNTIME='$v' ditolak dengan petunjuk perbaikan"
    else
        fail "API_RUNTIME='$v' mestinya ditolak + petunjuk, keluaran: $out"
    fi
done

# -- 2. urutan di cabang create ----------------------------------------------
# Ambil baris pertama tiap pemanggilan SESUDAH pemeriksaan runtime di create.
mulai="$(grep -n '^[[:space:]]*periksa_runtime_api || exit 1' "$SCRIPT" | head -n1 | cut -d: -f1)"
if [ -z "$mulai" ]; then
    fail "create tidak memanggil 'periksa_runtime_api || exit 1'"
else
    baris_setelah() { # <pola> -> nomor baris pertama >= $mulai
        awk -v m="$mulai" -v p="$1" 'NR>=m && $0 ~ p {print NR; exit}' "$SCRIPT"
    }
    l_tpl="$(baris_setelah '^[[:space:]]*if ! ensure_go_template')"
    # Pola memakai kelas [$]/[(] karena `awk -v` memproses escape backslash.
    l_port="$(baris_setelah 'API_PORT=[$][(]find_available_api_port')"
    l_info="$(baris_setelah 'cat > "[$]INFO_FILE"')"
    l_db="$(baris_setelah '^[[:space:]]*create_database[[:space:]]')"
    l_reg="$(baris_setelah '^[[:space:]]*register_port[[:space:]]')"
    l_rst="$(baris_setelah '^[[:space:]]*restart_freeradius')"
    l_api="$(baris_setelah '^[[:space:]]*setup_api[[:space:]]')"
    if [ -n "$l_tpl" ] && [ -n "$l_port" ] && [ -n "$l_info" ] && [ -n "$l_db" ] &&
       [ -n "$l_reg" ] && [ -n "$l_rst" ] && [ -n "$l_api" ] &&
       [ "$mulai" -lt "$l_tpl" ] && [ "$l_tpl" -lt "$l_port" ] &&
       [ "$l_port" -lt "$l_info" ] && [ "$l_info" -lt "$l_db" ] &&
       [ "$l_db" -lt "$l_reg" ] && [ "$l_reg" -lt "$l_rst" ] && [ "$l_rst" -lt "$l_api" ]; then
        ok "create: runtime($mulai) < template Go($l_tpl) < port($l_port) < .instance($l_info) < DB($l_db) < register_port($l_reg) < restart($l_rst) < setup_api($l_api)"
    else
        fail "urutan create salah: runtime=$mulai tpl=$l_tpl port=$l_port info=$l_info db=$l_db reg=$l_reg restart=$l_rst setup_api=$l_api"
    fi
fi

# -- 3. pemeriksaan RM_API_RUNTIME di install.sh -----------------------------
# Potong blok `if [[ -f "$ENV_FILE" ]]` yang mengikuti komentar penandanya.
awal="$(grep -n '^# RM_API_RUNTIME selain kosong' "$INSTALL" | head -n1 | cut -d: -f1)"
if [ -z "$awal" ]; then
    fail "blok pemeriksaan RM_API_RUNTIME tidak ditemukan di $INSTALL"
else
    awk -v a="$awal" 'NR>=a{print} NR>a && /^fi$/{exit}' "$INSTALL" > "$TMP/install-cek.sh"
    # cek_install <isi-berkas-env> -> "rc=<n>" (+ pesan fail)
    cek_install() {
        printf '%s\n' "$1" > "$TMP/env"
        ENV_FILE="$TMP/env" bash -c '
            fail() { echo "FAIL: $*"; exit 1; }
            . "$1"
            echo "lolos"
        ' _ "$TMP/install-cek.sh" 2>&1
        echo "rc=$?"
    }
    for isi in "RM_API_TOKEN=x" "RM_API_RUNTIME=" "RM_API_RUNTIME=go" 'RM_API_RUNTIME="GO"'; do
        out="$(cek_install "$isi")"
        if grep -q '^lolos$' <<<"$out"; then
            ok "install.sh: '$isi' lolos"
        else
            fail "install.sh: '$isi' mestinya lolos, keluaran: $out"
        fi
    done
    for isi in "RM_API_RUNTIME=python" 'RM_API_RUNTIME="python"' "export RM_API_RUNTIME=python" "  RM_API_RUNTIME=Python"; do
        out="$(cek_install "$isi")"
        if grep -q '^FAIL: .*Hapus baris RM_API_RUNTIME' <<<"$out" && ! grep -q '^lolos$' <<<"$out"; then
            ok "install.sh: '$isi' ditolak dengan petunjuk perbaikan"
        else
            fail "install.sh: '$isi' mestinya ditolak, keluaran: $out"
        fi
    done
fi

if [ "$FAILED" -ne 0 ]; then
    echo "${R}test-runtime-api: GAGAL${N}" >&2
    exit 1
fi
echo "${G}test-runtime-api: semua lulus${N}"
