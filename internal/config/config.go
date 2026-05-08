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
