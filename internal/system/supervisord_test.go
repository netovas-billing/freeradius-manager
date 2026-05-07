package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSupervisordSystemctl_WriteUnit_TranslatesSystemdUnitToProgram verifies that
// WriteUnit takes a systemd-style unit (the renderAPIServiceUnit output from
// internal/manager) and produces an equivalent supervisord [program:...]
// section. This is the core of option B: unit-of-measure stays the systemd
// unit name, but the on-disk artifact is supervisord conf.
func TestSupervisordSystemctl_WriteUnit_TranslatesSystemdUnitToProgram(t *testing.T) {
	dir := t.TempDir()
	// Use a fake supervisorctl binary that just exits 0 so we don't need
	// a running supervisord during the unit test.
	fakeBin := writeFakeSupervisorctl(t, dir)

	s := &SupervisordSystemctl{
		ConfDir:        filepath.Join(dir, "conf.d"),
		Bin:            fakeBin,
		FreeradiusUser: "root",
	}

	// Sample unit equivalent to manager.renderAPIServiceUnit output.
	unit := `[Unit]
Description=RadiusAPI with Uvicorn - mitra_x
After=network.target

[Service]
User=root
Group=root
WorkingDirectory=/root/mitra_x-api
ExecStart=/root/mitra_x-api/venv/bin/uvicorn main:app --host 0.0.0.0 --port 8100 --workers 4
Restart=always
RestartSec=5
SyslogIdentifier=mitra_x-api

[Install]
WantedBy=multi-user.target
`

	if err := s.WriteUnit(context.Background(), "mitra_x-api.service", unit); err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}

	confPath := filepath.Join(s.ConfDir, "mitra_x-api.conf")
	got, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read produced conf: %v", err)
	}

	gotStr := string(got)
	mustContain := []string{
		"[program:mitra_x-api]",
		"command=/root/mitra_x-api/venv/bin/uvicorn main:app --host 0.0.0.0 --port 8100 --workers 4",
		"directory=/root/mitra_x-api",
		"autorestart=true",
		"user=root",
	}
	for _, want := range mustContain {
		if !strings.Contains(gotStr, want) {
			t.Errorf("expected %q in produced conf, got:\n%s", want, gotStr)
		}
	}
}

func TestSupervisordSystemctl_WriteUnit_RejectsMissingExecStart(t *testing.T) {
	dir := t.TempDir()
	fakeBin := writeFakeSupervisorctl(t, dir)
	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}

	unit := "[Unit]\nDescription=foo\n[Service]\nUser=root\n"
	err := s.WriteUnit(context.Background(), "x.service", unit)
	if err == nil {
		t.Fatal("expected error for unit without ExecStart")
	}
	if !strings.Contains(err.Error(), "ExecStart") {
		t.Fatalf("error should mention ExecStart, got: %v", err)
	}
}

func TestSupervisordSystemctl_RemoveUnit_DeletesConf(t *testing.T) {
	dir := t.TempDir()
	fakeBin := writeFakeSupervisorctl(t, dir)
	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}

	// Place a conf, then remove it.
	if err := os.MkdirAll(s.ConfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	confPath := filepath.Join(s.ConfDir, "x.conf")
	if err := os.WriteFile(confPath, []byte("[program:x]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveUnit(context.Background(), "x.service"); err != nil {
		t.Fatalf("RemoveUnit: %v", err)
	}
	if _, err := os.Stat(confPath); !os.IsNotExist(err) {
		t.Fatalf("expected conf removed, stat err=%v", err)
	}
	// Idempotent: removing again is fine.
	if err := s.RemoveUnit(context.Background(), "x.service"); err != nil {
		t.Fatalf("RemoveUnit (idempotent): %v", err)
	}
}

func TestSupervisordSystemctl_LifecycleCallsSupervisorctl(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	fakeBin := writeFakeSupervisorctlWithLog(t, dir, logPath)

	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}

	ctx := context.Background()
	if err := s.DaemonReload(ctx); err != nil {
		t.Fatalf("DaemonReload: %v", err)
	}
	if err := s.Enable(ctx, "mitra_x-api.service"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := s.Start(ctx, "mitra_x-api.service"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Restart(ctx, "mitra_x-api.service"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := s.Stop(ctx, "mitra_x-api.service"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read calls log: %v", err)
	}
	got := string(logged)
	// reread/update should occur on DaemonReload (so newly written confs
	// are picked up).
	if !strings.Contains(got, "reread") {
		t.Errorf("expected 'reread' invocation, got:\n%s", got)
	}
	if !strings.Contains(got, "update") {
		t.Errorf("expected 'update' invocation, got:\n%s", got)
	}
	if !strings.Contains(got, "start mitra_x-api") {
		t.Errorf("expected 'start mitra_x-api', got:\n%s", got)
	}
	if !strings.Contains(got, "restart mitra_x-api") {
		t.Errorf("expected 'restart mitra_x-api', got:\n%s", got)
	}
	if !strings.Contains(got, "stop mitra_x-api") {
		t.Errorf("expected 'stop mitra_x-api', got:\n%s", got)
	}
}

func TestSupervisordSystemctl_IsActive_ReportsRunning(t *testing.T) {
	dir := t.TempDir()
	fakeBin := writeFakeSupervisorctlWithStatus(t, dir, "mitra_x-api                      RUNNING   pid 1234, uptime 0:00:42\n")
	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}
	active, err := s.IsActive(context.Background(), "mitra_x-api.service")
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !active {
		t.Fatal("expected active=true when supervisorctl status reports RUNNING")
	}
}

func TestSupervisordSystemctl_IsActive_ReportsStopped(t *testing.T) {
	dir := t.TempDir()
	fakeBin := writeFakeSupervisorctlWithStatus(t, dir, "mitra_x-api                      STOPPED   Not started\n")
	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}
	active, err := s.IsActive(context.Background(), "mitra_x-api.service")
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if active {
		t.Fatal("expected active=false when supervisorctl status reports STOPPED")
	}
}

// FreeRADIUS reload via supervisord must restart the freeradius program.
func TestSupervisordFreeRADIUS_ReloadRestartsProgram(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	fakeBin := writeFakeSupervisorctlWithLog(t, dir, logPath)
	s := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     fakeBin,
	}
	fr := &SupervisordFreeRADIUS{
		Systemctl: s,
		Program:   "freeradius",
	}
	if err := fr.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "restart freeradius") {
		t.Errorf("expected 'restart freeradius' in supervisorctl calls, got: %s", logged)
	}
}

// Helpers ---------------------------------------------------------

// writeFakeSupervisorctl writes a no-op shell script that pretends to be
// supervisorctl. Used in tests to satisfy exec.Cmd without a real daemon.
func writeFakeSupervisorctl(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "supervisorctl")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func writeFakeSupervisorctlWithLog(t *testing.T, dir, logPath string) string {
	t.Helper()
	bin := filepath.Join(dir, "supervisorctl")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func writeFakeSupervisorctlWithStatus(t *testing.T, dir, statusOutput string) string {
	t.Helper()
	bin := filepath.Join(dir, "supervisorctl")
	// Echo the canned status line on stdout when invoked.
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(statusOutput, "'", "'\"'\"'") + "'\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}
