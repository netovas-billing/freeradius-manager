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

# ── 3b. Unit systemd harus mengikuti mode IPsec ──────────────────────────
# Template-nya sempat meng-HARDCODE `ExecStart=/usr/sbin/ipsec up`, padahal
# dengan USE_IPSEC=no strongswan tidak dipasang sama sekali. Unit-nya gagal
# start karena berkasnya tak ada, dan ExecStartPost — satu-satunya baris yang
# BENAR-BENAR mendial tunnel — tak pernah dijalankan.
unit_untuk() { # unit_untuk <yes|no>
  local mode="$1" after req start stoppost desk
  if [ "$mode" = yes ]; then
    after="network-online.target strongswan-starter.service xl2tpd.service"
    req="Requires=strongswan-starter.service xl2tpd.service"
    start="ExecStart=/usr/sbin/ipsec up $TUNNEL_NAME"
    stoppost="ExecStopPost=/usr/sbin/ipsec down $TUNNEL_NAME"
    desk="/IPsec"
  else
    after="network-online.target xl2tpd.service"
    req="Requires=xl2tpd.service"
    start="ExecStart=/bin/true"
    stoppost=""
    desk=" (tanpa IPsec)"
  fi
  UNIT_AFTER="$after" UNIT_REQ="$req" UNIT_START="$start" \
  UNIT_STOPPOST="$stoppost" DESK_IPSEC="$desk" \
  TUNNEL_NAME="$TUNNEL_NAME" VPN_HOST="$VPN_HOST" \
  bash -c 'eval "cat <<XEOF
$(awk "/^tulis_berkas \"\/etc\/systemd/{a=1;next} a&&/^EOF\$/{exit} a" '"$SKRIP"')
XEOF"'
}

U_NO=$(unit_untuk no)
U_YES=$(unit_untuk yes)

if grep -q '/usr/sbin/ipsec' <<<"$U_NO"; then
  bad "unit mode TANPA IPsec masih memanggil /usr/sbin/ipsec — berkasnya tak dipasang, unit pasti gagal"
else
  ok "unit mode tanpa IPsec tidak menyentuh /usr/sbin/ipsec"
fi
if grep -q 'ExecStart=/usr/sbin/ipsec up' <<<"$U_YES"; then
  ok "unit mode IPsec tetap menaikkan terowongan IPsec"
else
  bad "unit mode IPsec kehilangan 'ipsec up'"
fi
for u in "$U_NO" "$U_YES"; do
  grep -q 'l2tp-control' <<<"$u" || bad "unit kehilangan ExecStartPost — tak ada yang mendial tunnel"
done
ok "kedua mode tetap mendial lewat l2tp-control"

# ── 4. Kegagalan xl2tpd harus MENUNJUKKAN sebabnya ───────────────────────
if grep -q 'journalctl -u xl2tpd.service' "$SKRIP"; then
  ok "kegagalan start xl2tpd mencetak pesan aslinya dari journal"
else
  bad "kegagalan start xl2tpd tidak mencetak sebabnya — operator harus menebak"
fi
if grep -q 'journalctl -u "l2tp-\${TUNNEL_NAME}.service"' "$SKRIP"; then
  ok "kegagalan start unit tunnel juga mencetak pesan aslinya"
else
  bad "kegagalan unit tunnel masih bisu"
fi

echo
[ "$GAGAL" = 0 ] && { echo "Semua uji konfigurasi vpn-client lolos."; exit 0; }
echo "Ada uji yang gagal." >&2; exit 1
