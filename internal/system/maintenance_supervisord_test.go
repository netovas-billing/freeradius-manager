package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// newTestSupervisordMaintenance returns a SupervisordMaintenance
// backed by a MockSystemctl. Note that we use MockSystemctl (not the
// real *SupervisordSystemctl) here because the contract under test is
// "what does SupervisordMaintenance hand to its Systemctl seam?" —
// actually exercising the supervisord parser belongs in
// supervisord_test.go.
func newTestSupervisordMaintenance(t *testing.T) (*SupervisordMaintenance, *MockSystemctl) {
	t.Helper()
	mock := NewMockSystemctl()
	m := NewSupervisordMaintenance(mock)
	m.MaintenanceStateFile = filepath.Join(t.TempDir(), "maintenance-jobs.json")
	return m, mock
}

func TestSupervisordMaintenance_InstallJob_Every15mEncodesSleep900(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	if err := m.InstallJob(context.Background(), "j", ScheduleEvery15m, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	body := mock.UnitContent["j.service"]
	if !strings.Contains(body, "sleep 900") {
		t.Errorf("expected 'sleep 900' for every 15m, got:\n%s", body)
	}
}

func TestSupervisordMaintenance_InstallJob_DailyEncodesSleep86400(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	if err := m.InstallJob(context.Background(), "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	body := mock.UnitContent["j.service"]
	if !strings.Contains(body, "sleep 86400") {
		t.Errorf("expected 'sleep 86400' for daily, got:\n%s", body)
	}
}

func TestSupervisordMaintenance_InstallJob_RejectsUnknownSchedule(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	err := m.InstallJob(context.Background(), "j", "Mon *-*-* 03:00:00", "/bin/true", nil)
	if err == nil {
		t.Fatal("expected error for unsupported schedule on supervisord backend")
	}
	if !strings.Contains(err.Error(), "unsupported schedule") {
		t.Errorf("expected error to mention 'unsupported schedule', got: %v", err)
	}
}

// TestSupervisordMaintenance_InstallJob_TranslatedThroughSupervisordSystemctl
// verifies that what SupervisordMaintenance writes is a unit that the
// real *SupervisordSystemctl (which parses systemd) can translate into
// a working [program:<name>] block. This is the integration seam we
// rely on instead of duplicating supervisord-conf rendering logic in
// SupervisordMaintenance.
func TestSupervisordMaintenance_InstallJob_TranslatedThroughSupervisordSystemctl(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "supervisorctl")
	// Fake supervisorctl that just exits 0 so DaemonReload/Start succeed.
	writeFakeSupervisorctlScript(t, bin)

	sup := &SupervisordSystemctl{
		ConfDir: filepath.Join(dir, "conf.d"),
		Bin:     bin,
	}
	m := NewSupervisordMaintenance(sup)
	m.MaintenanceStateFile = filepath.Join(dir, "ledger.json")

	env := map[string]string{
		"DB_HOST": "127.0.0.1",
		"DB_USER": "radius",
	}
	err := m.InstallJob(context.Background(), "mitra_x-zombie",
		ScheduleEvery15m, "/usr/local/bin/autoclearzombie.sh", env)
	if err != nil {
		t.Fatalf("InstallJob: %v", err)
	}

	confPath := filepath.Join(sup.ConfDir, "mitra_x-zombie.conf")
	got := mustReadFile(t, confPath)
	for _, want := range []string{
		"[program:mitra_x-zombie]",
		"autorestart=true",
		"autostart=true",
		"sleep 900",
		"DB_HOST='127.0.0.1'",
		"DB_USER='radius'",
		"/usr/local/bin/autoclearzombie.sh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("supervisord conf missing %q. Full conf:\n%s", want, got)
		}
	}
}

func TestSupervisordMaintenance_InstallJob_PassesScheduleStringToWriteUnit(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	if err := m.InstallJob(context.Background(), "j", ScheduleEvery15m,
		"/usr/local/bin/script.sh", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	unit, ok := mock.UnitContent["j.service"]
	if !ok {
		t.Fatal("expected j.service unit content captured")
	}
	// Must be a [Service] systemd-style unit (so SupervisordSystemctl
	// can parse it). The bash wrapper command + sleep loop must show up.
	for _, want := range []string{
		"[Service]",
		"ExecStart=bash -c",
		"while true; do",
		"/usr/local/bin/script.sh",
		"sleep 900",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("captured unit missing %q. Got:\n%s", want, unit)
		}
	}
}

