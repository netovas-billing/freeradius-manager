package system

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// newTestRealMaintenance returns a RealMaintenance backed by a
// MockSystemctl with its ledger pointed at a temp file so tests don't
// touch /var/lib/radius-manager-api.
func newTestRealMaintenance(t *testing.T) (*RealMaintenance, *MockSystemctl) {
	t.Helper()
	mock := NewMockSystemctl()
	r := NewRealMaintenance(mock)
	r.MaintenanceStateFile = filepath.Join(t.TempDir(), "maintenance-jobs.json")
	return r, mock
}

func TestRealMaintenance_InstallJob_Every15mTranslatesToOnCalendar(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	err := r.InstallJob(context.Background(), "mitra_x-zombie", ScheduleEvery15m,
		"/usr/local/bin/autoclearzombie.sh", nil)
	if err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	timer := mock.UnitContent["mitra_x-zombie.timer"]
	if !strings.Contains(timer, "OnCalendar=*:0/15") {
		t.Errorf("expected OnCalendar=*:0/15, got:\n%s", timer)
	}
}

func TestRealMaintenance_InstallJob_DailyTranslatesToOnCalendar(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	err := r.InstallJob(context.Background(), "mitra_x-backup", ScheduleDaily,
		"/usr/local/bin/autobackups3.sh", nil)
	if err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	timer := mock.UnitContent["mitra_x-backup.timer"]
	if !strings.Contains(timer, "OnCalendar=daily") {
		t.Errorf("expected OnCalendar=daily, got:\n%s", timer)
	}
}

func TestRealMaintenance_InstallJob_PassesThroughCustomCalendar(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	custom := "Mon *-*-* 03:00:00"
	err := r.InstallJob(context.Background(), "weekly", custom, "/bin/true", nil)
	if err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	timer := mock.UnitContent["weekly.timer"]
	if !strings.Contains(timer, "OnCalendar="+custom) {
		t.Errorf("expected verbatim OnCalendar=%q, got:\n%s", custom, timer)
	}
}

func TestRealMaintenance_InstallJob_WritesBothUnitFiles(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	env := map[string]string{
		"DB_HOST": "127.0.0.1",
		"DB_USER": "radius",
	}
	err := r.InstallJob(context.Background(), "mitra_x-zombie", ScheduleEvery15m,
		"/usr/local/bin/autoclearzombie.sh", env)
	if err != nil {
		t.Fatalf("InstallJob: %v", err)
	}

	// Service unit checks.
	svc, ok := mock.UnitContent["mitra_x-zombie.service"]
	if !ok {
		t.Fatal("expected mitra_x-zombie.service to be written")
	}
	for _, want := range []string{
		"[Service]",
		"Type=oneshot",
		"ExecStart=/usr/local/bin/autoclearzombie.sh",
		"Environment=DB_HOST=127.0.0.1",
		"Environment=DB_USER=radius",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit missing %q. Got:\n%s", want, svc)
		}
	}

	// Timer unit checks.
	tmr, ok := mock.UnitContent["mitra_x-zombie.timer"]
	if !ok {
		t.Fatal("expected mitra_x-zombie.timer to be written")
	}
	for _, want := range []string{
		"[Timer]",
		"Unit=mitra_x-zombie.service",
		"OnCalendar=*:0/15",
		"Persistent=true",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(tmr, want) {
			t.Errorf("timer unit missing %q. Got:\n%s", want, tmr)
		}
	}
}

func TestRealMaintenance_InstallJob_EnvKeysAreSortedDeterministic(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	env := map[string]string{
		"Z_LAST":  "z",
		"A_FIRST": "a",
		"M_MID":   "m",
	}
	if err := r.InstallJob(context.Background(), "j", ScheduleDaily, "/bin/true", env); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	svc := mock.UnitContent["j.service"]
	idxA := strings.Index(svc, "Environment=A_FIRST=a")
	idxM := strings.Index(svc, "Environment=M_MID=m")
	idxZ := strings.Index(svc, "Environment=Z_LAST=z")
	if !(idxA < idxM && idxM < idxZ) || idxA < 0 {
		t.Errorf("expected sorted env order A<M<Z, got idxA=%d idxM=%d idxZ=%d in:\n%s",
			idxA, idxM, idxZ, svc)
	}
}

