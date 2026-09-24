#!/usr/bin/env bash
#
# vpn-client-setup.sh — sambungkan server FreeRADIUS (Debian) ke VPN
# concentrator MikroTik sebagai client L2TP/IPsec, lalu pasang route ke pool
# NAS supaya paket auth/acct/CoA bisa bolak-balik lewat tunnel.
#
# ── KENAPA SKRIP INI ADA ─────────────────────────────────────────────────────
# Sampai sekarang server RADIUS selalu ber-IP PUBLIK: NAS di balik VPN mengirim
# paket RADIUS keluar tunnel, di-masquerade concentrator, lalu sampai ke RADIUS
# lewat internet. Itu bekerja, tapi punya satu konsekuensi yang tidak bisa
# ditawar: FreeRADIUS melihat SEMUA NAS di satu concentrator sebagai SATU client
# (IP publik concentrator), sehingga seluruh NAS di belakangnya wajib memakai
# shared secret yang sama.
#
# Kalau server RADIUS ikut masuk ke VPN, tiap NAS kembali terlihat dengan IP
# tunnel-nya sendiri — secret bisa beda per NAS lagi, dan port RADIUS tidak
# perlu terbuka ke internet sama sekali.
#
# Skrip ini mengerjakan SISI DEBIAN-nya saja. Sisi concentrator (PPP secret,
# profile, pool, firewall) dikerjakan di MikroTik — lihat bagian "LANGKAH DI
# CONCENTRATOR" di bawah.
#
# ── JEBAKAN YANG SUDAH DITANGANI (semuanya pernah menggigit di lapangan) ─────
#  1. Debian 13 (trixie): paket `strongswan` TIDAK lagi menarik
#     `strongswan-starter`, sehingga perintah `ipsec` tidak ada dan
#     `strongswan-starter.service` tak terdefinisi. Di sini dipasang eksplisit.
#  2. `rp_filter` default 1 di Debian membuang paket yang masuk lewat tunnel
#     (jalur balik tidak simetris). Wajib 2 (loose) atau 0.
#  3. Route otomatis SESUDAH tunnel naik tidak boleh disaring dengan nama
#     interface (`$1` bisa ppp0, ppp1, ppp2 tergantung sesi lama yang masih
#     nyangkut). Disaring dengan PEER IP (`$6`) yang selalu berada di dalam
#     pool concentrator.
#  4. Semua berkas yang disentuh skrip ini ditandai header "dikelola oleh
#     vpn-client-setup.sh". Berkas yang sudah ada dan BUKAN milik skrip ini
#     di-backup dulu (.bak-<timestamp>), tidak ditimpa diam-diam.
#
# ── PAKAI ───────────────────────────────────────────────────────────────────
#   sudo VPN_HOST=103.242.104.67 \
#        VPN_USER=radius01 \
#        VPN_PASS='Pass_RAD_Strong_456!' \
#        VPN_PSK='ljnbillv1' \
#        NAS_POOL_CIDR=172.31.199.0/24 \
#        bash vpn-client-setup.sh
#
#   Concentrator yang L2TP server-nya TANPA IPsec (bawaan skrip ERP saat ini):
#   sudo USE_IPSEC=no \
#        VPN_HOST=103.242.104.67 VPN_USER=radius-utama-3f9c1a VPN_PASS='…' \
#        NAS_POOL_CIDR=172.31.199.0/24 \
#        bash vpn-client-setup.sh
#
#   Pilihan lain:
#     USE_IPSEC=no  concentrator tidak menuntut IPsec (paket TIDAK terenkripsi)
#     --dry-run     tampilkan yang akan dikerjakan, tanpa mengubah apa pun
#     --uninstall   lepas tunnel + hapus berkas yang dibuat skrip ini
#     --status      periksa keadaan tunnel sekarang lalu keluar
#
# ── LANGKAH DI CONCENTRATOR (MikroTik) — kerjakan SEBELUM skrip ini ─────────
#   /ip pool add name=pool-radius ranges=<IP_RADIUS_DI_POOL>
#   /ppp profile add name=profile-radius local-address=<GATEWAY_POOL> \
#       remote-address=pool-radius change-tcp-mss=yes use-encryption=yes
#   /ppp secret add name=<VPN_USER> password=<VPN_PASS> service=l2tp \
#       profile=profile-radius
#   # HANYA bila memakai IPsec. `use-ipsec=yes` (bukan `required`) supaya klien
#   # lama yang TANPA IPsec — termasuk seluruh NAS yang dibuat skrip ERP — tetap
#   # bisa menyambung. `required` akan memutus mereka semua sekaligus.
#   /interface l2tp-server server set enabled=yes use-ipsec=yes \
#       ipsec-secret=<VPN_PSK> default-profile=default-encryption \
#       authentication=mschap2
#
#   VPN_USER harus PERSIS nama PPP secret yang dibuat skrip "RADIUS di dalam
#   VPN" dari ERP (mis. radius-utama-3f9c1a) — bukan nama karangan sendiri.
#   # izinkan trafik pool <-> pool (NAS <-> RADIUS) di chain forward
#
set -euo pipefail

