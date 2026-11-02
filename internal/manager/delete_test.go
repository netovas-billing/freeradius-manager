package manager

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/netovas-billing/freeradius-manager/internal/system"
)

func TestDeleteInstance_FullPath_WithDB(t *testing.T) {
	i, sysctl, fr, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// First create the instance (re-use happy-path setup).
	mock.ExpectExec("USE `mitra_x`").WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectExec("CREATE DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec("CREATE USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("GRANT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW TABLES FROM `mitra_x`").
		WillReturnRows(sqlmock.NewRows([]string{"t"}))
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := i.CreateInstance(context.Background(), createReq("mitra_x"))
	if err != nil {
		t.Fatalf("setup CreateInstance: %v", err)
	}
	_ = resp

	// Snapshot of files / units before delete.
	wantFilesBefore := len(fs.Files)
	if wantFilesBefore == 0 {
		t.Fatal("setup invariant: expected files to be created")
	}

	// Now expect DropDatabase round-trip on delete.
	mock.ExpectExec("USE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DROP DATABASE `mitra_x`").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM mysql\.user WHERE user='mitra_x'`).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectExec("DROP USER 'mitra_x'@'localhost'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("FLUSH PRIVILEGES").WillReturnResult(sqlmock.NewResult(0, 0))

	delResp, err := i.DeleteInstance(context.Background(), "mitra_x", true)
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if delResp.Name != "mitra_x" {
		t.Fatalf("name = %q", delResp.Name)
	}
	if !delResp.DatabaseDropped {
		t.Fatal("expected DatabaseDropped=true")
	}

	// freeradius reloaded.
	if !hasMethod(fr.Calls, "Reload") && !hasMethod(fr.Calls, "Restart") {
		t.Errorf("freeradius reload missing on delete: %+v", fr.Calls)
	}

	// systemd unit stopped + disabled + removed.
	wantUnit := "mitra_x-api.service"
	if !hasMethodArg(sysctl.Calls, "Stop", wantUnit) {
		t.Errorf("Stop %s missing", wantUnit)
	}
	if !hasMethodArg(sysctl.Calls, "Disable", wantUnit) {
		t.Errorf("Disable %s missing", wantUnit)
	}
	if !hasMethodArg(sysctl.Calls, "RemoveUnit", wantUnit) {
		t.Errorf("RemoveUnit %s missing", wantUnit)
	}

	// Filesystem clean.
	if len(fs.Files) != 0 {
		t.Errorf("expected zero files after delete, got: %v", keys(fs.Files))
	}
	if len(fs.Symlinks) != 0 {
		t.Errorf("expected zero symlinks after delete, got: %v", fs.Symlinks)
	}

	// .instance_<name> removed.
	if _, err := i.GetInstance(context.Background(), "mitra_x", false); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("expected ErrInstanceNotFound, got %v", err)
	}

	// Ports released.
	used, _ := i.cfg.Ports.UsedPorts()
	if len(used) != 0 {
		t.Errorf("expected port registry empty, got: %v", used)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock: %v", err)
	}
}

func TestDeleteInstance_WithoutDB_KeepsDatabase(t *testing.T) {
	i, _, _, _, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()

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
	mock.ExpectExec("CREATE TABLE radcheck").WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Delete with withDB=false: NO DROP DATABASE expected.
	delResp, err := i.DeleteInstance(context.Background(), "mitra_x", false)
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if delResp.DatabaseDropped {
		t.Error("expected DatabaseDropped=false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock unexpected: %v", err)
	}
}

func TestDeleteInstance_NotFound_IsIdempotent(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	_, err := i.DeleteInstance(context.Background(), "nonexistent", true)
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestDeleteInstance_InvalidName(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	_, err := i.DeleteInstance(context.Background(), "BAD-NAME", true)
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected invalid_name error, got %v", err)
	}
}

// Delete ikut menghapus <APIDirBase>/<nama>-api — selaras dengan bash delete.
// Kasus yang paling penting: instance Python LAMA (ada venv/). Dulu
// direktorinya tertinggal, dan create ulang dengan nama yang sama ditolak
// permanen oleh penjaga venv di setupInstanceGo.
func TestDeleteInstance_MenghapusDirektoriAPI(t *testing.T) {
	i, _, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	expectCreateDB(mock)
	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	apiDir := filepath.Join(i.cfg.APIDirBase, "mitra_x-api")
	// Tiru sisa instance Python lama di direktori yang sama (direktori itu
	// sendiri + venv/ di dalamnya — keduanya yang dilihat penjaga venv).
	fs.Dirs[apiDir] = "instance-lama"
	fs.Dirs[apiDir+"/venv"] = "python3 -m venv"
	fs.Files[apiDir+"/main.py"] = []byte("app = FastAPI()")

	if _, err := i.DeleteInstance(context.Background(), "mitra_x", false); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if !hasMethodArg(fs.Calls, "RemoveDir", apiDir) {
		t.Fatalf("RemoveDir %s tidak dipanggil; calls=%v", apiDir, fs.Calls)
	}
	for f := range fs.Files {
		if strings.HasPrefix(f, apiDir+"/") {
			t.Errorf("berkas tertinggal di direktori API: %s", f)
		}
	}
	if _, ada := fs.Dirs[apiDir+"/venv"]; ada {
		t.Error("venv/ tertinggal — create ulang nama yang sama akan ditolak penjaga venv")
	}

	// Dan create ulang dengan NAMA YANG SAMA sekarang lolos penjaga venv.
	expectCreateDB(mock)
	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("create ulang sesudah delete ditolak: %v", err)
	}
}

// Penjaga path: rm -rf TIDAK PERNAH boleh mendarat di luar APIDirBase, apa pun
// isi APIDirBase atau namanya.
func TestDirektoriAPIAman(t *testing.T) {
	cases := []struct {
		base, name string
		want       string // "" = harus ditolak
	}{
		{"/root", "mitra_x", "/root/mitra_x-api"},
		{"/root/", "mitra_x", "/root/mitra_x-api"},
		{"/srv/api/../api", "mitra_x", "/srv/api/mitra_x-api"},
		{"/", "mitra_x", ""},           // rm -rf /mitra_x-api di akar: tolak
		{"relatif/dir", "mitra_x", ""}, // tergantung cwd: tolak
		{"/root", "", ""},              // "/root/-api"
		{"/root", "../etc", ""},        // keluar dari base
		{"/root", "a/b", ""},           // subdir
		{"/root", "default", ""},       // nama cadangan
		{"/root", "MITRA", ""},         // gagal validateName
	}
	for _, c := range cases {
		i := &impl{cfg: Config{APIDirBase: c.base}}
		got, err := i.direktoriAPIAman(c.name)
		if c.want == "" {
			if err == nil {
				t.Errorf("base=%q name=%q diterima (%q) — seharusnya ditolak", c.base, c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("base=%q name=%q: got %q, %v; want %q", c.base, c.name, got, err, c.want)
		}
	}
}

// Kalau APIDirBase tidak aman, delete TIDAK memanggil RemoveDir sama sekali,
// tapi langkah lain (unit, config, port, state) tetap berjalan.
func TestDeleteInstance_APIDirBaseTidakAman_TidakMenghapus(t *testing.T) {
	i, sysctl, _, fs, mock, cleanup := newCreateTestImpl(t)
	defer cleanup()
	expectCreateDB(mock)
	if _, err := i.CreateInstance(context.Background(), createReq("mitra_x")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	i.cfg.APIDirBase = "/"
	if _, err := i.DeleteInstance(context.Background(), "mitra_x", false); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if hasMethod(fs.Calls, "RemoveDir") {
		t.Fatalf("RemoveDir dipanggil padahal APIDirBase=/: %v", fs.Calls)
	}
	if !hasMethodArg(sysctl.Calls, "RemoveUnit", "mitra_x-api.service") {
		t.Error("langkah lain delete ikut berhenti")
	}
}

func hasMethod(calls []system.Call, method string) bool {
	for _, c := range calls {
		if c.Method == method {
			return true
		}
	}
	return false
}
