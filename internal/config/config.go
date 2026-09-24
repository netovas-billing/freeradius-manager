// Package config loads runtime configuration for radius-manager-api from
// environment variables. See deployments/systemd/radius-manager-api.service
// for canonical env names.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen        string // RM_API_LISTEN, default 127.0.0.1:9000
	TokenFile     string // RM_API_TOKEN_FILE
	Token         string // RM_API_TOKEN (alternative for single-token mode)
	FreeRADIUSDir string // RM_API_FREERADIUS_DIR, default /etc/freeradius/3.0
	StateDir      string // RM_API_STATE_DIR, default /var/lib/radius-manager-api
	VPNIP         string // RM_API_VPN_IP (advertised in /v1/server/info)
	CapacityMax   int    // RM_API_CAPACITY_MAX, default 50
	LogFormat     string // RM_API_LOG_FORMAT: json | text (default json)
	AuditLogPath  string // RM_API_AUDIT_LOG, default /var/log/radius-manager-api/audit.log
	APIDirBase    string // RM_API_API_DIR_BASE, default /root (matches bash)
	APIPublishIP  string // RM_API_API_PUBLISH_IP, default $RM_API_VPN_IP or 0.0.0.0
	DBDSN         string // RM_API_DB_DSN, MariaDB DSN for management user

	// APIPortStart adalah awal blok port HTTP freeradius-api di mesin ini
	// (RM_API_API_PORT_START; bawaan 8100 hanya jaring pengaman — nilai
	// sebenarnya dialokasikan ERP: base 20000 + k*1000, api_port_start =
	// base + 100). Harus BERBEDA antar VM RADIUS
	// yang bernaung di satu VPN concentrator: backend masuk lewat DSTNAT 1:1
	// di IP publik concentrator, jadi dua VM dengan blok port yang sama
	// bertabrakan di aturan NAT — hanya satu yang terjangkau dan yang lain
	// gagal diam-diam. Nilai harus cocok dengan aturan DSTNAT concentrator.
	// Lihat manager.NewPortRegistryWithAPIStart.
	//
	// 0 = env tidak diisi ATAU isinya tidak terbaca sebagai angka → manager
	// memakai bawaannya. Batas kewajaran nilainya juga ditegakkan di manager,
	// supaya semua pemanggil ikut terlindungi, bukan cuma jalur env ini.
	APIPortStart int

	// APIPortStartRaw menyimpan isi RM_API_API_PORT_START apa adanya ("" kalau
	// env memang tidak diisi). Gunanya membedakan "tidak diisi" dari "diisi
	// tapi ditolak": tanpa pembeda ini, salah ketik seperti "delapanribu" atau
	// "0" sama-sama jadi APIPortStart == 0, dipakai-bawaan TANPA peringatan,
	// dan instance lahir di luar blok port yang di-DSTNAT concentrator —
	// backend tidak pernah bisa menghubunginya dan tak ada galat di mana pun.
	// main.go memakai field ini untuk memberi WARN beserta nilai mentahnya.
	APIPortStartRaw string

	// freeradius-api bootstrap (v0.2.0). When BootstrapAPIRepo is non-empty,
	// CreateInstance runs the template-once + venv flow.
	BootstrapAPIRepo     string // RM_API_BOOTSTRAP_REPO, e.g. https://github.com/heirro/freeradius-api
	BootstrapTemplateDir string // RM_API_BOOTSTRAP_TEMPLATE_DIR, e.g. /var/lib/radius-manager-api/freeradius-api-template
	BootstrapSkipPull    bool   // RM_API_BOOTSTRAP_SKIP_PULL=true to disable git pull on EnsureTemplate

	// SystemdBackend selects which Systemctl implementation runs the
	// per-instance freeradius-api units. Production Linux defaults to
	// "systemd"; the Docker dev stack sets "supervisord" because the
	// container runs supervisord as PID 1 (no real systemd available).
	// RM_API_SYSTEMD_BACKEND, default "systemd".
	SystemdBackend string

	// DBHost / DBPort are the values written into the per-instance
	// FreeRADIUS sql module template AND into the freeradius-api .env.
	// On a real RADIUS VM both freeradius and the per-instance API live
	// on the same host as MariaDB, so the historical default "localhost"
	// is correct. In a multi-container Docker setup MariaDB lives in
	// another service, so DBHost is set to "mariadb" via env.
	// RM_API_INSTANCE_DB_HOST, default "localhost".
	// RM_API_INSTANCE_DB_PORT, default 3306.
	InstanceDBHost string
	InstanceDBPort int

	// MaintenanceBackend selects the system.Maintenance backend used to
	// schedule per-instance autoclearzombie / autobackups3 jobs.
	//   "systemd"     — production Linux; .timer + .service unit pair.
	//   "supervisord" — Docker dev; sleep-loop program.
	//   "none"        — disables maintenance entirely (preserves v0.2.0).
	// RM_API_MAINTENANCE_BACKEND, default "systemd".
	MaintenanceBackend string

	// S3Remote / S3Bucket / S3BackupRoot configure the autobackups3
	// destination embedded into the script and into the timer's env.
	// Empty S3Remote disables the backup timer (autoclearzombie still
	// runs). Mirrors the constants from radius-manager.sh:
	//   S3_REMOTE        ljns3
	//   S3_BUCKET        backup-db
	//   S3_BACKUP_ROOT   radiusdb
	// RM_API_S3_REMOTE, RM_API_S3_BUCKET, RM_API_S3_BACKUP_ROOT (default "radiusdb").
	S3Remote     string
	S3Bucket     string
	S3BackupRoot string
}