# ── Parameter ───────────────────────────────────────────────────────────────
VPN_HOST="${VPN_HOST:-}"                    # IP publik concentrator
VPN_USER="${VPN_USER:-}"                    # nama PPP secret untuk server RADIUS
VPN_PASS="${VPN_PASS:-}"                    # password PPP secret
VPN_PSK="${VPN_PSK:-}"                      # IPsec pre-shared key
NAS_POOL_CIDR="${NAS_POOL_CIDR:-}"          # pool IP tunnel NAS, mis. 172.31.199.0/24
EXTRA_ROUTES="${EXTRA_ROUTES:-}"            # CIDR tambahan, dipisah koma
TUNNEL_NAME="${TUNNEL_NAME:-vpn-mikrotik}"  # nama conn ipsec + LAC xl2tpd
MTU="${MTU:-1450}"
PEER_NET="${PEER_NET:-$NAS_POOL_CIDR}"      # rentang IP peer yang sah (untuk saringan ip-up)

# USE_IPSEC — concentrator ini menuntut IPsec atau tidak.
#
# PERIKSA DULU SEBELUM MENJALANKAN. Skrip concentrator yang diterbitkan ERP
# mengaktifkan L2TP server TANPA IPsec (`/interface l2tp-server server set
# enabled=yes default-profile=... authentication=mschap2`), dan klien NAS yang
# dibuatnya juga tanpa IPsec. Kalau concentrator Anda masih seperti itu,
# memaksa IPsec di sisi ini membuat `ipsec up` gagal dan tunnel tak pernah naik.
#
# USE_IPSEC=no → L2TP polos: paket TIDAK terenkripsi, hanya ter-enkapsulasi.
# Untuk lalu lintas RADIUS itu berarti password pelanggan (yang cuma
# di-obfuscate MD5) dan seluruh accounting lewat apa adanya. Pakai hanya bila
# jalur antara kedua mesin memang tepercaya.
USE_IPSEC="${USE_IPSEC:-yes}"

DRY_RUN=0
MODE="install"

for arg in "$@"; do
  case "$arg" in
    --dry-run)   DRY_RUN=1 ;;
    --uninstall) MODE="uninstall" ;;
    --status)    MODE="status" ;;
    -h|--help)   sed -n '2,60p' "$0"; exit 0 ;;
    *) echo "Argumen tidak dikenal: $arg" >&2; exit 2 ;;
  esac
done

