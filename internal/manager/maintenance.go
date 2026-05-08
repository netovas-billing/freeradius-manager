package manager

import (
	"context"
	"fmt"
	"path"
	"path/filepath"

	"github.com/heirro/freeradius-manager/internal/system"
)

// MaintenanceManager owns the policy of which periodic jobs each
// FreeRADIUS instance gets. It maps the freeradius-api maintenance
// scripts (autoclearzombie.sh, autobackups3.sh) to scheduled jobs via
// the system.Maintenance backend (systemd timers in production,
// supervisord sleep-loops in Docker dev).
//
// Equivalent bash flow lives in radius-manager.sh:setup_api() — the
// difference is that the bash flow writes crontab entries while this
// implementation goes through systemd .timer/.service or supervisord
// program units. See SRS §20 for the trade-off rationale.
type MaintenanceManager struct {
	// Backend is the per-host scheduling backend. Required.
	Backend system.Maintenance

	// APIDirBase is the directory where per-instance freeradius-api
	// directories live (e.g. "/root"). The instance dir is derived
	// as `<APIDirBase>/<name>-api`. Mirrors manager.Config.APIDirBase.
	APIDirBase string

	// S3 contains the autobackups3 destination. Zero-value means
	// autobackups3 is skipped (and any existing job is removed for
	// cleanliness).
	S3 S3Config
}

// jobNames returns (zombieJob, backupJob) for an instance.
func maintenanceJobNames(instance string) (string, string) {
	return instance + "-zombie", instance + "-backup"
}

// MaintenanceCreds carries the per-instance credentials embedded as
// env vars on the scheduled job. Mirrors what bootstrap.PatchScripts
// writes into the scripts on disk; duplicated by design so newer
// freeradius-api revisions can pick the env up while older ones fall
// back to the patched default.
type MaintenanceCreds struct {
	DBHost string
	DBPort int
	DBName string
	DBUser string
	DBPass string
}

// SetupForInstance installs both the autoclearzombie (every 15m) and
// autobackups3 (daily) maintenance jobs for the given instance.
//
// If m.S3 is the zero value, the backup job is skipped AND any
// existing backup job for this instance is removed so the host doesn't
// end up running stale credentials.
func (m *MaintenanceManager) SetupForInstance(ctx context.Context, name string, creds MaintenanceCreds) error {
	if m == nil {
		return fmt.Errorf("MaintenanceManager not configured")
	}
	if m.Backend == nil {
		return fmt.Errorf("MaintenanceManager.Backend is required")
	}
	if err := validateIdentifier(name); err != nil {
		return err
	}

	apiDir := filepath.Join(m.apiDirBase(), name+"-api")
	zombieName, backupName := maintenanceJobNames(name)

	dbHost := creds.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	dbPort := creds.DBPort
	if dbPort == 0 {
		dbPort = 3306
	}
	dbEnv := map[string]string{
		"DB_HOST": dbHost,
		"DB_PORT": fmt.Sprintf("%d", dbPort),
		"DB_USER": creds.DBUser,
		"DB_PASS": creds.DBPass,
		"DB_NAME": creds.DBName,
	}

	// autoclearzombie: every 15 minutes.
	if err := m.Backend.InstallJob(ctx,
		zombieName,
		system.ScheduleEvery15m,
		filepath.Join(apiDir, "autoclearzombie.sh"),
		dbEnv,
	); err != nil {
		return fmt.Errorf("install %s: %w", zombieName, err)
	}

	// autobackups3: daily, only if S3 destination is configured.
	if m.S3.IsZero() {
		// Defensive cleanup: ensure no stale backup job lingers.
		if err := m.Backend.RemoveJob(ctx, backupName); err != nil {
			return fmt.Errorf("remove stale %s: %w", backupName, err)
		}
		return nil
	}

	backupEnv := make(map[string]string, len(dbEnv)+3)
	for k, v := range dbEnv {
		backupEnv[k] = v
	}
	backupEnv["REMOTE"] = m.S3.Remote
	backupEnv["BUCKET"] = m.S3.Bucket
	// BACKUP_PATH composed exactly like bash setup_api(): `${root}/${instance}`.
	backupEnv["BACKUP_PATH"] = path.Join(m.S3.BackupRoot, name)

	if err := m.Backend.InstallJob(ctx,
		backupName,
		system.ScheduleDaily,
		filepath.Join(apiDir, "autobackups3.sh"),
		backupEnv,
	); err != nil {
		return fmt.Errorf("install %s: %w", backupName, err)
	}
	return nil
}

// TeardownForInstance removes both jobs. Idempotent — missing jobs
// are not an error (the system.Maintenance contract guarantees this).
func (m *MaintenanceManager) TeardownForInstance(ctx context.Context, name string) error {
	if m == nil {
		return nil
	}
	if m.Backend == nil {
		return fmt.Errorf("MaintenanceManager.Backend is required")
	}
	if err := validateIdentifier(name); err != nil {
		return err
	}
	zombieName, backupName := maintenanceJobNames(name)
	if err := m.Backend.RemoveJob(ctx, zombieName); err != nil {
		return fmt.Errorf("remove %s: %w", zombieName, err)
	}
	if err := m.Backend.RemoveJob(ctx, backupName); err != nil {
		return fmt.Errorf("remove %s: %w", backupName, err)
	}
	return nil
}

func (m *MaintenanceManager) apiDirBase() string {
	if m.APIDirBase == "" {
		return "/root"
	}
	return m.APIDirBase
}
