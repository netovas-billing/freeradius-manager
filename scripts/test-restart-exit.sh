#!/usr/bin/env bash
# `radius-manager.sh restart` harus MENERUSKAN status gagal.
#
# KENAPA: skrip berakhir `exit 0` tanpa syarat, jadi subcommand ini dulu selalu
# melaporkan sukses — termasuk saat FreeRADIUS gagal naik. Pemanggil dari luar
# tak bisa membedakannya selain membaca keluaran teks, dan otomasi tidak membaca
# teks.
set -uo pipefail
cd "$(dirname "$0")/.."
gagal=0

blok=$(awk '/^    restart\)/{p=1} p{print} p&&/^        ;;/{exit}' radius-manager.sh)

if printf '%s' "$blok" | grep -q 'restart_freeradius || exit 1'; then
    echo "[ok]   restart) meneruskan status gagal"
else
    echo "[GAGAL] restart) tidak meneruskan status — 'exit 0' di akhir skrip akan menelannya"
    gagal=1
fi

# Cakupannya harus tetap sempit: create/delete punya kontrak sendiri.
for sub in create delete; do
    b=$(awk -v s="^    ${sub})" '$0~s{p=1} p{print} p&&/^        ;;/{exit}' radius-manager.sh)
    if printf '%s' "$b" | grep -q 'restart_freeradius || exit 1'; then
        echo "[GAGAL] ${sub}) ikut berubah — cakupannya melebar di luar yang disepakati"
        gagal=1
    else
        echo "[ok]   ${sub}) tidak ikut berubah"
    fi
done

# `exit 0` di akhir masih ada: itu yang membuat penerusan di atas perlu.
tail -1 radius-manager.sh | grep -q '^exit 0' \
  && echo "[ok]   'exit 0' tanpa syarat masih di akhir (alasan penjaga ini ada)" \
  || { echo "[GAGAL] akhir skrip berubah — tinjau ulang penjaga ini"; gagal=1; }

[ "$gagal" -eq 0 ] && echo "Semua uji status keluar restart lolos." || echo "ADA YANG GAGAL."
exit "$gagal"