# PENANDA berkas kelolaan skrip ini.
#
# Karakter komentarnya BERBEDA per format, dan ini bukan kerewelan: parser
# xl2tpd hanya mengenal ';'. Baris berawalan '#' di xl2tpd.conf dibaca sebagai
# DATA di luar section, dan xl2tpd menolak SELURUH berkas:
#
#   parse_config: line 1: data '# ...' occurs with no context
#   init: Unable to load config file
#
# Akibatnya service gagal start, skrip berhenti di tengah (set -e), dan
# operator melihat "Job for xl2tpd.service failed" tanpa sebab yang terlihat —
# pesan parse-nya hanya ada di journal. Terjadi nyata 24 Sep 2026.
#
# Teksnya dipisah dari karakter komentarnya supaya pengenalan berkas kelolaan
# (tulis_berkas) tetap cocok untuk KEDUA bentuk; kalau tidak, tiap kali skrip
# dijalankan ia membuat .bak baru untuk berkas yang sebenarnya miliknya sendiri.
#
# ASCII saja — tanda pisah panjang sempat dipakai di sini dan tak ada parser
# yang diuntungkan olehnya.
MARKER_TEKS="dikelola oleh vpn-client-setup.sh - jangan sunting tangan"
MARKER="# $MARKER_TEKS"
MARKER_XL2TPD="; $MARKER_TEKS"
STAMP="$(date +%Y%m%d-%H%M%S)"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m  ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m  !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mGAGAL:\033[0m %s\n' "$*" >&2; exit 1; }

jalankan() {
  if [ "$DRY_RUN" = 1 ]; then printf '  [dry-run] %s\n' "$*"; else eval "$@"; fi
}

# tulis_berkas <path> <<'EOF' … EOF
# Membaca isi dari stdin. Berkas yang sudah ada TANPA marker di-backup dulu.
tulis_berkas() {
  local path="$1" isi
  isi="$(cat)"
  if [ -e "$path" ] && ! grep -qF "$MARKER_TEKS" "$path" 2>/dev/null; then
    if [ "$DRY_RUN" = 1 ]; then
      printf '  [dry-run] backup %s -> %s.bak-%s\n' "$path" "$path" "$STAMP"
    else
      cp -a "$path" "${path}.bak-${STAMP}"
      warn "berkas lama disimpan: ${path}.bak-${STAMP}"
    fi
  fi
  if [ "$DRY_RUN" = 1 ]; then
    printf '  [dry-run] tulis %s (%d baris)\n' "$path" "$(printf '%s\n' "$isi" | wc -l)"
  else
    mkdir -p "$(dirname "$path")"
    printf '%s\n' "$isi" > "$path"
    ok "tulis $path"
  fi
}

# ── Pemeriksaan awal ────────────────────────────────────────────────────────
[ "$(id -u)" = 0 ] || die "jalankan sebagai root (sudo)."
command -v systemctl >/dev/null || die "butuh systemd."

if [ "$MODE" = "status" ]; then
  log "Status tunnel"
  ipsec status 2>/dev/null | sed 's/^/  /' || warn "perintah ipsec tidak ada"
  echo
  ip -4 addr show | awk '/^[0-9]+: ppp/{iface=$2} /inet /{if(iface) printf "  %s %s\n", iface, $2}'
  echo
  ip route | grep -E 'ppp' | sed 's/^/  route: /' || echo "  (tak ada route lewat ppp)"
  exit 0
fi

if [ "$MODE" = "uninstall" ]; then
  log "Melepas tunnel dan berkas yang dibuat skrip ini"
  jalankan "systemctl disable --now l2tp-${TUNNEL_NAME}.service 2>/dev/null || true"
  jalankan "ipsec down ${TUNNEL_NAME} 2>/dev/null || true"
  for f in "/etc/systemd/system/l2tp-${TUNNEL_NAME}.service" \
           "/etc/ppp/ip-up.d/00-vpn-routes" \
           "/etc/ppp/ip-down.d/00-vpn-routes" \
           "/etc/ppp/options.l2tpd.client" \
           "/etc/sysctl.d/99-vpn-tunnel.conf"; do
    [ -e "$f" ] && jalankan "rm -f '$f'" && ok "hapus $f"
  done
  warn "/etc/ipsec.conf, /etc/ipsec.secrets, /etc/xl2tpd/xl2tpd.conf, /etc/ppp/chap-secrets TIDAK dihapus"
  warn "(berkas itu bisa memuat konfigurasi lain — periksa & bersihkan manual)"
  jalankan "systemctl daemon-reload"
  exit 0
fi

case "$USE_IPSEC" in
  yes|no) : ;;
  *) die "USE_IPSEC harus 'yes' atau 'no' (dapat: $USE_IPSEC)" ;;
