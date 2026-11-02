package manager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/netovas-billing/freeradius-manager/internal/system"
	"github.com/netovas-billing/freeradius-manager/pkg/types"
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
	// Bootstrap runtime Go WAJIB untuk create (tanpa itu create ditolak), jadi
	// fixture memasangnya dengan Filesystem yang SAMA dengan alur create —
	// pemeriksaan skrip pemeliharaan di create membaca hasil salinan bootstrap.
	cfg.APIBootstrap = &FreeRADIUSAPIBootstrap{
		GoRepoURL:     "https://github.com/netovas-billing/freeradius-api",
		GoTemplateDir: filepath.Join(dir, "fr-api-go-template"),
		Git:           system.NewMockGit(),
		Go:            system.NewMockGo(),
		FS:            fs,
	}
	for _, skrip := range skripPemeliharaan {
		fs.PresetExists[filepath.Join(cfg.APIBootstrap.GoTemplateDir, skrip)] = true
	}
	i := &impl{cfg: cfg}
	cleanup := func() { db.Close() }
	return i, sysctl, fr, fs, mock, cleanup
}

// expectCreateDB memasang ekspektasi sqlmock untuk create DB + user + skema
// mitra_x yang berhasil.
func expectCreateDB(mock sqlmock.Sqlmock) {
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))
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
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
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

	bootstrap := i.cfg.APIBootstrap
	mockGit := bootstrap.Git.(*system.MockGit)
	mockGo := bootstrap.Go.(*system.MockGo)
	apiDir := filepath.Join(i.cfg.APIDirBase, "mitra_x-api")

	expectCreateDB(mock)

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

	// Biner dibangun di template lalu disalin ke direktori instance.
	if len(mockGo.Built) != 1 {
		t.Errorf("expected 1 go build, got %v", mockGo.Built)
	}
	if _, ok := fs.Files[apiDir+"/freeradius-api"]; !ok {
		t.Errorf("biner tidak disalin ke %s; files=%v", apiDir, keys(fs.Files))
	}

	// .env file written and contains the password the response advertises.
	envBytes, ok := fs.Files[apiDir+"/.env"]
	if !ok {
		t.Fatal(".env not written by bootstrap")
	}
	if !strings.Contains(string(envBytes), "BASIC_AUTH_PASSWORD="+resp.Swagger.Password) {
		t.Errorf(".env should embed password %q\nfile:\n%s",
			resp.Swagger.Password, envBytes)
	}

	// systemd unit still wired up after bootstrap step.
	if _, ok := sysctl.UnitContent["mitra_x-api.service"]; !ok {
		t.Error("systemd unit not written after bootstrap")
	}
}

// Unit yang ditulis create TIDAK PERNAH boleh menyebut venv/uvicorn: runtime
// Python sudah dibuang, dan unit seperti itu menunjuk berkas yang tak pernah
// dibuat — Start systemd tidak menunggu proses sehat, jadi create melapor
// sukses untuk instance yang API-nya mati.
func TestCreateInstance_UnitTakPernahMenyebutVenvAtauUvicorn(t *testing.T) {
	i, sysctl, _, _, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	expectCreateDB(mock)

	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	unit, ok := sysctl.UnitContent["mitra_x-api.service"]
	if !ok {
		t.Fatal("unit tidak ditulis")
	}
	for _, terlarang := range []string{"venv", "uvicorn", "main:app"} {
		if strings.Contains(unit, terlarang) {
			t.Fatalf("unit menyebut %q:\n%s", terlarang, unit)
		}
	}
	apiDir := filepath.Join(i.cfg.APIDirBase, "mitra_x-api")
	if !strings.Contains(unit, "ExecStart="+apiDir+"/freeradius-api") {
		t.Fatalf("unit tidak menunjuk biner Go:\n%s", unit)
	}
}

