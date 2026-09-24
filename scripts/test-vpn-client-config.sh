#!/usr/bin/env bash
# Uji berkas konfigurasi yang diterbitkan vpn-client-setup.sh.
#
# Ada karena satu kegagalan nyata (24 Sep 2026): baris penanda ditulis dengan
# '#' ke SEMUA berkas, padahal parser xl2tpd hanya mengenal ';'. Baris '#' di
# xl2tpd.conf dibaca sebagai data di luar section dan SELURUH berkas ditolak:
#
#   parse_config: line 1: data '# ...' occurs with no context
#   init: Unable to load config file
#
# Yang dilihat operator cuma "Job for xl2tpd.service failed" — pesan parse-nya
# hanya ada di journal, dan skrip berhenti di tengah karena set -e.
#
# Tanpa root, tanpa Docker, tanpa jaringan.
set -uo pipefail
cd "$(dirname "$0")/.."
SKRIP=scripts/vpn-client-setup.sh
GAGAL=0
ok()   { printf '[ok]   %s\n' "$*"; }
bad()  { printf '[GAGAL] %s\n' "$*" >&2; GAGAL=1; }

# Nilai contoh untuk mengembangkan heredoc.
VPN_HOST=103.242.104.67
VPN_USER=radius-jkt-04-d2851e
VPN_PASS=Rad96f2d6584d7dd52459
TUNNEL_NAME=vpn-mikrotik
NAS_POOL_CIDR=172.31.199.0/24
MTU=1450
VPN_PSK=psk-contoh
MARKER_TEKS="dikelola oleh vpn-client-setup.sh - jangan sunting tangan"
MARKER="# $MARKER_TEKS"
MARKER_XL2TPD="; $MARKER_TEKS"

blok() { # blok <path-literal-di-skrip>
  awk -v p="$1" '
    $0 ~ ("^tulis_berkas " p " <<") {ambil=1; next}
    ambil && /^EOF$/ {exit}
    ambil {print}
  ' "$SKRIP"
}

render() { eval "cat <<XEOF
$(blok "$1")
XEOF"; }

# ── 1. Karakter komentar per format ──────────────────────────────────────
XL=$(render /etc/xl2tpd/xl2tpd.conf)
if [ -z "$XL" ]; then
  bad "blok xl2tpd.conf tidak ketemu di $SKRIP"
elif [ "${XL:0:1}" = ";" ]; then
  ok "xl2tpd.conf memakai ';' — satu-satunya komentar yang dikenal parsernya"
else
  bad "xl2tpd.conf diawali '${XL:0:1}', bukan ';' — xl2tpd akan menolak SELURUH berkas"
fi

for f in /etc/sysctl.d/99-vpn-tunnel.conf /etc/ipsec.conf /etc/ipsec.secrets /etc/ppp/options.l2tpd.client; do
  ISI=$(render "$f")
  if [ -z "$ISI" ]; then
    bad "blok $f tidak ketemu"
  elif [ "${ISI:0:1}" = "#" ]; then
    ok "$(basename "$f") memakai '#'"
  else
    bad "$f diawali '${ISI:0:1}', bukan '#'"
  fi
done

# ── 2. Pengenalan berkas kelolaan harus cocok untuk KEDUA bentuk ─────────
# Kalau tidak, tiap kali skrip dijalankan ia membuat .bak baru untuk berkas
# yang sebenarnya miliknya sendiri — kebisingan yang menyesatkan saat
# menelusuri masalah.
if grep -q 'grep -qF "\$MARKER_TEKS"' "$SKRIP"; then
  ok "deteksi berkas kelolaan mencocokkan TEKS, bukan bentuk berkomentarnya"
else
  bad "deteksi masih memakai bentuk ber-'#' — berkas xl2tpd akan di-backup berulang"
fi

# ── 3. Uji dengan parser sungguhan kalau xl2tpd terpasang ────────────────
if command -v xl2tpd >/dev/null 2>&1; then
  T=$(mktemp -d)
  printf '%s\n' "$XL" > "$T/xl2tpd.conf"
  KELUARAN=$(timeout 3 xl2tpd -D -c "$T/xl2tpd.conf" -p "$T/p" -C "$T/c" 2>&1)
  if grep -qiE 'Unable to load config|occurs with no context' <<<"$KELUARAN"; then
    bad "parser xl2tpd menolak konfigurasi:"; printf '%s\n' "$KELUARAN" | head -5 >&2
  else
    ok "parser xl2tpd menerima konfigurasi yang diterbitkan"
  fi
  rm -rf "$T"
else
  printf '[lewat] xl2tpd tidak terpasang — uji parser dilewati\n'
fi

# ── 4. Kegagalan xl2tpd harus MENUNJUKKAN sebabnya ───────────────────────
if grep -q 'journalctl -u xl2tpd.service' "$SKRIP"; then
  ok "kegagalan start xl2tpd mencetak pesan aslinya dari journal"
else
  bad "kegagalan start xl2tpd tidak mencetak sebabnya — operator harus menebak"
fi

echo
[ "$GAGAL" = 0 ] && { echo "Semua uji konfigurasi vpn-client lolos."; exit 0; }
echo "Ada uji yang gagal." >&2; exit 1
