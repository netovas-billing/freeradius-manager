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


# ─────────────────────────────────────────────────────────────────────────────
# 7. UNIT-nya HARUS BENAR-BENAR TER-PARSE systemd.
#
# ADA KARENA KEGAGALAN NYATA 28 Sep 2026: seluruh pemeriksaan di atas HIJAU
# sementara unit yang diterbitkan DITOLAK systemd dengan
#
#   /etc/systemd/system/l2tp-vpn-mikrotik.service:25: Unbalanced quoting
#   Unit configuration has fatal error, unit will not be started
#
# Sebabnya: pemeriksaan di atas membaca TEMPLATE-nya (hasil `sed` dari skrip),
# bukan unit yang SUDAH DIEKSPANSI. Di template tertulis `$(seq 1 30)`; heredoc
# penulisnya tidak berkutip, jadi substitusi itu dijalankan saat menulis dan
# `seq` mencetak satu angka per BARIS — satu direktif pecah menjadi 30 baris.
# grep atas template tak mungkin melihat itu.
#
# Pelajarannya: menegaskan STRING yang ada tidak sama dengan menegaskan unit
# yang SAH. Bagian ini mengekspansi template dengan nilai boneka lalu menyerahkan
# hasilnya ke systemd untuk dinilai.
if ! command -v systemd-analyze >/dev/null 2>&1; then
  printf '[lewat] systemd-analyze tidak ada — verifikasi parse dilewati\n'
else
  TMPD=$(mktemp -d)
  trap 'rm -rf "$TMPD"' EXIT

  # Nilai boneka untuk setiap variabel yang dipakai template.
  DESK_IPSEC=" (tanpa IPsec)"
  VPN_HOST="203.0.113.10"
  UNIT_AFTER="network-online.target xl2tpd.service"
  UNIT_REQ=""
  UNIT_START="ExecStart=/bin/systemctl start xl2tpd.service"
  UNIT_STOPPOST=""
  TUNNEL_NAME="uji-tunnel"
  VPN_USER="radius-uji-abc123"
  export DESK_IPSEC VPN_HOST UNIT_AFTER UNIT_REQ UNIT_START UNIT_STOPPOST TUNNEL_NAME VPN_USER

  # Ekspansi template dengan SATU lintasan heredoc, persis seperti produksi.
  #
  # SENGAJA BUKAN `eval "cat <<EOF ... EOF"`: eval memproses string itu sekali
  # lagi SEBELUM heredoc-nya berjalan, jadi `\$(...)` yang di produksi lewat apa
  # adanya justru IKUT DIJALANKAN di tes. Tesnya lalu melaporkan bug yang tidak
  # ada di produksi, dan — lebih buruk — bisa MENUTUPI yang nyata.
  #
  # Skrip sekali-pakai di bawah memuat heredoc-nya secara LITERAL, jadi jumlah
  # lintasan ekspansinya sama dengan vpn-client-setup.sh: tepat satu.
  sed -n '/^\[Unit\]$/,/^EOF$/p' scripts/vpn-client-setup.sh | sed '$d' > "$TMPD/tmpl"
  {
    printf 'cat <<EOF\n'
    cat "$TMPD/tmpl"
    printf 'EOF\n'
  } > "$TMPD/gen.sh"
  bash "$TMPD/gen.sh" > "$TMPD/l2tp-uji-tunnel.service"

  # Satu direktif TIDAK BOLEH pecah jadi beberapa baris: setiap baris tak-kosong
  # yang bukan komentar harus berupa "Kunci=..." atau "[Seksi]".
  BARIS_LIAR=$(grep -nvE '^\[|^[A-Za-z][A-Za-z0-9]*=|^#|^$' "$TMPD/l2tp-uji-tunnel.service" || true)
  if [ -n "$BARIS_LIAR" ]; then
    bad "unit hasil ekspansi punya baris yang bukan direktif (satu direktif pecah jadi banyak baris):"
    printf '%s\n' "$BARIS_LIAR" | head -5 >&2
  else
    ok "setiap baris unit hasil ekspansi berupa direktif utuh"
  fi

  # Dan systemd sendiri yang menilai. Galat parse dilaporkan ke stderr;
  # keluhan soal unit lain di After= (mis. xl2tpd tak terpasang di mesin dev)
  # BUKAN galat sintaksis, jadi disaring.
  VERIF=$(systemd-analyze verify "$TMPD/l2tp-uji-tunnel.service" 2>&1 || true)
  PARSE_BURUK=$(printf '%s\n' "$VERIF" | grep -iE 'unbalanced|invalid|bad |fatal|unknown lvalue|missing|syntax' \
                | grep -viE 'not found|does not exist|Unknown unit type|command not found' || true)
  if [ -n "$PARSE_BURUK" ]; then
    bad "systemd-analyze verify menolak unit-nya:"
    printf '%s\n' "$PARSE_BURUK" | head -6 >&2
  else
    ok "systemd-analyze verify menerima unit hasil ekspansi"
  fi
fi

[ "$GAGAL" -eq 0 ] && printf '\nSEMUA PEMERIKSAAN LULUS\n' || printf '\nADA YANG GAGAL\n' >&2
exit "$GAGAL"
