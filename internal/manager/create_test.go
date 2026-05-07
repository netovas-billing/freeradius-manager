package manager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/heirro/freeradius-manager/internal/system"
	"github.com/heirro/freeradius-manager/pkg/types"
)

func newCreateTestImpl(t *testing.T) (*impl, *system.MockSystemctl, *system.MockFreeRADIUS, *system.MockFilesystem, sqlmock.Sqlmock, func()) {
	t.Helper()
	dir := t.TempDir()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}

	dbm := &DBManager{DB: db, AllowRemote: false}
	pr := NewPortRegistry(filepath.Join(dir, ".port_registry"))
	sysctl := system.NewMockSystemctl()
	fr := system.NewMockFreeRADIUS()
	fs := system.NewMockFilesystem()

	cfg := Config{
		FreeRADIUSDir:    dir,
		APIVersion:       "test",
		DB:               dbm,
		Ports:            pr,
		Systemctl:        sysctl,
		FreeRADIUS:       fr,
		FS:               fs,
		Migrations:       []Migration{{Name: "001.sql", SQL: "CREATE TABLE radcheck (id INT)"}},
		APIPublishIP:     "10.254.252.2",
		APIDirBase:       filepath.Join(dir, "api"),
		PortRegistryPath: filepath.Join(dir, ".port_registry"),
	}
	i := &impl{cfg: cfg}
	cleanup := func() { db.Close() }
	return i, sysctl, fr, fs, mock, cleanup
}

func TestCreateInstance_HappyPath(t *testing.T) {
	i, sysctl, fr, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// Expectations for DB layer (in order): CreateDatabase + grants + schema.
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	if resp.Name != "mitra_x" {
		t.Fatalf("name = %q", resp.Name)
	}
	if resp.Ports.Auth < 10000 || resp.Ports.Auth > 59000 {
		t.Fatalf("auth port out of range: %d", resp.Ports.Auth)
	}
	if resp.Ports.Acct != resp.Ports.Auth+1 {
		t.Fatalf("acct port wrong: %d vs %d", resp.Ports.Acct, resp.Ports.Auth+1)
	}
	if resp.Ports.API < 8100 {
		t.Fatalf("api port wrong: %d", resp.Ports.API)
	}
	if resp.Database.Password == "" {
		t.Fatal("DB password should be set")
	}
	if resp.Swagger.Password == "" {
		t.Fatal("Swagger password should be set")
	}

	// FreeRADIUS configs were written.
	mustWritten := []string{
		"/mods-available/sql_mitra_x",
		"/mods-available/eap_mitra_x",
		"/sites-available/inner-tunnel-mitra_x",
		"/sites-available/mitra_x",
	}
	for _, suffix := range mustWritten {
		if !anyKeyHasSuffix(fs.Files, suffix) {
			t.Errorf("expected file ending in %s to be written, got files: %v", suffix, keys(fs.Files))
		}
	}

	// Symlinks were created.
	if len(fs.Symlinks) < 4 {
		t.Errorf("expected ≥4 symlinks (mods + sites enabled), got %d: %v", len(fs.Symlinks), fs.Symlinks)
	}

	// freeradius reload was called once.
	frReloads := 0
	for _, c := range fr.Calls {
		if c.Method == "Reload" || c.Method == "Restart" {
			frReloads++
		}
	}
	if frReloads != 1 {
		t.Errorf("expected exactly 1 freeradius reload, got %d", frReloads)
	}

	// systemd unit for the instance API was written + enabled + started.
	wantUnit := "mitra_x-api.service"
	if _, ok := sysctl.UnitContent[wantUnit]; !ok {
		t.Errorf("expected unit %s to be written", wantUnit)
	}
	if !hasMethodArg(sysctl.Calls, "Enable", wantUnit) {
		t.Errorf("Enable %s missing", wantUnit)
	}
	if !hasMethodArg(sysctl.Calls, "Start", wantUnit) {
		t.Errorf("Start %s missing", wantUnit)
	}

	// .instance_<name> file exists and is readable.
	got, err := i.GetInstance(context.Background(), "mitra_x", true)
	if err != nil {
		t.Fatalf("GetInstance after create: %v", err)
	}
	if got.Database.Password != resp.Database.Password {
		t.Fatalf("DB password mismatch: %q vs %q", got.Database.Password, resp.Database.Password)
	}
}

func TestCreateInstance_AlreadyExists_Returns409(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// Pre-create the .instance_ file.
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x",
		DBName:        "mitra_x",
		DBUser:        "mitra_x",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err == nil || err.Error() == "" {
		t.Fatalf("expected ErrInstanceExists, got %v", err)
	}
	if !strings.Contains(err.Error(), "exists") && err != ErrInstanceExists {
		t.Fatalf("expected ErrInstanceExists, got %v", err)
	}
}

func TestCreateInstance_InvalidName_Returns400(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	for _, bad := range []string{"INVALID", "with-dash", "with space", "default", ""} {
		_, err := i.CreateInstance(context.Background(), createReq(bad))
		if err == nil {
			t.Errorf("expected error for name %q", bad)
		}
	}
}

