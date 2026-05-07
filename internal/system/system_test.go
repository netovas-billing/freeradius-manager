package system

import (
	"context"
	"errors"
	"testing"
)

func TestMockSystemctl_RecordsAndReplays(t *testing.T) {
	m := NewMockSystemctl()
	ctx := context.Background()

	if err := m.WriteUnit(ctx, "x.service", "[Unit]\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, "x.service"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "x.service"); err != nil {
		t.Fatal(err)
	}

	active, err := m.IsActive(ctx, "x.service")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("expected active=true after Start")
	}

	if got := m.UnitContent["x.service"]; got != "[Unit]\n" {
		t.Fatalf("unit content not stored: %q", got)
	}
}

func TestMockSystemctl_FailureInjection(t *testing.T) {
	m := NewMockSystemctl()
	want := errors.New("boom")
	m.Failures["Start"] = want

	if err := m.Start(context.Background(), "x.service"); !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestMockFilesystem_WriteAndRoundTrip(t *testing.T) {
	fs := NewMockFilesystem()
	ctx := context.Background()
	body := []byte("hello world")
	if err := fs.WriteFile(ctx, "/a/b.conf", body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := string(fs.Files["/a/b.conf"]); got != "hello world" {
		t.Fatalf("file body = %q", got)
	}
	if err := fs.RemoveFile(ctx, "/a/b.conf"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.Files["/a/b.conf"]; ok {
		t.Fatal("file should be removed")
	}
}

func TestMockFreeRADIUS_RecordsCalls(t *testing.T) {
	fr := NewMockFreeRADIUS()
	if err := fr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 1 || fr.Calls[0].Method != "Reload" {
		t.Fatalf("calls = %+v", fr.Calls)
	}
}

func TestMockFilesystem_CopyDirAndExists(t *testing.T) {
	fs := NewMockFilesystem()
	ctx := context.Background()
	if err := fs.CopyDir(ctx, "/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	exists, err := fs.Exists(ctx, "/dst")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("dst should exist after CopyDir")
	}
	if fs.Dirs["/dst"] != "/src" {
		t.Fatalf("Dirs lineage = %q", fs.Dirs["/dst"])
	}
}

func TestMockFilesystem_RemoveDir(t *testing.T) {
	fs := NewMockFilesystem()
	ctx := context.Background()
	_ = fs.CopyDir(ctx, "/src", "/dst")
	if err := fs.RemoveDir(ctx, "/dst"); err != nil {
		t.Fatal(err)
	}
	exists, _ := fs.Exists(ctx, "/dst")
	if exists {
		t.Fatal("/dst should not exist after RemoveDir")
	}
}

func TestMockFilesystem_PresetExistsOverride(t *testing.T) {
	// PresetExists lets a test simulate an external mutation (e.g., a
	// path created by some other actor) without going through CopyDir.
	fs := NewMockFilesystem()
	fs.PresetExists["/etc/foo"] = true
	exists, err := fs.Exists(context.Background(), "/etc/foo")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("preset should make path appear to exist")
	}
}

func TestMockGit_RecordsClone(t *testing.T) {
	g := NewMockGit()
	if err := g.Clone(context.Background(), "https://x/y.git", "/tmp/x"); err != nil {
		t.Fatal(err)
	}
	if g.Repos["/tmp/x"] != "https://x/y.git" {
		t.Fatalf("Repos = %v", g.Repos)
	}
	if len(g.Calls) != 1 || g.Calls[0].Method != "Clone" {
		t.Fatalf("calls = %+v", g.Calls)
	}
}

func TestMockGit_FailureInjection(t *testing.T) {
	g := NewMockGit()
	want := errors.New("network unreachable")
	g.Failures["Clone"] = want
	if err := g.Clone(context.Background(), "x", "y"); !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}

func TestMockPython_VenvAndPip(t *testing.T) {
	p := NewMockPython()
	ctx := context.Background()
	if err := p.CreateVenv(ctx, "/v"); err != nil {
		t.Fatal(err)
	}
	if !p.Venvs["/v"] {
		t.Fatal("venv should be recorded")
	}
	if err := p.PipInstall(ctx, "/v", "/r.txt"); err != nil {
		t.Fatal(err)
	}
	if len(p.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(p.Calls))
	}
}
