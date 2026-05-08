// Command radius-manager-api is the HTTP control plane for FreeRADIUS
// instance lifecycle on a single RADIUS Server VM.
//
// See docs/SRS-RadiusManagerAPI.md for the full specification.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/heirro/freeradius-manager/internal/api"
	"github.com/heirro/freeradius-manager/internal/config"
	"github.com/heirro/freeradius-manager/internal/manager"
	"github.com/heirro/freeradius-manager/internal/schema"
	"github.com/heirro/freeradius-manager/internal/system"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "init":
		if err := runInit(); err != nil {
			fmt.Fprintln(os.Stderr, "init:", err)
			os.Exit(1)
		}
	case "version", "-v", "--version":
		fmt.Println("radius-manager-api", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `radius-manager-api %s

Usage:
  radius-manager-api serve      Start the HTTP API server.
  radius-manager-api init       Generate a fresh API token (prints to stdout).
  radius-manager-api version    Print version.
  radius-manager-api help       Show this help.

Environment (read by 'serve'):
  RM_API_LISTEN            bind address, default 127.0.0.1:9000
  RM_API_TOKEN             single API token (use this OR RM_API_TOKEN_FILE)
  RM_API_TOKEN_FILE        path to file containing the token
  RM_API_FREERADIUS_DIR    default /etc/freeradius/3.0
  RM_API_STATE_DIR         default /var/lib/radius-manager-api
  RM_API_VPN_IP            advertised in /v1/server/info
  RM_API_CAPACITY_MAX      max instances on this server, default 50
  RM_API_LOG_FORMAT        json | text, default json
  RM_API_AUDIT_LOG         path for JSON-line audit log, default /var/log/radius-manager-api/audit.log
  RM_API_API_DIR_BASE      where per-instance freeradius-api dirs live, default /root
  RM_API_API_PUBLISH_IP    public IP advertised in WEB_API_URL (defaults to VPN_IP)
  RM_API_DB_DSN            MariaDB DSN for management (e.g. root@unix(/var/run/mysqld/mysqld.sock)/);
                           when unset, write operations return 501 Not Implemented (read-only mode)
  RM_API_BOOTSTRAP_REPO            git URL for the freeradius-api repo; when empty, v0.2.0
                                   bootstrap (clone + venv + .env per instance) is skipped
  RM_API_BOOTSTRAP_TEMPLATE_DIR    where the cloned template lives, default /var/lib/radius-manager-api/freeradius-api-template
  RM_API_BOOTSTRAP_SKIP_PULL       set to "true" to disable git pull on every EnsureTemplate
  RM_API_SYSTEMD_BACKEND           "systemd" (default, real Linux) or "supervisord" (Docker dev)
  RM_API_INSTANCE_DB_HOST          DB host written into per-instance configs, default localhost
  RM_API_INSTANCE_DB_PORT          DB port written into per-instance configs, default 3306
  RM_API_MAINTENANCE_BACKEND       "systemd" (default), "supervisord" (Docker dev), or "none" (disable maintenance timers)
  RM_API_S3_REMOTE                 rclone remote name for autobackups3 (e.g. "ljns3"); empty disables backup timer
  RM_API_S3_BUCKET                 S3 bucket name (e.g. "backup-db")
  RM_API_S3_BACKUP_ROOT            backup path prefix; per-instance suffix appended automatically (default "radiusdb")
`, version)
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := buildLogger(cfg.LogFormat)
	logger.Info("starting radius-manager-api",
		slog.String("version", version),
		slog.String("listen", cfg.Listen),
		slog.String("freeradius_dir", cfg.FreeRADIUSDir),
	)

	token, err := loadToken(cfg)
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}

	// Optional DB (nil → manager runs in read-only mode).
	var db *sql.DB
	if cfg.DBDSN != "" {
		db, err = sql.Open("mysql", cfg.DBDSN)
		if err != nil {
			return fmt.Errorf("open db: %w", err)
		}
		db.SetConnMaxLifetime(5 * time.Minute)
		db.SetMaxOpenConns(8)
		defer db.Close()
		if err := db.Ping(); err != nil {
			logger.Warn("DB ping failed at startup; manager will run in read-only mode",
				slog.Any("err", err))
			db = nil
		}
	}

	migrations, err := loadMigrations()
	if err != nil {
		logger.Warn("failed to load embedded schema migrations",
			slog.Any("err", err))
	}

	managerCfg := manager.Config{
		FreeRADIUSDir:    cfg.FreeRADIUSDir,
		StateDir:         cfg.StateDir,
		VPNIP:            cfg.VPNIP,
		CapacityMax:      cfg.CapacityMax,
		APIVersion:       version,
		APIDirBase:       cfg.APIDirBase,
		APIPublishIP:     cfg.APIPublishIP,
		Migrations:       migrations,
		PortRegistryPath: filepath.Join(cfg.FreeRADIUSDir, ".port_registry"),
		InstanceDBHost:   cfg.InstanceDBHost,
		InstanceDBPort:   cfg.InstanceDBPort,
	}
	if db != nil {
		managerCfg.DB = &manager.DBManager{DB: db, AllowRemote: true}
		managerCfg.Ports = manager.NewPortRegistry(managerCfg.PortRegistryPath)
		// Backend selection: production Linux uses real systemctl; the
		// Docker dev image uses supervisord (no systemd in the container).
		switch cfg.SystemdBackend {
		case "supervisord":
			sup := system.NewSupervisordSystemctl()
			managerCfg.Systemctl = sup
			managerCfg.FreeRADIUS = system.NewSupervisordFreeRADIUS(sup)
			logger.Info("systemctl backend = supervisord (Docker dev mode)")
		default:
			sysctl := system.NewRealSystemctl()
			managerCfg.Systemctl = sysctl
			managerCfg.FreeRADIUS = system.NewRealFreeRADIUS(sysctl)
		}
		fs := system.NewRealFilesystem()
		managerCfg.FS = fs

		// Optional v0.2.0 bootstrap. Skipped when RM_API_BOOTSTRAP_REPO empty,
		// preserving v0.1.x behavior where the API directory is provisioned
		// out of band (e.g., by radius-manager.sh).
		if cfg.BootstrapAPIRepo != "" {
			managerCfg.APIBootstrap = &manager.FreeRADIUSAPIBootstrap{
				RepoURL:     cfg.BootstrapAPIRepo,
				TemplateDir: cfg.BootstrapTemplateDir,
				SkipPull:    cfg.BootstrapSkipPull,
				Git:         system.NewRealGit(),
				Python:      system.NewRealPython(),
				FS:          fs,
			}
			logger.Info("freeradius-api bootstrap enabled",
				slog.String("repo", cfg.BootstrapAPIRepo),
				slog.String("template_dir", cfg.BootstrapTemplateDir),
				slog.Bool("skip_pull", cfg.BootstrapSkipPull),
			)
		}

		// v0.3.0 maintenance timers. Independent backend selection from
		// systemd backend so operator can mix-and-match if needed (rare).
		// "none" disables timer setup entirely; CreateInstance then skips
		// the new step exactly like v0.2.0.
		var maintBackend system.Maintenance
		switch cfg.MaintenanceBackend {
		case "none":
			// leave nil
		case "supervisord":
			maintBackend = system.NewSupervisordMaintenance(system.NewSupervisordSystemctl())
		default: // "systemd"
			maintBackend = system.NewRealMaintenance(system.NewRealSystemctl())
		}
		if maintBackend != nil {
			s3 := manager.S3Config{
				Remote:     cfg.S3Remote,
				Bucket:     cfg.S3Bucket,
				BackupRoot: cfg.S3BackupRoot,
			}
			managerCfg.Maintenance = &manager.MaintenanceManager{
				Backend:    maintBackend,
				APIDirBase: cfg.APIDirBase,
				S3:         s3,
			}
			managerCfg.MaintenanceS3 = s3
			logger.Info("maintenance timers enabled",
				slog.String("backend", cfg.MaintenanceBackend),
				slog.String("s3_remote", cfg.S3Remote),
				slog.String("s3_bucket", cfg.S3Bucket),
			)
		}
	}
	mgr := manager.New(managerCfg)

	auditW, err := openAuditLog(cfg.AuditLogPath, logger)
	if err != nil {
		return err
	}
	defer func() {
		if c, ok := auditW.(io.Closer); ok {
			c.Close()
		}
	}()

	auth := &api.StaticTokenAuth{Token: token, Subject: "default"}
	srv := api.NewServer(mgr, auth, logger, api.WithAuditWriter(auditW))

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", slog.Any("err", err))
		}
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("server stopped")
	return nil
}