func TestCreateInstance_DBFailure_Rollback(t *testing.T) {
	i, sysctl, fr, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// Schema import fails midway.
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x`").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnError(sqlmock.ErrCancelled)
	// After rollback, DropDatabase is called.
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DROP DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user.*localhost`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectExec("DROP USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	_, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err == nil {
		t.Fatal("expected error from schema import failure")
	}

	// FreeRADIUS configs MUST NOT exist (rollback).
	if len(fs.Files) != 0 {
		t.Errorf("expected zero files after rollback, got %d: %v", len(fs.Files), keys(fs.Files))
	}
	// systemd unit MUST NOT have been written.
	if len(sysctl.UnitContent) != 0 {
		t.Errorf("expected zero units after rollback, got: %v", sysctl.UnitContent)
	}
	// freeradius reload MUST NOT have been called.
	for _, c := range fr.Calls {
		if c.Method == "Reload" || c.Method == "Restart" {
			t.Errorf("freeradius reload should not have been called on rollback: %+v", fr.Calls)
		}
	}

	// Port registry must be empty after rollback.
	used, _ := i.cfg.Ports.UsedPorts()
	if len(used) != 0 {
		t.Errorf("expected port registry empty after rollback, got: %v", used)
	}

	// .instance_<name> file MUST NOT exist.
	if _, err := i.GetInstance(context.Background(), "mitra_x", false); err != ErrInstanceNotFound {
		t.Errorf("expected ErrInstanceNotFound, got %v", err)
	}
}

// helpers

func createReq(name string) types.CreateInstanceRequest {
	return types.CreateInstanceRequest{Name: name}
}

func anyKeyHasSuffix(m map[string][]byte, suffix string) bool {
	for k := range m {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasMethodArg(calls []system.Call, method, arg string) bool {
	for _, c := range calls {
		if c.Method != method {
			continue
		}
		for _, a := range c.Args {
			if a == arg {
				return true
			}
		}
	}
	return false
}

func TestCreateInstance_WithBootstrap_ClonesAndSetsUpAPIDir(t *testing.T) {
	i, sysctl, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// Wire bootstrap — share Filesystem with create flow so PresetExists
	// for requirements.txt can be observed by SetupInstance.
	mockGit := system.NewMockGit()
	mockPy := system.NewMockPython()
	bootstrap := &FreeRADIUSAPIBootstrap{
		RepoURL:     "https://github.com/heirro/freeradius-api",
		TemplateDir: filepath.Join(t.TempDir(), "fr-api-template"),
		Git:         mockGit,
		Python:      mockPy,
		FS:          fs,
	}
	i.cfg.APIBootstrap = bootstrap
	// Pretend the copy will produce a requirements.txt that pip should pick up.
	apiDir := filepath.Join(i.cfg.APIDirBase, "mitra_x-api")
	fs.PresetExists[apiDir+"/requirements.txt"] = true

	// Same DB expectations as the happy-path test.
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Git Clone happened exactly once.
	cloneSeen := 0
	for _, c := range mockGit.Calls {
		if c.Method == "Clone" {
			cloneSeen++
		}
	}
	if cloneSeen != 1 {
		t.Errorf("expected 1 Clone call, got %d", cloneSeen)
	}

	// CopyDir was called from template into apiDir.
	if fs.Dirs[apiDir] != bootstrap.TemplateDir {
		t.Errorf("CopyDir lineage = %q want %q", fs.Dirs[apiDir], bootstrap.TemplateDir)
	}

	// venv was created.
	venvDir := apiDir + "/venv"
	if !mockPy.Venvs[venvDir] {
		t.Errorf("venv %q not created", venvDir)
	}

	// pip install was invoked because requirements.txt existed.
	pipSeen := false
	for _, c := range mockPy.Calls {
		if c.Method == "PipInstall" {
			pipSeen = true
		}
	}
	if !pipSeen {
		t.Error("expected PipInstall after CreateInstance with bootstrap")
	}

	// .env file written and contains the swagger password the response advertises.
	envBytes, ok := fs.Files[apiDir+"/.env"]
	if !ok {
		t.Fatal(".env not written by bootstrap")
	}
	if !strings.Contains(string(envBytes), "SWAGGER_PASSWORD="+resp.Swagger.Password) {
		t.Errorf(".env should embed swagger password %q\nfile:\n%s",
			resp.Swagger.Password, envBytes)
	}

	// systemd unit still wired up after bootstrap step.
	if _, ok := sysctl.UnitContent["mitra_x-api.service"]; !ok {
		t.Error("systemd unit not written after bootstrap")
	}
}

func TestCreateInstance_BootstrapFailureRollsBack(t *testing.T) {
	i, sysctl, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	mockGit := system.NewMockGit()
	mockPy := system.NewMockPython()
	mockPy.Failures["CreateVenv"] = errors.New("python missing")
	bootstrap := &FreeRADIUSAPIBootstrap{
		RepoURL:     "https://github.com/heirro/freeradius-api",
		TemplateDir: filepath.Join(t.TempDir(), "fr-api-template"),
		Git:         mockGit,
		Python:      mockPy,
		FS:          fs,
	}
	i.cfg.APIBootstrap = bootstrap

	// DB expectations + cleanup expectations (rollback drops DB).
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))
	// Rollback path:
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DROP DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectExec("DROP USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err == nil {
		t.Fatal("expected CreateInstance to fail when bootstrap venv fails")
	}

	// systemd unit must not have been written (we failed before that step).
	if _, ok := sysctl.UnitContent["mitra_x-api.service"]; ok {
		t.Error("systemd unit should not be written when bootstrap fails")
	}
}