func TestRealMaintenance_InstallJob_EnableAndStartTimer(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	if err := r.InstallJob(context.Background(), "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	// Walk the recorded calls and assert the lifecycle order:
	// WriteUnit(svc), WriteUnit(timer), DaemonReload, Enable(timer), Start(timer).
	want := []string{"WriteUnit", "WriteUnit", "DaemonReload", "Enable", "Start"}
	if len(mock.Calls) < len(want) {
		t.Fatalf("expected at least %d calls, got %d: %+v", len(want), len(mock.Calls), mock.Calls)
	}
	for i, m := range want {
		if mock.Calls[i].Method != m {
			t.Errorf("call %d: got %s, want %s; calls=%+v", i, mock.Calls[i].Method, m, mock.Calls)
		}
	}
	// Final Enable + Start must target the timer, not the service.
	if mock.Calls[3].Args[0] != "j.timer" {
		t.Errorf("Enable should target timer, got %q", mock.Calls[3].Args[0])
	}
	if mock.Calls[4].Args[0] != "j.timer" {
		t.Errorf("Start should target timer, got %q", mock.Calls[4].Args[0])
	}
}

func TestRealMaintenance_InstallJob_RejectsEmptyName(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	err := r.InstallJob(context.Background(), "", ScheduleDaily, "/bin/true", nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRealMaintenance_InstallJob_RejectsEmptyCommand(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	err := r.InstallJob(context.Background(), "j", ScheduleDaily, "", nil)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestRealMaintenance_InstallJob_PropagatesWriteUnitError(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	want := errors.New("disk full")
	mock.Failures["WriteUnit"] = want
	err := r.InstallJob(context.Background(), "j", ScheduleDaily, "/bin/true", nil)
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
}

func TestRealMaintenance_InstallJob_Idempotent_OverwritesUnits(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	ctx := context.Background()

	if err := r.InstallJob(ctx, "j", ScheduleEvery15m, "/bin/v1", nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first := mock.UnitContent["j.service"]
	if !strings.Contains(first, "ExecStart=/bin/v1") {
		t.Fatalf("first install ExecStart wrong: %s", first)
	}

	// Re-install with new command + schedule.
	if err := r.InstallJob(ctx, "j", ScheduleDaily, "/bin/v2", nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second := mock.UnitContent["j.service"]
	if !strings.Contains(second, "ExecStart=/bin/v2") {
		t.Errorf("re-install should overwrite ExecStart, got: %s", second)
	}
	if strings.Contains(second, "ExecStart=/bin/v1") {
		t.Errorf("old ExecStart should be gone, got: %s", second)
	}
	tmr := mock.UnitContent["j.timer"]
	if !strings.Contains(tmr, "OnCalendar=daily") {
		t.Errorf("re-install should overwrite OnCalendar, got: %s", tmr)
	}

	// Ledger should have just one entry.
	jobs, err := r.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0] != "j" {
		t.Errorf("expected single ledger entry [j], got %v", jobs)
	}
}

func TestRealMaintenance_RemoveJob_StopsDisablesAndRemovesUnits(t *testing.T) {
	r, mock := newTestRealMaintenance(t)
	ctx := context.Background()

	if err := r.InstallJob(ctx, "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	// Reset Calls so we can assert exclusively on removal.
	mock.Calls = nil

	if err := r.RemoveJob(ctx, "j"); err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}

	// Required calls in order: Stop, Disable, RemoveUnit(timer),
	// RemoveUnit(service), DaemonReload.
	want := []Call{
		{Method: "Stop", Args: []string{"j.timer"}},
		{Method: "Disable", Args: []string{"j.timer"}},
		{Method: "RemoveUnit", Args: []string{"j.timer"}},
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

	// Ledger emptied.
	jobs, _ := r.ListJobs(ctx)
	if len(jobs) != 0 {
		t.Errorf("expected empty ledger after RemoveJob, got %v", jobs)
	}
}

func TestRealMaintenance_RemoveJob_IdempotentForUnknownName(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	if err := r.RemoveJob(context.Background(), "never-installed"); err != nil {
		t.Errorf("RemoveJob of unknown name should be idempotent, got: %v", err)
	}
}

func TestRealMaintenance_RemoveJob_RejectsEmptyName(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	err := r.RemoveJob(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRealMaintenance_RemoveJob_StopFailureIsTolerated(t *testing.T) {
	// A timer that's not yet running may make Stop fail. The contract
	// says removal must still succeed (idempotency).
	r, mock := newTestRealMaintenance(t)
	ctx := context.Background()

	if err := r.InstallJob(ctx, "j", ScheduleDaily, "/bin/true", nil); err != nil {
		t.Fatalf("InstallJob: %v", err)
	}
	mock.Failures["Stop"] = errors.New("not active")

	if err := r.RemoveJob(ctx, "j"); err != nil {
		t.Errorf("RemoveJob should swallow Stop errors, got: %v", err)
	}
}

func TestRealMaintenance_ListJobs_ReturnsSortedNames(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	ctx := context.Background()
	for _, n := range []string{"zeta", "alpha", "mike"} {
		if err := r.InstallJob(ctx, n, ScheduleDaily, "/bin/true", nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "mike", "zeta"}
	if !sort.StringsAreSorted(got) {
		t.Errorf("ListJobs should return sorted, got %v", got)
	}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Errorf("ListJobs got %v, want %v", got, want)
	}
}

func TestRealMaintenance_ListJobs_EmptyLedger(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	got, err := r.ListJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list for fresh store, got %v", got)
	}
}

func TestRealMaintenance_ConcurrentInstall_NoRace(t *testing.T) {
	r, _ := newTestRealMaintenance(t)
	ctx := context.Background()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("job-%d", i)
			if err := r.InstallJob(ctx, name, ScheduleEvery15m, "/bin/true", nil); err != nil {
				t.Errorf("InstallJob(%s): %v", name, err)
			}
		}()
	}
	wg.Wait()

	jobs, err := r.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != workers {
		t.Errorf("expected %d ledger entries, got %d: %v", workers, len(jobs), jobs)
	}
}

// equalArgs is a tiny helper that compares two []string for value
// equality (deep equality on slices). Defined here because the
// existing test files don't expose one.
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
