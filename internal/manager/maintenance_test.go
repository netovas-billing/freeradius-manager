package manager

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/heirro/freeradius-manager/internal/system"
)

func newTestMaintenance(s3 S3Config) (*MaintenanceManager, *system.MockMaintenance) {
	mock := system.NewMockMaintenance()
	return &MaintenanceManager{
		Backend:    mock,
		APIDirBase: "/root",
		S3:         s3,
	}, mock
}

func defaultCreds() MaintenanceCreds {
	return MaintenanceCreds{
		DBHost: "10.254.252.2",
		DBPort: 3306,
		DBName: "mitra_x",
		DBUser: "mitra_x",
		DBPass: "Sup3rSecret!",
	}
}

func TestMaintenance_SetupForInstance_InstallsBothJobsWithS3(t *testing.T) {
	mm, mock := newTestMaintenance(S3Config{
		Remote: "ljns3", Bucket: "backup-db", BackupRoot: "radiusdb",
	})
	if err := mm.SetupForInstance(context.Background(), "mitra_x", defaultCreds()); err != nil {
		t.Fatal(err)
	}

	zombie, ok := mock.Jobs["mitra_x-zombie"]
	if !ok {
		t.Fatal("expected mitra_x-zombie job installed")
	}
	if zombie.Schedule != system.ScheduleEvery15m {
		t.Errorf("zombie schedule = %q want %q", zombie.Schedule, system.ScheduleEvery15m)
	}
	if zombie.Command != "/root/mitra_x-api/autoclearzombie.sh" {
		t.Errorf("zombie command = %q", zombie.Command)
	}
	wantZombieEnv := map[string]string{
		"DB_HOST": "10.254.252.2",
		"DB_PORT": "3306",
		"DB_USER": "mitra_x",
		"DB_PASS": "Sup3rSecret!",
		"DB_NAME": "mitra_x",
	}
	if !envEqual(zombie.Env, wantZombieEnv) {
		t.Errorf("zombie env = %v\nwant %v", zombie.Env, wantZombieEnv)
	}

	backup, ok := mock.Jobs["mitra_x-backup"]
	if !ok {
		t.Fatal("expected mitra_x-backup job installed")
	}
	if backup.Schedule != system.ScheduleDaily {
		t.Errorf("backup schedule = %q want %q", backup.Schedule, system.ScheduleDaily)
	}
	if backup.Command != "/root/mitra_x-api/autobackups3.sh" {
		t.Errorf("backup command = %q", backup.Command)
	}
	wantBackupEnv := map[string]string{
		"DB_HOST":     "10.254.252.2",
		"DB_PORT":     "3306",
		"DB_USER":     "mitra_x",
		"DB_PASS":     "Sup3rSecret!",
		"DB_NAME":     "mitra_x",
		"REMOTE":      "ljns3",
		"BUCKET":      "backup-db",
		"BACKUP_PATH": "radiusdb/mitra_x",
	}
	if !envEqual(backup.Env, wantBackupEnv) {
		t.Errorf("backup env = %v\nwant %v", backup.Env, wantBackupEnv)
	}
}

func TestMaintenance_SetupForInstance_SkipsBackupWhenS3Empty(t *testing.T) {
	mm, mock := newTestMaintenance(S3Config{})
	if err := mm.SetupForInstance(context.Background(), "mitra_x", defaultCreds()); err != nil {
		t.Fatal(err)
	}
	if _, ok := mock.Jobs["mitra_x-zombie"]; !ok {
		t.Error("zombie job missing")
	}
	if _, ok := mock.Jobs["mitra_x-backup"]; ok {
		t.Error("backup job should NOT be installed when S3 is zero")
	}
	// And RemoveJob was called for the backup name (defensive cleanup).
	removeSeen := false
	for _, c := range mock.Calls {
		if c.Method == "RemoveJob" && len(c.Args) > 0 && c.Args[0] == "mitra_x-backup" {
			removeSeen = true
		}
	}
	if !removeSeen {
		t.Error("expected RemoveJob mitra_x-backup as defensive cleanup")
	}
}

func TestMaintenance_SetupForInstance_ValidatesName(t *testing.T) {
	mm, _ := newTestMaintenance(S3Config{})
	for _, bad := range []string{"BAD-name", "", "with space", "back`tick"} {
		if err := mm.SetupForInstance(context.Background(), bad, defaultCreds()); err == nil {
			t.Errorf("expected error for name %q", bad)
		}
	}
}

func TestMaintenance_SetupForInstance_PropagatesBackendError(t *testing.T) {
	mm, mock := newTestMaintenance(S3Config{Remote: "ljns3", Bucket: "b", BackupRoot: "r"})
	want := errors.New("systemd dead")
	mock.Failures["InstallJob"] = want
	err := mm.SetupForInstance(context.Background(), "mitra_x", defaultCreds())
	if !errors.Is(err, want) {
		t.Fatalf("got %v want wrapping %v", err, want)
	}
}

func TestMaintenance_TeardownForInstance_RemovesBoth(t *testing.T) {
	mm, mock := newTestMaintenance(S3Config{
		Remote: "ljns3", Bucket: "backup-db", BackupRoot: "radiusdb",
	})
	// Pre-populate by Setup.
	if err := mm.SetupForInstance(context.Background(), "mitra_x", defaultCreds()); err != nil {
		t.Fatal(err)
	}
	if err := mm.TeardownForInstance(context.Background(), "mitra_x"); err != nil {
		t.Fatal(err)
	}
	if len(mock.Jobs) != 0 {
		t.Errorf("expected no jobs after teardown, got: %v", mock.Jobs)
	}
}

func TestMaintenance_TeardownForInstance_IdempotentOnMissing(t *testing.T) {
	mm, _ := newTestMaintenance(S3Config{})
	if err := mm.TeardownForInstance(context.Background(), "never_created"); err != nil {
		t.Errorf("teardown should be idempotent, got %v", err)
	}
}

func TestMaintenance_DBHostDefaultsWhenEmpty(t *testing.T) {
	mm, mock := newTestMaintenance(S3Config{})
	creds := MaintenanceCreds{DBName: "x", DBUser: "x", DBPass: "x"} // host empty, port 0
	if err := mm.SetupForInstance(context.Background(), "x", creds); err != nil {
		t.Fatal(err)
	}
	job := mock.Jobs["x-zombie"]
	if job.Env["DB_HOST"] != "localhost" {
		t.Errorf("DB_HOST default = %q want localhost", job.Env["DB_HOST"])
	}
	if job.Env["DB_PORT"] != "3306" {
		t.Errorf("DB_PORT default = %q want 3306", job.Env["DB_PORT"])
	}
}

func TestMaintenance_NilManagerSafeTeardown(t *testing.T) {
	var mm *MaintenanceManager
	if err := mm.TeardownForInstance(context.Background(), "x"); err != nil {
		t.Errorf("nil receiver Teardown should be a no-op, got %v", err)
	}
}

// envEqual compares two string maps regardless of key order.
func envEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}