esac

WAJIB="VPN_HOST VPN_USER VPN_PASS NAS_POOL_CIDR"
[ "$USE_IPSEC" = yes ] && WAJIB="$WAJIB VPN_PSK"
for v in $WAJIB; do
  [ -n "${!v}" ] || die "$v wajib diisi. Lihat contoh di header skrip (--help)."
done
if [ "$USE_IPSEC" = no ]; then
  warn "USE_IPSEC=no — trafik TIDAK terenkripsi, hanya ter-enkapsulasi L2TP."
fi

# Validasi bentuk CIDR sejak awal: salah ketik di sini berakhir sebagai
# "tunnel naik tapi tidak bisa ping" yang jauh lebih mahal ditelusuri.
case "$NAS_POOL_CIDR" in
  */*) : ;;
  *) die "NAS_POOL_CIDR harus berbentuk CIDR, mis. 172.31.199.0/24 (dapat: $NAS_POOL_CIDR)" ;;
esac

log "Konfigurasi yang dipakai"
cat <<RINGKAS
  Concentrator : $VPN_HOST
  PPP user     : $VPN_USER
  Pool NAS     : $NAS_POOL_CIDR   (route dipasang ke sini lewat tunnel)
  Route ekstra : ${EXTRA_ROUTES:-(tidak ada)}
  Nama tunnel  : $TUNNEL_NAME
  IPsec        : $USE_IPSEC
  MTU/MRU      : $MTU
RINGKAS
[ "$DRY_RUN" = 1 ] && warn "MODE DRY-RUN — tidak ada yang diubah"

# ── 1. Paket ────────────────────────────────────────────────────────────────
log "1/8 Memasang paket"
PAKET="xl2tpd ppp"
[ "$USE_IPSEC" = yes ] && PAKET="strongswan strongswan-starter libcharon-extra-plugins $PAKET"
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] apt-get install -y %s\n' "$PAKET"
else
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  # shellcheck disable=SC2086
  apt-get install -y -qq $PAKET >/dev/null || {
    warn "apt gagal — mencoba refresh metadata (gejala 404 karena cache basi)"
    apt-get clean && apt-get update -qq
    # shellcheck disable=SC2086
    apt-get install -y -qq --fix-missing $PAKET >/dev/null
  }
  if [ "$USE_IPSEC" = yes ]; then
    command -v ipsec >/dev/null \
      || die "perintah 'ipsec' tetap tidak ada — pastikan paket strongswan-starter terpasang."
    ok "paket siap ($(ipsec --version 2>/dev/null | head -1))"
  else
    ok "paket siap (tanpa IPsec)"
  fi
fi

# ── 2. sysctl ───────────────────────────────────────────────────────────────
log "2/8 Menyetel sysctl (rp_filter longgar — kalau tidak, paket dari tunnel dibuang diam-diam)"
tulis_berkas /etc/sysctl.d/99-vpn-tunnel.conf <<EOF
$MARKER
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.ip_forward = 0
EOF
jalankan "sysctl --system >/dev/null"

# ── 3. IPsec ────────────────────────────────────────────────────────────────
log "3/8 Menyetel IPsec (strongSwan, mode transport untuk L2TP)"
if [ "$USE_IPSEC" = no ]; then
  ok "dilewati (USE_IPSEC=no)"
else
tulis_berkas /etc/ipsec.conf <<EOF
$MARKER
config setup
    charondebug="ike 1, knl 1, cfg 0"
    uniqueids=no

conn $TUNNEL_NAME
    keyexchange=ikev1
    authby=secret
    auto=add
    type=transport
    left=%defaultroute
    leftprotoport=17/1701
    right=$VPN_HOST
    rightprotoport=17/1701
    ike=aes128-sha1-modp1024,aes256-sha1-modp2048!
    esp=aes128-sha1,aes256-sha1!
    ikelifetime=8h
    keylife=1h
    dpdaction=restart
    dpddelay=30s
    dpdtimeout=120s
EOF

tulis_berkas /etc/ipsec.secrets <<EOF
$MARKER
$VPN_HOST %any : PSK "$VPN_PSK"
EOF
jalankan "chmod 600 /etc/ipsec.secrets"
fi

# ── 4. xl2tpd ───────────────────────────────────────────────────────────────
log "4/8 Menyetel xl2tpd"
tulis_berkas /etc/xl2tpd/xl2tpd.conf <<EOF
$MARKER_XL2TPD
[global]
port = 1701

[lac $TUNNEL_NAME]
lns = $VPN_HOST
ppp debug = yes
pppoptfile = /etc/ppp/options.l2tpd.client
length bit = yes
require chap = yes
refuse pap = yes
require authentication = yes
name = $VPN_USER
EOF

# ── 5. PPP ──────────────────────────────────────────────────────────────────
log "5/8 Menyetel PPP"
tulis_berkas /etc/ppp/options.l2tpd.client <<EOF
$MARKER
ipcp-accept-local
ipcp-accept-remote
refuse-eap
refuse-pap
refuse-chap
refuse-mschap
require-mschap-v2
noccp
noauth
mtu $MTU
mru $MTU
noipdefault
nodefaultroute
usepeerdns
connect-delay 5000
name $VPN_USER
remotename $TUNNEL_NAME
lcp-echo-interval 30
lcp-echo-failure 4
EOF

# chap-secrets dipakai bersama layanan lain, jadi barisnya DISISIPKAN,
# bukan berkasnya ditimpa.
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] sisipkan baris chap-secrets untuk %s\n' "$VPN_USER"
else
  touch /etc/ppp/chap-secrets
  chmod 600 /etc/ppp/chap-secrets
  BARIS="\"$VPN_USER\"    *    \"$VPN_PASS\"    *"
  if grep -qE "^\"?$VPN_USER\"?[[:space:]]" /etc/ppp/chap-secrets; then
    cp -a /etc/ppp/chap-secrets "/etc/ppp/chap-secrets.bak-${STAMP}"
    grep -vE "^\"?$VPN_USER\"?[[:space:]]" "/etc/ppp/chap-secrets.bak-${STAMP}" > /etc/ppp/chap-secrets
    warn "baris lama untuk $VPN_USER diganti (backup: /etc/ppp/chap-secrets.bak-${STAMP})"
  fi
  printf '%s\n' "$BARIS" >> /etc/ppp/chap-secrets
  ok "chap-secrets diperbarui"
fi

# ── 6. Route otomatis saat tunnel naik ──────────────────────────────────────
log "6/8 Memasang route otomatis (disaring PEER IP, bukan nama interface)"
ROUTES_LIST="$NAS_POOL_CIDR"
if [ -n "$EXTRA_ROUTES" ]; then
  ROUTES_LIST="$ROUTES_LIST,$EXTRA_ROUTES"
fi

BUAT_ROUTE_SCRIPT() {
  local aksi="$1"   # add | del
  cat <<EOF
#!/usr/bin/env bash
$MARKER
#
# Argumen dari pppd: \$1=interface \$2=tty \$3=speed \$4=local-ip \$5=? \$6=peer-ip
# (\$5 = IP kita, \$6 = IP peer/gateway tunnel).
#
# Saringan memakai PEER IP, BUKAN "\$1" = "ppp0": pppd memberi nomor interface
# berikutnya yang bebas, jadi sesi yang sempat nyangkut membuat tunnel naik
# sebagai ppp1/ppp2 dan saringan berbasis nama diam-diam tidak pernah cocok.
PEER="\$6"
IFACE="\$1"
PEER_NET="$PEER_NET"
ROUTES="$ROUTES_LIST"

ip_ke_int() { local IFS=.; read -r a b c d <<<"\$1"; echo \$(( (a<<24)|(b<<16)|(c<<8)|d )); }
di_dalam_cidr() {
  local ip="\$1" cidr="\$2" net bits maski
  net="\${cidr%/*}"; bits="\${cidr#*/}"
  case "\$net" in *.*.*.*) : ;; *) return 1 ;; esac
  maski=\$(( bits == 0 ? 0 : (0xFFFFFFFF << (32 - bits)) & 0xFFFFFFFF ))
  [ \$(( \$(ip_ke_int "\$ip") & maski )) -eq \$(( \$(ip_ke_int "\$net") & maski )) ]
}

