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

// patchScriptsParams returns a SetupInstanceParams matching the bash
// setup_api() example: localhost MariaDB on 3306, instance "mitra_x".
func patchScriptsParams() SetupInstanceParams {
	return SetupInstanceParams{
		APIDir:       "/root/mitra_x-api",
		InstanceName: "mitra_x",
		DBHost:       "10.254.252.2",
		DBPort:       3306,
		DBName:       "mitra_x",
		DBUser:       "mitra_x",
		DBPass:       "Sup3rSecret!",
	}
}

func TestPatchScripts_AutoclearzombieDBLines(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	const apiDir = "/root/mitra_x-api"
	const original = `#!/usr/bin/env bash
# autoclearzombie.sh — kills stale sessions
DB_HOST="default-host"
DB_PORT="3306"
DB_USER="defaultuser"
DB_PASS="defaultpass"
DB_NAME="defaultdb"

set -euo pipefail
echo "running"
`
	fs.Files[apiDir+"/autoclearzombie.sh"] = []byte(original)

	if err := b.PatchScripts(context.Background(), patchScriptsParams(), S3Config{}); err != nil {
		t.Fatal(err)
	}

	got := string(fs.Files[apiDir+"/autoclearzombie.sh"])
	wantContains := []string{
		`DB_HOST="10.254.252.2"`,
		`DB_PORT="3306"`,
		`DB_USER="mitra_x"`,
		`DB_PASS="Sup3rSecret!"`,
		`DB_NAME="mitra_x"`,
		`#!/usr/bin/env bash`,
		`set -euo pipefail`,
		`echo "running"`,
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("autoclearzombie missing %q\nfull:\n%s", w, got)
		}
	}

	// File should have been written with mode 0700 (executable) — check
	// last WriteFile call recorded for this path.
	mode := lastWriteMode(fs, apiDir+"/autoclearzombie.sh")
	if mode != "mode=700" {
		t.Errorf("expected WriteFile mode 700, got %q", mode)
	}
}

func TestPatchScripts_AutobackupS3DBAndS3Lines(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	const apiDir = "/root/mitra_x-api"
	const original = `#!/usr/bin/env bash
REMOTE="default"
BUCKET="default-bucket"
BACKUP_PATH="default/path"
DB_HOST="default"
DB_PORT="3306"
DB_USER="default"
DB_PASS="default"
DB_NAME="default"

# real script body below
echo "backing up"
`
	fs.Files[apiDir+"/autobackups3.sh"] = []byte(original)

	s3 := S3Config{Remote: "ljns3", Bucket: "backup-db", BackupRoot: "radiusdb"}
	if err := b.PatchScripts(context.Background(), patchScriptsParams(), s3); err != nil {
		t.Fatal(err)
	}
	got := string(fs.Files[apiDir+"/autobackups3.sh"])
	wantContains := []string{
		`REMOTE="ljns3"`,
		`BUCKET="backup-db"`,
		`BACKUP_PATH="radiusdb/mitra_x"`,
		`DB_HOST="10.254.252.2"`,
		`DB_PORT="3306"`,
		`DB_USER="mitra_x"`,
		`DB_PASS="Sup3rSecret!"`,
		`DB_NAME="mitra_x"`,
		`# real script body below`,
		`echo "backing up"`,
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("autobackups3 missing %q\nfull:\n%s", w, got)
		}
	}
}

func TestPatchScripts_SkipsMissingScriptsSilently(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	// No scripts pre-loaded — both should be silently skipped.
	if err := b.PatchScripts(context.Background(), patchScriptsParams(),
		S3Config{Remote: "ljns3", Bucket: "backup-db", BackupRoot: "radiusdb"}); err != nil {
		t.Fatalf("expected no error when scripts missing, got %v", err)
	}
	if len(fs.Files) != 0 {
		t.Errorf("no files should be written when scripts missing, got: %v", keys(fs.Files))
	}
}

func TestPatchScripts_PreservesUnrelatedLines(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	const apiDir = "/root/mitra_x-api"
	const original = `#!/usr/bin/env bash
# important comment, MUST not be touched
SOME_OTHER_VAR="keep me"
DB_HOST="old"
inline_DB_HOST="this should also stay because not at line start"
echo "DB_HOST inside echo line should also be left alone"
`
	fs.Files[apiDir+"/autoclearzombie.sh"] = []byte(original)

	if err := b.PatchScripts(context.Background(), patchScriptsParams(), S3Config{}); err != nil {
		t.Fatal(err)
	}
	got := string(fs.Files[apiDir+"/autoclearzombie.sh"])
	preserved := []string{
		`# important comment, MUST not be touched`,
		`SOME_OTHER_VAR="keep me"`,
		`inline_DB_HOST="this should also stay because not at line start"`,
		`echo "DB_HOST inside echo line should also be left alone"`,
	}
	for _, w := range preserved {
		if !strings.Contains(got, w) {
			t.Errorf("missing preserved line %q\nfull:\n%s", w, got)
		}
	}
	// And DB_HOST at start of line was rewritten.
	if !strings.Contains(got, `DB_HOST="10.254.252.2"`) {
		t.Errorf("expected DB_HOST rewrite\nfull:\n%s", got)
	}
}

func TestPatchScripts_AutobackupS3SkipsS3WhenZeroConfig(t *testing.T) {
	b, _, _, fs := newTestBootstrap()
	const apiDir = "/root/mitra_x-api"
	const original = `REMOTE="keep-this"
BUCKET="keep-this"
BACKUP_PATH="keep-this"
DB_HOST="default"
`
	fs.Files[apiDir+"/autobackups3.sh"] = []byte(original)

	if err := b.PatchScripts(context.Background(), patchScriptsParams(), S3Config{}); err != nil {
		t.Fatal(err)
	}
	got := string(fs.Files[apiDir+"/autobackups3.sh"])
	// DB_HOST gets rewritten regardless of S3 config.
	if !strings.Contains(got, `DB_HOST="10.254.252.2"`) {
		t.Errorf("expected DB_HOST rewrite\nfull:\n%s", got)
	}
	// S3 lines preserved when S3Config is zero.
	for _, w := range []string{`REMOTE="keep-this"`, `BUCKET="keep-this"`, `BACKUP_PATH="keep-this"`} {
		if !strings.Contains(got, w) {
			t.Errorf("expected preserved %q\nfull:\n%s", w, got)
		}
	}
}

// lastWriteMode digs out the mode argument for the most recent
// WriteFile call recording the given path.
func lastWriteMode(fs *system.MockFilesystem, path string) string {
	var got string
	for _, c := range fs.Calls {
		if c.Method == "WriteFile" && len(c.Args) >= 2 && c.Args[0] == path {
			got = c.Args[1]
		}
	}
	return got
}
