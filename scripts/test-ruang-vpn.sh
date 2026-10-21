#!/usr/bin/env bash
# Uji penjaga ruang VPN di vpn-client-setup.sh (blok "periksa-ruang-vpn").
#
# Kejadian 21 Okt 2026 (appliance staging JKT-1): docker0 172.17.0.0/16
# memegang gateway pool NAS 172.17.0.1 → paket concentrator dibuang sebagai
# martian, REST API tak terjangkau, auth NAS akan gagal. VM RADIUS Manager
# terpasang native (tanpa Docker), tapi Docker yang dipasang belakangan atau
# LAN 172.16–31 menghasilkan kegagalan yang sama — skrip WAJIB menolak.
#
# Tanpa root, tanpa jaringan: blok fungsinya diambil dari skrip lalu
# alamat_lokal diganti tiruan.
set -uo pipefail
cd "$(dirname "$0")/.."
SKRIP=scripts/vpn-client-setup.sh
GAGAL=0
ok()  { printf '[ok]   %s\n' "$*"; }
bad() { printf '[GAGAL] %s\n' "$*" >&2; GAGAL=1; }

blok="$(awk '/^# >>> periksa-ruang-vpn/{a=1} a{print} /^# <<< periksa-ruang-vpn/{exit}' "$SKRIP")"
[ -n "$blok" ] || { bad "blok periksa-ruang-vpn tidak ditemukan di $SKRIP"; exit 1; }
eval "$blok"

kasus() { # kasus <nama> <pool> <harap: tolak|peringatan|aman> <baris alamat...>
  local nama="$1" pool="$2" harap="$3"; shift 3
  local isi; isi="$(printf '%s\n' "$@")"
  alamat_lokal() { printf '%s\n' "$isi"; }
  local tolak rawan; tolak="$(alamat_bentrok_vpn "$pool")"; rawan="$(alamat_rawan_vpn "$pool")"
  local dapat=aman
  [ -n "$rawan" ] && dapat=peringatan
  [ -n "$tolak" ] && dapat=tolak
  if [ "$dapat" = "$harap" ]; then ok "$nama"; else bad "$nama (dapat: $dapat — ${tolak:-$rawan})"; fi
}

kasus "LAN 192.168 + tunnel ppp → aman"                 172.17.0.0/17   aman       "ens18 192.168.10.56/24" "ppp0 172.16.0.1/32"
kasus "JKT-1: docker0 172.17/16 vs pool 172.17.0.0/17"  172.17.0.0/17   tolak      "ens18 192.168.10.56/24" "docker0 172.17.0.1/16"
kasus "compose br-x 172.18/16 vs pool 172.18.0.0/17"    172.18.0.0/17   tolak      "br-abc 172.18.0.1/16"
kasus "LAN di ruang RADIUS 172.16.5.0/24"               172.31.199.0/24 tolak      "eth0 172.16.5.20/24"
kasus "PROD radius-manager-jkt: LAN 172.17.17/24, pool 172.31.199/24 → peringatan saja" \
                                                        172.31.199.0/24 peringatan "ens18 172.17.17.2/24" "ppp0 10.0.0.2/32"
kasus "docker0 172.17/16 vs pool 172.31.199/24 → peringatan" 172.31.199.0/24 peringatan "docker0 172.17.0.1/16"
kasus "Docker sudah di 198.18/15 → aman"                172.17.0.0/17   aman       "docker0 198.18.0.1/24" "br-x 198.19.0.1/24"
kasus "172.32 / 10.x bukan ruang VPN"                   172.17.0.0/17   aman       "eth0 172.32.0.5/24" "eth1 10.17.0.5/16"
cek_iris() { if cidr_beririsan "$1" "$2"; then echo y; else echo n; fi; }
[ "$(cek_iris 172.17.17.0/24 172.31.199.0/24)" = n ] && ok "172.17.17/24 tak beririsan 172.31.199/24" || bad "irisan salah (1)"
[ "$(cek_iris 172.17.0.1/16 172.17.0.0/17)" = y ] && ok "172.17/16 beririsan 172.17.0.0/17" || bad "irisan salah (2)"

NAS_POOL_CIDR=172.17.0.0/17
alamat_lokal() { echo "docker0 172.17.0.1/16"; }
pesan="$(pesan_bentrok_vpn "$(alamat_bentrok_vpn "$NAS_POOL_CIDR")")"
case "$pesan" in *198.18.0.1/24*) ok "pesan Docker menyebut daemon.json 198.18/15" ;; *) bad "pesan Docker tak menyebut obatnya: $pesan" ;; esac
alamat_lokal() { echo "eth0 172.17.0.20/24"; }
pesan="$(pesan_bentrok_vpn "$(alamat_bentrok_vpn "$NAS_POOL_CIDR")")"
case "$pesan" in *"LAN mesin ini"*) ok "pesan LAN menyebut LAN" ;; *) bad "pesan LAN salah: $pesan" ;; esac

# Penjaganya dipanggil SEBELUM langkah pemasangan pertama.
p_cek=$(grep -n 'BENTROK_VPN="$(alamat_bentrok_vpn' "$SKRIP" | head -1 | cut -d: -f1)
p_paket=$(grep -n '^log "1/' "$SKRIP" | head -1 | cut -d: -f1)
if [ -n "$p_cek" ] && [ -n "$p_paket" ] && [ "$p_cek" -lt "$p_paket" ]; then
  ok "pemeriksaan jalan sebelum langkah 1 (belum ada yang diubah)"
else
  bad "pemeriksaan tidak mendahului langkah pemasangan (cek=$p_cek paket=$p_paket)"
fi

exit $GAGAL