di_dalam_cidr "\$PEER" "\$PEER_NET" || exit 0

IFS=,
for r in \$ROUTES; do
  [ -n "\$r" ] || continue
  ip route $aksi "\$r" dev "\$IFACE" 2>/dev/null || true
  logger -t vpn-tunnel "route \$r $aksi via \$IFACE (peer=\$PEER)"
done
exit 0
EOF
}

if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] tulis /etc/ppp/ip-up.d/00-vpn-routes dan ip-down.d/00-vpn-routes\n'
else
  BUAT_ROUTE_SCRIPT add > /etc/ppp/ip-up.d/00-vpn-routes
  BUAT_ROUTE_SCRIPT del > /etc/ppp/ip-down.d/00-vpn-routes
  chmod +x /etc/ppp/ip-up.d/00-vpn-routes /etc/ppp/ip-down.d/00-vpn-routes
  ok "route otomatis terpasang untuk: $ROUTES_LIST"
fi

# ── 7. systemd: tunnel naik sendiri setelah reboot ──────────────────────────
log "7/8 Memasang unit systemd (tunnel naik lagi setelah reboot)"
if [ "$USE_IPSEC" = yes ]; then
  UNIT_AFTER="network-online.target strongswan-starter.service xl2tpd.service"
  UNIT_REQ="Requires=strongswan-starter.service xl2tpd.service"
  UNIT_START="$UNIT_START"
  UNIT_STOPPOST="$UNIT_STOPPOST"