func TestSupervisordMaintenance_InstallJob_RejectsEmptyName(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	if err := m.InstallJob(context.Background(), "", ScheduleDaily, "/bin/true", nil); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestSupervisordMaintenance_InstallJob_RejectsEmptyCommand(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	if err := m.InstallJob(context.Background(), "j", ScheduleDaily, "", nil); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestSupervisordMaintenance_InstallJob_PropagatesWriteUnitError(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	want := errors.New("write failed")
	mock.Failures["WriteUnit"] = want
	err := m.InstallJob(context.Background(), "j", ScheduleDaily, "/bin/true", nil)
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
}

func TestSupervisordMaintenance_InstallJob_Idempotent_OverwritesProgram(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	ctx := context.Background()

	if err := m.InstallJob(ctx, "j", ScheduleEvery15m, "/bin/v1", nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !strings.Contains(mock.UnitContent["j.service"], "/bin/v1") {
		t.Fatalf("first install missing /bin/v1: %s", mock.UnitContent["j.service"])
	}

	if err := m.InstallJob(ctx, "j", ScheduleDaily, "/bin/v2", nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second := mock.UnitContent["j.service"]
	if !strings.Contains(second, "/bin/v2") {
		t.Errorf("re-install should overwrite, got: %s", second)
	}
	if strings.Contains(second, "/bin/v1") {
		t.Errorf("old command should be gone, got: %s", second)
	}
	if !strings.Contains(second, "sleep 86400") {
		t.Errorf("re-install should switch to daily sleep, got: %s", second)
	}

	jobs, _ := m.ListJobs(ctx)
	if len(jobs) != 1 || jobs[0] != "j" {
		t.Errorf("expected single ledger entry [j], got %v", jobs)
	}
}

func TestSupervisordMaintenance_RemoveJob_StopsAndRemovesUnit(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	ctx := context.Background()

	if err := m.InstallJob(ctx, "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	mock.Calls = nil

	if err := m.RemoveJob(ctx, "j"); err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}

	want := []Call{
		{Method: "Stop", Args: []string{"j.service"}},
		{Method: "RemoveUnit", Args: []string{"j.service"}},
		{Method: "DaemonReload"},
	}
	if len(mock.Calls) != len(want) {
		t.Fatalf("expected %d calls, got %d: %+v", len(want), len(mock.Calls), mock.Calls)
	}
	for i, w := range want {
		if mock.Calls[i].Method != w.Method {
			t.Errorf("call %d method: got %s, want %s", i, mock.Calls[i].Method, w.Method)
		}
		if !equalArgs(mock.Calls[i].Args, w.Args) {
			t.Errorf("call %d args: got %v, want %v", i, mock.Calls[i].Args, w.Args)
		}
	}

	jobs, _ := m.ListJobs(ctx)
	if len(jobs) != 0 {
		t.Errorf("expected empty ledger after RemoveJob, got %v", jobs)
	}
}

func TestSupervisordMaintenance_RemoveJob_IdempotentForUnknownName(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	if err := m.RemoveJob(context.Background(), "never-installed"); err != nil {
		t.Errorf("RemoveJob of unknown name should be idempotent, got: %v", err)
	}
}

func TestSupervisordMaintenance_RemoveJob_RejectsEmptyName(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	if err := m.RemoveJob(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestSupervisordMaintenance_RemoveJob_StopFailureIsTolerated(t *testing.T) {
	m, mock := newTestSupervisordMaintenance(t)
	ctx := context.Background()
	if err := m.InstallJob(ctx, "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	mock.Failures["Stop"] = errors.New("not running")

	if err := m.RemoveJob(ctx, "j"); err != nil {
		t.Errorf("RemoveJob should swallow Stop errors, got: %v", err)
	}
}

func TestSupervisordMaintenance_ListJobs_ReturnsSortedNames(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	ctx := context.Background()
	for _, n := range []string{"zeta", "alpha", "mike"} {
		if err := m.InstallJob(ctx, n, ScheduleDaily, "/bin/true", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("ListJobs should return sorted, got %v", got)
	}
	if len(got) != 3 {
		t.Errorf("ListJobs got %v, want 3 entries", got)
	}
}

func TestSupervisordMaintenance_ListJobs_EmptyLedger(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	got, err := m.ListJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list for fresh store, got %v", got)
	}
}

func TestSupervisordMaintenance_ConcurrentInstall_NoRace(t *testing.T) {
	m, _ := newTestSupervisordMaintenance(t)
	ctx := context.Background()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("job-%d", i)
			if err := m.InstallJob(ctx, name, ScheduleEvery15m, "/bin/true", nil); err != nil {
				t.Errorf("InstallJob(%s): %v", name, err)
			}
		}()
	}
	wg.Wait()

	jobs, err := m.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != workers {
		t.Errorf("expected %d ledger entries, got %d: %v", workers, len(jobs), jobs)
	}
}

// Helpers ---------------------------------------------------------

// writeFakeSupervisorctlScript writes a no-op shell stub at the given
// absolute path. Used by the integration test that runs through real
// SupervisordSystemctl (and therefore really exec's supervisorctl).
func writeFakeSupervisorctlScript(t *testing.T, path string) {
	t.Helper()
	const script = "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
