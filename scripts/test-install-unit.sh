#!/usr/bin/env bash
# Uji unit systemd yang diterbitkan install.sh.
#
# Ada karena satu kegagalan nyata (24 Sep 2026): hardening bawaan menutup
# persis lokasi yang menjadi pekerjaan service ini. Pembuatan instance gagal
# dengan "read-only file system" — dan baru terlihat saat instance PERTAMA
# dibuat, jauh sesudah installer melaporkan semua fase hijau dan health 200.
set -uo pipefail
cd "$(dirname "$0")/.."
GAGAL=0
ok()  { printf '[ok]   %s\n' "$*"; }
bad() { printf '[GAGAL] %s\n' "$*" >&2; GAGAL=1; }

UNIT=$(sed -n '/^cat > "\$tmp_unit"/,/^EOF$/p' install.sh)
[ -n "$UNIT" ] || { bad "blok unit systemd tidak ketemu di install.sh"; exit 1; }

# Lokasi yang WAJIB bisa ditulis. Tiap baris: <path> <alasan>
while read -r path alasan; do
  [ -n "$path" ] || continue
  if grep -qE "^ReadWritePaths=.*$path" <<<"$UNIT" || ! grep -qE '^ProtectSystem=' <<<"$UNIT"; then
    ok "$path bisa ditulis ($alasan)"
  else
    bad "$path TIDAK bisa ditulis — $alasan"
  fi
done <<'PATHS'
/etc/freeradius registry-port+virtual-server+metadata-instance
/etc/systemd/system unit-timer-maintenance-per-instance
PATHS

# /root dipakai sebagai RM_API_API_DIR_BASE (bawaan), dan ProtectHome=yes
# membuatnya kosong DAN tak terjangkau — ReadWritePaths tidak menolongnya.
if grep -qE '^ProtectHome=yes' <<<"$UNIT"; then
  bad "ProtectHome=yes menutup /root — direktori instance tak bisa dibuat"
else
  ok "ProtectHome tidak menutup /root (RM_API_API_DIR_BASE bawaan)"
fi

# Yang sesungguhnya ingin dijaga harus TETAP terjaga.
if grep -qE '^ProtectSystem=full' <<<"$UNIT"; then
  ok "ProtectSystem=full dipertahankan — /usr dan /boot tetap read-only"
else
  bad "ProtectSystem dilonggarkan seluruhnya — /usr dan /boot ikut terbuka"
fi
if grep -qE '^ReadWritePaths=.*(/usr|/boot)' <<<"$UNIT"; then
  bad "ReadWritePaths membuka /usr atau /boot — di luar kebutuhan service ini"
else
  ok "ReadWritePaths tidak membuka /usr maupun /boot"
fi

echo
[ "$GAGAL" = 0 ] && { echo "Semua uji unit systemd lolos."; exit 0; }
echo "Ada uji yang gagal." >&2; exit 1