// Tanpa bootstrap, create DITOLAK sebelum apa pun disentuh: tak ada port
// teralokasi, tak ada DB (sqlmock tanpa ekspektasi — panggilan apa pun gagal),
// tak ada config FreeRADIUS, tak ada unit.
func TestCreateInstance_TanpaBootstrapDitolak(t *testing.T) {
	i, sysctl, fr, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	i.cfg.APIBootstrap = nil

	_, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if !errors.Is(err, ErrBootstrapTidakAktif) {
		t.Fatalf("expected ErrBootstrapTidakAktif, got %v", err)
	}
	if !strings.Contains(err.Error(), "RM_API_GO_REPO") {
		t.Fatalf("pesan galat tidak menyebut cara memperbaikinya: %v", err)
	}
	if len(sysctl.UnitContent) != 0 || len(sysctl.Calls) != 0 {
		t.Errorf("systemd disentuh: units=%v calls=%v", sysctl.UnitContent, sysctl.Calls)
	}
	if len(fs.Files) != 0 || len(fs.Symlinks) != 0 {
		t.Errorf("berkas tertulis: %v %v", keys(fs.Files), fs.Symlinks)
	}
	if len(fr.Calls) != 0 {
		t.Errorf("freeradius disentuh: %v", fr.Calls)
	}
	if used, _ := i.cfg.Ports.UsedPorts(); len(used) != 0 {
		t.Errorf("port teralokasi: %v", used)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock: %v", err)
	}
}

func TestCreateInstance_WithMaintenance_InstallsTimers(t *testing.T) {
	i, _, _, _, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	maint := system.NewMockMaintenance()
	i.cfg.Maintenance = &MaintenanceManager{
		Backend:    maint,
		APIDirBase: i.cfg.APIDirBase,
		S3:         S3Config{Remote: "ljns3", Bucket: "backup-db", BackupRoot: "radiusdb"},
	}
	i.cfg.MaintenanceS3 = i.cfg.Maintenance.S3

	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x` LIKE 'radcheck'").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	if _, ok := maint.Jobs["mitra_x-zombie"]; !ok {
		t.Errorf("mitra_x-zombie job not installed; jobs=%v", maint.Jobs)
	}
	if _, ok := maint.Jobs["mitra_x-backup"]; !ok {
		t.Errorf("mitra_x-backup job not installed; jobs=%v", maint.Jobs)
	}

	// Maintenance teardown should run on subsequent DeleteInstance.
	// Verify with DeleteInstance smoke (no DB drop to keep mock simple).
	if _, err := i.DeleteInstance(context.Background(), "mitra_x", false); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if len(maint.Jobs) != 0 {
		t.Errorf("expected jobs cleared after DeleteInstance, got: %v", maint.Jobs)
	}
}

func TestCreateInstance_MaintenanceFailure_RollsBack(t *testing.T) {
	i, sysctl, _, _, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	maint := system.NewMockMaintenance()
	maint.Failures["InstallJob"] = errors.New("systemd dead")
	i.cfg.Maintenance = &MaintenanceManager{Backend: maint, APIDirBase: i.cfg.APIDirBase}

	// DB expectations + cleanup expectations on rollback.
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
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
		t.Fatal("expected CreateInstance to fail when maintenance install fails")
	}
	// systemd unit should not have been written: maintenance step is BEFORE
	// the systemd unit step in the new ordering.
	if _, ok := sysctl.UnitContent["mitra_x-api.service"]; ok {
		t.Error("systemd unit should not be written when maintenance fails")
	}
}

func TestCreateInstance_BootstrapFailureRollsBack(t *testing.T) {
	i, sysctl, _, _, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	i.cfg.APIBootstrap.Go.(*system.MockGo).Failures["Build"] = errors.New("go build gagal")

	// DB expectations + cleanup expectations (rollback drops DB).
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x' AND host='localhost'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
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
		t.Fatal("expected CreateInstance to fail when bootstrap build fails")
	}

	// systemd unit must not have been written (we failed before that step).
	if _, ok := sysctl.UnitContent["mitra_x-api.service"]; ok {
		t.Error("systemd unit should not be written when bootstrap fails")
	}
}