else
  # Tanpa IPsec tak ada yang perlu dinaikkan lebih dulu; xl2tpd yang mendial.
  UNIT_AFTER="network-online.target xl2tpd.service"
  UNIT_REQ="Requires=xl2tpd.service"
  UNIT_START="ExecStart=/bin/true"
  UNIT_STOPPOST=""
fi
tulis_berkas "/etc/systemd/system/l2tp-${TUNNEL_NAME}.service" <<EOF
[Unit]
Description=Tunnel L2TP/IPsec ke VPN concentrator ($VPN_HOST)
Documentation=vpn-client-setup.sh
After=$UNIT_AFTER
Wants=network-online.target
$UNIT_REQ

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=/bin/sleep 5
ExecStart=/usr/sbin/ipsec up $TUNNEL_NAME
ExecStartPost=/bin/bash -c 'sleep 3 && echo "c $TUNNEL_NAME" > /var/run/xl2tpd/l2tp-control'
ExecStop=/bin/bash -c 'echo "d $TUNNEL_NAME" > /var/run/xl2tpd/l2tp-control'
ExecStopPost=/usr/sbin/ipsec down $TUNNEL_NAME

[Install]
WantedBy=multi-user.target
EOF

jalankan "systemctl daemon-reload"
if [ "$USE_IPSEC" = yes ]; then
  jalankan "systemctl enable --now strongswan-starter.service >/dev/null 2>&1 || true"
  jalankan "systemctl restart strongswan-starter.service"
