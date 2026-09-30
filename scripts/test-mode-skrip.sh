#!/usr/bin/env bash
# Semua skrip yang dimaksudkan untuk DIJALANKAN harus tercatat 100755 di git.
#
# KENAPA ADA: update.sh dan radius-manager.sh pernah tercatat 100644, sehingga
# setiap klon baru harus di-chmod dulu. Biayanya bukan besar, tapi waktunya
# paling buruk — ia menghadang operator tepat saat ia sedang memperbaiki sesuatu
# yang lain, dengan pesan "Permission denied" yang tak ada hubungannya dengan
# masalah yang sedang ia kejar. Terjadi nyata 30 Sep 2026 di ntvs-radius-jkt.
#
# Mode berkas ikut tercatat di git, jadi ia bisa dijaga seperti kode lain.
set -uo pipefail
cd "$(dirname "$0")/.."

gagal=0
while read -r mode _ _ berkas; do
    case "$berkas" in
        # Berkas yang memang BUKAN untuk dijalankan langsung boleh 644.
        *.tmpl|*template*) continue ;;
    esac
    if [ "$mode" != "100755" ]; then
        echo "[GAGAL] $berkas tercatat $mode — seharusnya 100755 (git update-index --chmod=+x $berkas)"
        gagal=1
    else
        echo "[ok]   $berkas"
    fi
done < <(git ls-files -s -- '*.sh')

[ "$gagal" -eq 0 ] && echo "Semua skrip bisa dieksekusi." || echo "ADA YANG TIDAK BISA DIEKSEKUSI."
exit "$gagal"