func runInit() error {
	tok, err := generateToken()
	if err != nil {
		return err
	}
	fmt.Println(tok)
	return nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func loadToken(cfg *config.Config) (string, error) {
	if cfg.Token != "" {
		return strings.TrimSpace(cfg.Token), nil
	}
	if cfg.TokenFile == "" {
		return "", fmt.Errorf("no token configured")
	}
	b, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// loadMigrations reads the //go:embed migrations from internal/schema and
// converts them into manager.Migration values.
func loadMigrations() ([]manager.Migration, error) {
	src, err := schema.All()
	if err != nil {
		return nil, err
	}
	out := make([]manager.Migration, 0, len(src))
	for _, m := range src {
		out = append(out, manager.Migration{Name: m.Name, SQL: m.SQL})
	}
	return out, nil
}

// openAuditLog opens the audit log file in append mode. If the directory
// is not writable (typical on dev boxes that point at /var/log without
// being root), it falls back to discarding records and warns the operator.
func openAuditLog(path string, logger *slog.Logger) (io.Writer, error) {
	if path == "" {
		return io.Discard, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		logger.Warn("audit log directory not writable; auditing disabled",
			slog.String("path", path), slog.Any("err", err))
		return io.Discard, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		logger.Warn("audit log file not writable; auditing disabled",
			slog.String("path", path), slog.Any("err", err))
		return io.Discard, nil
	}
	logger.Info("audit log opened", slog.String("path", path))
	return f, nil
}

func buildLogger(format string) *slog.Logger {
	var h slog.Handler
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