func Load() (*Config, error) {
	c := &Config{
		Listen:        getenv("RM_API_LISTEN", "127.0.0.1:9000"),
		TokenFile:     os.Getenv("RM_API_TOKEN_FILE"),
		Token:         os.Getenv("RM_API_TOKEN"),
		FreeRADIUSDir: getenv("RM_API_FREERADIUS_DIR", "/etc/freeradius/3.0"),
		StateDir:      getenv("RM_API_STATE_DIR", "/var/lib/radius-manager-api"),
		VPNIP:         os.Getenv("RM_API_VPN_IP"),
		LogFormat:     strings.ToLower(getenv("RM_API_LOG_FORMAT", "json")),
		AuditLogPath:  getenv("RM_API_AUDIT_LOG", "/var/log/radius-manager-api/audit.log"),
		APIDirBase:           getenv("RM_API_API_DIR_BASE", "/root"),
		APIPublishIP:         getenv("RM_API_API_PUBLISH_IP", ""),
		DBDSN:                os.Getenv("RM_API_DB_DSN"),
		BootstrapAPIRepo:     os.Getenv("RM_API_BOOTSTRAP_REPO"),
		BootstrapTemplateDir: getenv("RM_API_BOOTSTRAP_TEMPLATE_DIR", "/var/lib/radius-manager-api/freeradius-api-template"),
		BootstrapSkipPull:    strings.EqualFold(os.Getenv("RM_API_BOOTSTRAP_SKIP_PULL"), "true"),
		SystemdBackend:       strings.ToLower(getenv("RM_API_SYSTEMD_BACKEND", "systemd")),
		InstanceDBHost:       getenv("RM_API_INSTANCE_DB_HOST", "localhost"),
		MaintenanceBackend:   strings.ToLower(getenv("RM_API_MAINTENANCE_BACKEND", "systemd")),
		S3Remote:             os.Getenv("RM_API_S3_REMOTE"),
		S3Bucket:             os.Getenv("RM_API_S3_BUCKET"),
		S3BackupRoot:         getenv("RM_API_S3_BACKUP_ROOT", "radiusdb"),
	}
	// Sengaja TIDAK mengembalikan error: nilai yang aneh cukup diabaikan dan
	// mesin tetap naik dengan blok port bawaan. Tapi nilai mentahnya disimpan
	// supaya start-up bisa memberi WARN — "diisi tapi ditolak" tidak boleh
	// terlihat sama dengan "tidak diisi", karena bedanya adalah instance yang
	// lahir di luar blok port yang di-NAT (gagal senyap) versus mesin yang
	// memang sengaja memakai blok bawaan.
	if raw := os.Getenv("RM_API_API_PORT_START"); strings.TrimSpace(raw) != "" {
		c.APIPortStartRaw = raw
		if n, ok := ParseAPIPortStart(raw); ok {
			c.APIPortStart = n
		}
	}
	dbPort := getenv("RM_API_INSTANCE_DB_PORT", "3306")
	if n, err := strconv.Atoi(dbPort); err == nil && n > 0 {
		c.InstanceDBPort = n
	} else {
		return nil, fmt.Errorf("invalid RM_API_INSTANCE_DB_PORT %q: must be positive integer", dbPort)
	}
	if c.APIPublishIP == "" {
		// Sensible default: advertise VPN IP if known, otherwise 0.0.0.0.
		if c.VPNIP != "" {
			c.APIPublishIP = c.VPNIP
		} else {
			c.APIPublishIP = "0.0.0.0"
		}
	}
	cap := getenv("RM_API_CAPACITY_MAX", "50")
	n, err := strconv.Atoi(cap)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("invalid RM_API_CAPACITY_MAX %q: must be positive integer", cap)
	}
	c.CapacityMax = n

	if c.Token == "" && c.TokenFile == "" {
		return nil, fmt.Errorf("either RM_API_TOKEN or RM_API_TOKEN_FILE must be set")
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ParseAPIPortStart membaca isi RM_API_API_PORT_START dengan aturan yang SAMA
// PERSIS dengan sanitize_api_port_start() di radius-manager.sh:
//
//  1. pangkas HANYA spasi di UJUNG (berkas env sering disunting tangan; satu
//     spasi di belakang nilai bukan salah ketik yang berarti);
//  2. sisanya harus digit semua — nol di depan boleh dan dibaca DESIMAL
//     ("0020100" → 20100, bukan oktal);
//  3. selain itu ditolak (ok=false), termasuk spasi di TENGAH ("20 100"),
//     tanda "+"/"-", dan angka yang kepanjangan.
//
// Kenapa kedua sisi harus identik: skrip bash dan RM-API Go menulis SATU
// .port_registry yang sama. Kalau satu sisi menerima nilai yang ditolak sisi
// lain, mesin yang sama memakai DUA blok port berbeda — instance yang lahir
// lewat jalur yang jatuh ke bawaan 8100 tidak ikut di-DSTNAT concentrator,
// provisioning tetap "berhasil", dan backend tak pernah bisa menghubunginya.
//
// Rentang kewajaran (1024–64000) TIDAK diperiksa di sini: itu tugas
// manager.NewPortRegistryWithAPIStart, supaya semua pemanggil ikut terlindungi.
func ParseAPIPortStart(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	// Buang nol di depan supaya "0020100" dan "20100" diperlakukan sama, lalu
	// tolak yang kepanjangan sebelum dikonversi — angka raksasa tidak boleh
	// mengandalkan perilaku overflow strconv.
	stripped := strings.TrimLeft(trimmed, "0")
	if stripped == "" {
		stripped = "0"
	}
	if len(stripped) > 5 {
		return 0, false
	}
	n, err := strconv.Atoi(stripped)
	if err != nil {
		return 0, false
	}
	return n, true
}
