package manager

import (
	"context"
	"errors"
	"testing"
)

func TestStartInstance_DelegatesToSystemctl(t *testing.T) {
	i, sysctl, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	// Pre-populate state file (lifecycle ops require instance to exist).
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x", DBName: "mitra_x", DBUser: "mitra_x",
	}); err != nil {
		t.Fatal(err)
	}

	if err := i.StartInstance(context.Background(), "mitra_x"); err != nil {
		t.Fatalf("StartInstance: %v", err)
	}

	if !hasMethodArg(sysctl.Calls, "Start", "mitra_x-api.service") {
		t.Errorf("expected Start mitra_x-api.service, got: %+v", sysctl.Calls)
	}
}

func TestStopInstance_DelegatesToSystemctl(t *testing.T) {
	i, sysctl, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x", DBName: "mitra_x", DBUser: "mitra_x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := i.StopInstance(context.Background(), "mitra_x"); err != nil {
		t.Fatal(err)
	}
	if !hasMethodArg(sysctl.Calls, "Stop", "mitra_x-api.service") {
		t.Errorf("Stop call missing")
	}
}

func TestRestartInstance_DelegatesToSystemctl(t *testing.T) {
	i, sysctl, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x", DBName: "mitra_x", DBUser: "mitra_x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := i.RestartInstance(context.Background(), "mitra_x"); err != nil {
		t.Fatal(err)
	}
	if !hasMethodArg(sysctl.Calls, "Restart", "mitra_x-api.service") {
		t.Errorf("Restart call missing")
	}
}

func TestStartInstance_NotFound(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()

	err := i.StartInstance(context.Background(), "nonexistent")
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestStartInstance_PropagatesSystemctlError(t *testing.T) {
	i, sysctl, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x", DBName: "mitra_x", DBUser: "mitra_x",
	}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("systemctl boom")
	sysctl.Failures["Start"] = want

	err := i.StartInstance(context.Background(), "mitra_x")
	if !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestTestInstance_ReturnsPortStatus(t *testing.T) {
	i, _, _, _, _, cleanup := newCreateTestImpl(t)
	defer cleanup()
	if err := i.writeInstanceFile("mitra_x", &instanceFile{
		AdminUsername: "mitra_x", DBName: "mitra_x", DBUser: "mitra_x",
		AuthPort: 11000, AcctPort: 11001, CoAPort: 13000, APIPort: 8101,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := i.TestInstance(context.Background(), "mitra_x")
	if err != nil {
		t.Fatalf("TestInstance: %v", err)
	}
	if res.Name != "mitra_x" {
		t.Fatalf("name = %q", res.Name)
	}
	// Each declared port should produce a check entry.
	for _, k := range []string{"auth", "acct", "coa", "api"} {
		if _, ok := res.PortChecks[k]; !ok {
			t.Errorf("missing port check %q", k)
		}
	}
}