fi
# Kalau xl2tpd menolak start, sebabnya HAMPIR SELALU galat parsing config yang
# hanya tercetak di journal — systemd sendiri cuma melaporkan status=1. Tanpa
# baris di bawah, operator berhenti di "Job for xl2tpd.service failed" dan
# harus menebak. Itu memakan beberapa putaran bolak-balik pada 24 Sep 2026.
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] systemctl restart xl2tpd.service\n'
elif ! systemctl restart xl2tpd.service; then
  warn "xl2tpd menolak start. Pesan aslinya:"
  journalctl -u xl2tpd.service --no-pager -n 15 2>/dev/null \
    | sed 's/^/      /' >&2 || true
  die "xl2tpd gagal start — lihat pesan di atas (sering: galat parsing /etc/xl2tpd/xl2tpd.conf)"
fi
jalankan "systemctl enable l2tp-${TUNNEL_NAME}.service >/dev/null"
jalankan "systemctl restart l2tp-${TUNNEL_NAME}.service"

# ── 8. Verifikasi ───────────────────────────────────────────────────────────
log "8/8 Verifikasi"
if [ "$DRY_RUN" = 1 ]; then
  printf '  [dry-run] lewati verifikasi\n'
  exit 0
fi

sleep 8
GAGAL=0

if [ "$USE_IPSEC" = no ]; then
  ok "IPsec dilewati (USE_IPSEC=no)"
elif ipsec status 2>/dev/null | grep -q "$TUNNEL_NAME.*ESTABLISHED"; then
  ok "IPsec ESTABLISHED"
else
  warn "IPsec belum ESTABLISHED — cek: journalctl -u strongswan-starter -n 50"
  warn "  'no proposal chosen' = cipher beda · 'AUTHENTICATION_FAILED' = PSK beda"
  GAGAL=1
fi

IFACE="$(ip -o -4 addr show | awk '/ ppp/{print $2; exit}')"
IPTUN="$(ip -o -4 addr show dev "${IFACE:-ppp0}" 2>/dev/null | awk '{print $4}' | cut -d/ -f1)"
if [ -n "$IPTUN" ]; then
  ok "tunnel naik di $IFACE dengan IP $IPTUN"
else
  warn "interface ppp belum ada — cek: journalctl -t xl2tpd -n 50 ; cek /etc/ppp/chap-secrets"
  GAGAL=1
fi

if ip route | grep -q "$NAS_POOL_CIDR"; then
  ok "route ke $NAS_POOL_CIDR terpasang"
else
  warn "route ke $NAS_POOL_CIDR BELUM ada — tunnel mungkin naik sebelum skrip route terpasang."
  warn "  coba: systemctl restart l2tp-${TUNNEL_NAME}.service"
  GAGAL=1
fi

echo
log "Langkah berikutnya"
cat <<LANJUT
  1. Daftarkan IP tunnel server RADIUS ini di ERP: ${IPTUN:-<belum dapat IP>}
     (dipakai sebagai alamat RADIUS untuk NAS yang satu concentrator).
  2. Pastikan FreeRADIUS mendengar di semua interface — template
     freeradius-manager memakai 'ipaddr = *', jadi otomatis ikut menjawab di
     IP tunnel. Verifikasi: ss -ulnp | grep freeradius
  3. Di ERP, skrip NAS untuk router yang satu concentrator harus memakai
     alamat RADIUS ${IPTUN:-<IP tunnel>} — BUKAN IP publik.
  4. clients.conf / tabel nas akan melihat NAS dengan IP TUNNEL-nya
     (mis. 172.31.199.x), bukan lagi IP publik concentrator.
LANJUT

if [ "$GAGAL" = 1 ]; then
  echo
  warn "Ada langkah yang belum hijau — perbaiki dulu sebelum memindahkan NAS."
  exit 1
fi
ok "Selesai."
