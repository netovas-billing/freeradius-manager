#!/usr/bin/env bash
# Uji unit systemd tunnel yang diterbitkan vpn-client-setup.sh.
#
# ADA KARENA SATU KEGAGALAN NYATA (27 Sep 2026): unit-nya melaporkan
#
#   Active: active (exited)
#   Finished l2tp-vpn-mikrotik.service
#
# untuk tunnel yang TIDAK PERNAH NAIK. Pemilik sistem membacanya sebagai sukses
# dan mencari sebab "backend tak bisa menghubungi RADIUS" di tempat lain
# berjam-jam — padahal PPP secret-nya belum ada di concentrator.
#
# Sebabnya: menulis "c <tunnel>" ke socket kontrol xl2tpd hanya MENGANTRE
# permintaan connect. Penulisannya berhasil entah autentikasinya nanti diterima
# atau ditolak, dan hasilnya tak pernah sampai ke systemd. Jadi ExecStartPost
# yang cuma menulis ke socket itu TIDAK BISA gagal.
#
# Tanpa root, tanpa Docker, tanpa jaringan.
set -uo pipefail
cd "$(dirname "$0")/.."
GAGAL=0
ok()  { printf '[ok]   %s\n' "$*"; }
bad() { printf '[GAGAL] %s\n' "$*" >&2; GAGAL=1; }

UNIT=$(sed -n '/^\[Unit\]$/,/^EOF$/p' scripts/vpn-client-setup.sh)
[ -n "$UNIT" ] || { bad "blok unit systemd tidak ketemu di vpn-client-setup.sh"; exit 1; }

# 1. HARUS ada langkah yang membuktikan tunnelnya naik.
if grep -qE '^ExecStartPost=.*ip -4 -o addr show' <<<"$UNIT"; then
  ok "unit memverifikasi alamat muncul di antarmuka ppp"
else
  bad "tak ada verifikasi alamat ppp — unit akan hijau untuk tunnel yang mati"
fi

# 2. Verifikasinya harus BISA GAGAL. Tanpa 'exit 1' ia cuma mencetak lalu lulus.
if grep -qE '^ExecStartPost=.*exit 1' <<<"$UNIT"; then
  ok "verifikasi berakhir dengan exit 1 — unit benar-benar merah saat gagal"
else
  bad "verifikasi tidak pernah keluar dengan status gagal"
fi

# 3. Pesannya harus menyebut SEBAB YANG PALING SERING, bukan cuma "gagal".
#    Orang yang terhalang di sini biasanya tidak tahu skrip concentrator ada.
for frasa in 'PPP secret' 'Setup Script' 'journalctl'; do
  if grep -qF "$frasa" <<<"$UNIT"; then
    ok "pesan gagal menyebut \"$frasa\""
  else
    bad "pesan gagal tidak menyebut \"$frasa\" — operator tak tahu harus ke mana"
  fi
done

# 4. Penulisan ke socket kontrol TETAP ada: verifikasi menggantikan keyakinan,
#    bukan menggantikan perintah connect-nya.
if grep -qE '^ExecStartPost=.*l2tp-control' <<<"$UNIT"; then
  ok "perintah connect ke socket xl2tpd masih dipancarkan"
else
  bad "perintah connect hilang — tunnelnya tak akan pernah diminta naik"
fi

# 5. Urutannya: connect DULU, baru verifikasi. Terbalik = memverifikasi sesuatu
#    yang belum diminta naik, dan SELALU gagal.
BARIS_CONNECT=$(grep -nE '^ExecStartPost=.*l2tp-control' <<<"$UNIT" | head -1 | cut -d: -f1)
BARIS_VERIF=$(grep -nE '^ExecStartPost=.*ip -4 -o addr show' <<<"$UNIT" | head -1 | cut -d: -f1)
if [ -n "$BARIS_CONNECT" ] && [ -n "$BARIS_VERIF" ] && [ "$BARIS_CONNECT" -lt "$BARIS_VERIF" ]; then
  ok "connect mendahului verifikasi"
else
  bad "verifikasi mendahului connect (connect=$BARIS_CONNECT verif=$BARIS_VERIF) — akan selalu gagal"
fi

echo
[ "$GAGAL" = 0 ] && { echo "Semua uji unit tunnel lolos."; exit 0; }
echo "Ada uji yang gagal." >&2; exit 1
