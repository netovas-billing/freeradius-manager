#!/usr/bin/env bash
# Penjaga bawaan alamat bind installer + penurunan alamat probe.
#
# Kenapa ada: bawaan 127.0.0.1 membuat RM-API hanya terjangkau dari mesinnya
# sendiri, SEMENTARA installer tetap melapor hijau karena self-test-nya menembak
# loopback. Itu kegagalan bisu yang sudah memakan waktu di lapangan — backend
# menjangkau VM ini dari LUAR, lewat DSTNAT concentrator.
#
# Dan bawaan Go (`internal/config`) tidak menolong: unit systemd yang ditulis
# installer menyetel RM_API_LISTEN secara eksplisit, jadi ia MENIMPA bawaan itu.
set -uo pipefail
cd "$(dirname "$0")/.."
gagal=0
uji() { if [ "$2" = "$3" ]; then echo "[ok]   $1"; else echo "[GAGAL] $1: dapat '$2', mau '$3'"; gagal=1; fi; }

bawaan=$(grep -oP 'RM_INSTALL_BIND:-\K[^}"]+' install.sh)
uji "bawaan RM_INSTALL_BIND wildcard" "$bawaan" "0.0.0.0:9000"

# Unit systemd harus meneruskan nilai itu, bukan nilai lain.
if grep -q 'Environment="RM_API_LISTEN=${RM_INSTALL_BIND}"' install.sh; then
    echo "[ok]   unit systemd memakai RM_INSTALL_BIND"
else
    echo "[GAGAL] unit systemd tidak memakai RM_INSTALL_BIND"; gagal=1
fi

# Self-test TIDAK boleh menembak alamat wildcard: `curl http://0.0.0.0:9000`
# bergantung pada perilaku kernel, dan URL itu tak berguna saat dicetak.
if grep -q 'health_url="http://${rm_probe}/v1/server/health"' install.sh; then
    echo "[ok]   self-test memakai alamat probe, bukan alamat bind"
else
    echo "[GAGAL] self-test masih menembak alamat bind mentah"; gagal=1
fi

turunkan() {
    local b="$1" h p
    h="${b%:*}"; p="${b##*:}"
    case "$h" in 0.0.0.0|::|"[::]"|"") echo "127.0.0.1:${p}" ;; *) echo "$b" ;; esac
}
uji "wildcard  -> loopback" "$(turunkan 0.0.0.0:9000)"   "127.0.0.1:9000"
uji "loopback  -> apa adanya" "$(turunkan 127.0.0.1:9000)" "127.0.0.1:9000"
uji "IP nyata  -> apa adanya" "$(turunkan 10.0.0.5:9000)"  "10.0.0.5:9000"

[ "$gagal" -eq 0 ] && echo "Semua uji bind installer lolos." || echo "ADA YANG GAGAL."
exit "$gagal"
