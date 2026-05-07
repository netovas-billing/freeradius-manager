package manager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heirro/freeradius-manager/internal/system"
)

func newTestBootstrap() (*FreeRADIUSAPIBootstrap, *system.MockGit, *system.MockPython, *system.MockFilesystem) {
	g := system.NewMockGit()
	py := system.NewMockPython()
	fs := system.NewMockFilesystem()
	b := &FreeRADIUSAPIBootstrap{
		RepoURL:     "https://github.com/heirro/freeradius-api",
		TemplateDir: "/var/lib/radius-manager-api/freeradius-api-template",
		Git:         g,
		Python:      py,
		FS:          fs,
	}
	return b, g, py, fs
}

func TestEnsureTemplate_FreshClone(t *testing.T) {
	b, g, _, _ := newTestBootstrap()
	if err := b.EnsureTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.Repos[b.TemplateDir] != b.RepoURL {
		t.Fatalf("expected clone of %s into %s, got %v", b.RepoURL, b.TemplateDir, g.Repos)
	}
}

func TestEnsureTemplate_AlreadyExists_PullsByDefault(t *testing.T) {
	b, g, _, fs := newTestBootstrap()
	fs.PresetExists[b.TemplateDir] = true

	if err := b.EnsureTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Clone should not happen.
	if _, cloned := g.Repos[b.TemplateDir]; cloned {
		t.Fatal("should not clone when template dir already exists")
	}
	// Pull should happen.
	pullSeen := false
	for _, c := range g.Calls {
		if c.Method == "Pull" {
			pullSeen = true
		}
	}
	if !pullSeen {
		t.Fatal("expected Pull call when template exists")
	}
}

func TestEnsureTemplate_SkipPullHonored(t *testing.T) {
	b, g, _, fs := newTestBootstrap()
	fs.PresetExists[b.TemplateDir] = true
	b.SkipPull = true

	if err := b.EnsureTemplate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Calls {
		if c.Method == "Pull" {
			t.Fatal("Pull should be skipped when SkipPull=true")
		}
	}
}

func TestEnsureTemplate_PropagatesCloneError(t *testing.T) {
	b, g, _, _ := newTestBootstrap()
	want := errors.New("network down")
	g.Failures["Clone"] = want
	err := b.EnsureTemplate(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("got %v want wrapping %v", err, want)
	}
}

func TestSetupInstance_FullFlow(t *testing.T) {
	b, _, py, fs := newTestBootstrap()
	// Pretend the template was already cloned.
	fs.PresetExists[b.TemplateDir] = true
	// Pretend the copied dir's requirements.txt exists.
	const apiDir = "/root/mitra_x-api"
	fs.PresetExists[apiDir+"/requirements.txt"] = true

	err := b.SetupInstance(context.Background(), SetupInstanceParams{
		APIDir:       apiDir,
		InstanceName: "mitra_x",
		DBHost:       "localhost",
		DBPort:       3306,
		DBName:       "mitra_x",
		DBUser:       "mitra_x",
		DBPass:       "Sup3r",
		SwaggerPass:  "swP",
	})
	if err != nil {
		t.Fatal(err)
	}

	if fs.Dirs[apiDir] != b.TemplateDir {
		t.Fatalf("CopyDir lineage = %q want %q", fs.Dirs[apiDir], b.TemplateDir)
	}
	if !py.Venvs[apiDir+"/venv"] {
		t.Fatal("venv not created")
	}
	pipSeen := false
	for _, c := range py.Calls {
		if c.Method == "PipInstall" {
			pipSeen = true
			if c.Args[1] != apiDir+"/requirements.txt" {
				t.Fatalf("pip install requirements arg = %q", c.Args[1])
			}
		}
	}
	if !pipSeen {
		t.Fatal("PipInstall not called")
	}

	envBytes, ok := fs.Files[apiDir+"/.env"]
	if !ok {
		t.Fatal(".env not written")
	}
	env := string(envBytes)
	for _, want := range []string{
		"APP_NAME=mitra_x-api",
		"SWAGGER_USERNAME=admin",
		"SWAGGER_PASSWORD=swP",
		"DB_NAME=mitra_x",
		"DB_USER=mitra_x",
		"DB_PASSWORD=Sup3r",
	} {
		if !strings.Contains(env, want) {
			t.Errorf(".env missing %q\nfull:\n%s", want, env)
		}
	}
}

func TestSetupInstance_SkipsPipIfNoRequirements(t *testing.T) {
	b, _, py, _ := newTestBootstrap()
	// Don't preset requirements.txt — Exists() returns false.
	err := b.SetupInstance(context.Background(), SetupInstanceParams{
		APIDir:       "/root/x-api",
		InstanceName: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range py.Calls {
		if c.Method == "PipInstall" {
			t.Fatal("PipInstall should not run when requirements.txt missing")
		}
	}
}

func TestSetupInstance_RejectsInvalidName(t *testing.T) {
	b, _, _, _ := newTestBootstrap()
	for _, bad := range []string{"foo bar", "foo`bar", "", "DROP-TABLE"} {
		err := b.SetupInstance(context.Background(), SetupInstanceParams{
			APIDir:       "/root/x-api",
			InstanceName: bad,
		})
		if err == nil {
			t.Errorf("expected error for name %q", bad)
		}
	}
}

func TestTeardown_RemovesAPIDir(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	const apiDir = "/root/x-api"
	fs.Dirs[apiDir] = "src"
	if err := b.Teardown(context.Background(), apiDir); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.Dirs[apiDir]; ok {
		t.Fatal("api dir should be removed")
	}
}
