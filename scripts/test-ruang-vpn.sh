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

kasus() { # kasus <nama> <harap: bentrok|aman> <baris alamat...>
  local nama="$1" harap="$2"; shift 2
  local isi; isi="$(printf '%s\n' "$@")"
  alamat_lokal() { printf '%s\n' "$isi"; }
  local hasil; hasil="$(alamat_bentrok_vpn)"
  if { [ "$harap" = bentrok ] && [ -n "$hasil" ]; } || { [ "$harap" = aman ] && [ -z "$hasil" ]; }; then
    ok "$nama"
  else
    bad "$nama (dapat: ${hasil:-kosong})"
  fi
}

kasus "LAN 192.168 + tunnel ppp → aman"     aman    "ens18 192.168.10.56/24" "ppp0 172.16.0.1/32"
kasus "Docker bawaan docker0 172.17/16"     bentrok "ens18 192.168.10.56/24" "docker0 172.17.0.1/16"
kasus "jaringan compose br-x 172.18/16"     bentrok "br-abc 172.18.0.1/16"
kasus "LAN 172.16.5.0/24"                   bentrok "eth0 172.16.5.20/24"
kasus "LAN 172.31.x"                        bentrok "eth0 172.31.0.10/16"
kasus "Docker sudah di 198.18/15 → aman"    aman    "docker0 198.18.0.1/24" "br-x 198.19.0.1/24"
kasus "172.32 / 10.x bukan ruang VPN"       aman    "eth0 172.32.0.5/24" "eth1 10.17.0.5/16"

alamat_lokal() { echo "docker0 172.17.0.1/16"; }
pesan="$(pesan_bentrok_vpn "$(alamat_bentrok_vpn)")"
case "$pesan" in *198.18.0.1/24*) ok "pesan Docker menyebut daemon.json 198.18/15" ;; *) bad "pesan Docker tak menyebut obatnya: $pesan" ;; esac
alamat_lokal() { echo "eth0 172.16.5.20/24"; }
pesan="$(pesan_bentrok_vpn "$(alamat_bentrok_vpn)")"
case "$pesan" in *"LAN mesin ini"*) ok "pesan LAN menyebut LAN" ;; *) bad "pesan LAN salah: $pesan" ;; esac

# Penjaganya dipanggil SEBELUM langkah pemasangan pertama.
p_cek=$(grep -n 'BENTROK_VPN="$(alamat_bentrok_vpn)"' "$SKRIP" | head -1 | cut -d: -f1)
p_paket=$(grep -n '^log "1/' "$SKRIP" | head -1 | cut -d: -f1)
if [ -n "$p_cek" ] && [ -n "$p_paket" ] && [ "$p_cek" -lt "$p_paket" ]; then
  ok "pemeriksaan jalan sebelum langkah 1 (belum ada yang diubah)"
else
  bad "pemeriksaan tidak mendahului langkah pemasangan (cek=$p_cek paket=$p_paket)"
fi

exit $GAGAL
