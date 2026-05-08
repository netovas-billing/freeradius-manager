package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/heirro/freeradius-manager/internal/system"
)

// S3Config carries the S3 backup destination passed to the
// autobackups3.sh credential patcher. Zero-value means the
// autobackups3 timer is not installed (and any pre-existing job is
// removed). Mirrors the constants from radius-manager.sh:
//
//	S3_REMOTE        ljns3
//	S3_BUCKET        backup-db
//	S3_BACKUP_ROOT   radiusdb       (per-instance suffix appended)
type S3Config struct {
	Remote     string
	Bucket     string
	BackupRoot string
}

// IsZero reports whether the S3Config is unconfigured. Treats Remote
// and Bucket as the required pair; BackupRoot can default to "radiusdb"
// without enabling backups. This matches the config-side contract:
// empty RM_API_S3_REMOTE means "no backups".
func (s S3Config) IsZero() bool {
	return s.Remote == "" || s.Bucket == ""
}

// FreeRADIUSAPIBootstrap encapsulates the template-once + copy-per-instance
// strategy for the freeradius-api repo (per RM-Q answer: option B).
//
//   - On first call to EnsureTemplate, the repo is git-cloned to TemplateDir.
//   - Subsequent calls only Pull (or no-op if SkipPull=true).
//   - SetupInstance copies TemplateDir → APIDir, creates a Python venv,
//     pip-installs requirements, and writes a `.env` file.
//
// The whole operation is mockable via the system.Git, system.Python, and
// system.Filesystem interfaces — production deploy uses RealGit/RealPython,
// unit tests use MockGit/MockPython.
type FreeRADIUSAPIBootstrap struct {
	RepoURL     string
	TemplateDir string
	SkipPull    bool

	Git    system.Git
	Python system.Python
	FS     system.Filesystem

	once sync.Mutex
}

// EnsureTemplate guarantees that TemplateDir exists and is up to date.
// Safe to call concurrently.
func (b *FreeRADIUSAPIBootstrap) EnsureTemplate(ctx context.Context) error {
	b.once.Lock()
	defer b.once.Unlock()

	exists, err := b.FS.Exists(ctx, b.TemplateDir)
	if err != nil {
		return fmt.Errorf("probe template dir: %w", err)
	}
	if !exists {
		if err := b.Git.Clone(ctx, b.RepoURL, b.TemplateDir); err != nil {
			return fmt.Errorf("git clone template: %w", err)
		}
		return nil
	}
	if b.SkipPull {
		return nil
	}
	if err := b.Git.Pull(ctx, b.TemplateDir); err != nil {
		return fmt.Errorf("git pull template: %w", err)
	}
	return nil
}

// SetupInstanceParams collects the parameters SetupInstance needs.
type SetupInstanceParams struct {
	APIDir         string // e.g., /root/mitra_x-api
	InstanceName   string
	DBHost         string
	DBPort         int
	DBName         string
	DBUser         string
	DBPass         string
	SwaggerUser    string // default "admin"
	SwaggerPass    string
}

// SetupInstance bootstraps an api directory by copying the template,
// creating a Python venv, pip-installing requirements, and writing .env.
// Returns the swagger username/password actually written (for the caller
// to record).
func (b *FreeRADIUSAPIBootstrap) SetupInstance(ctx context.Context, p SetupInstanceParams) error {
	if err := validateIdentifier(p.InstanceName); err != nil {
		return err
	}
	if p.APIDir == "" {
		return fmt.Errorf("APIDir is required")
	}

	exists, err := b.FS.Exists(ctx, p.APIDir)
	if err != nil {
		return fmt.Errorf("probe api dir: %w", err)
	}
	if !exists {
		if err := b.FS.CopyDir(ctx, b.TemplateDir, p.APIDir); err != nil {
			return fmt.Errorf("copy template: %w", err)
		}
	}

	venvDir := filepath.Join(p.APIDir, "venv")
	if err := b.Python.CreateVenv(ctx, venvDir); err != nil {
		return err
	}
	requirementsFile := filepath.Join(p.APIDir, "requirements.txt")
	reqExists, _ := b.FS.Exists(ctx, requirementsFile)
	if reqExists {
		if err := b.Python.PipInstall(ctx, venvDir, requirementsFile); err != nil {
			return err
		}
	}

	envContent := renderAPIEnvFile(p)
	envPath := filepath.Join(p.APIDir, ".env")
	if err := b.FS.WriteFile(ctx, envPath, []byte(envContent), 0o600); err != nil {
		return err
	}
	return nil
}

// Teardown removes the API directory. Idempotent.
func (b *FreeRADIUSAPIBootstrap) Teardown(ctx context.Context, apiDir string) error {
	return b.FS.RemoveDir(ctx, apiDir)
}

// PatchScripts rewrites the hardcoded `KEY="..."` shell-variable lines
// at the top of the freeradius-api maintenance scripts so the cloned
// template gets per-instance DB credentials and S3 destination baked
// in. Mirrors what radius-manager.sh:setup_api() does with sed(1).
//
// Files handled:
//
//	autoclearzombie.sh — DB_HOST, DB_PORT, DB_USER, DB_PASS, DB_NAME
//	autobackups3.sh   — DB_* (same keys) + REMOTE, BUCKET, BACKUP_PATH
//
// Idempotent: a missing script is silently skipped (treated as a
// template that doesn't ship that maintenance helper). The Maintenance
// interface backend (.service unit) also injects these as environment
// variables; the in-script patching is belt-and-suspenders for older
// freeradius-api revisions that don't honor env overrides.
func (b *FreeRADIUSAPIBootstrap) PatchScripts(ctx context.Context, p SetupInstanceParams, s3 S3Config) error {
	if err := validateIdentifier(p.InstanceName); err != nil {
		return err
	}
	if p.APIDir == "" {
		return fmt.Errorf("APIDir is required")
	}

	dbReplacements := map[string]string{
		"DB_HOST": p.DBHost,
		"DB_PORT": fmt.Sprintf("%d", p.DBPort),
		"DB_USER": p.DBUser,
		"DB_PASS": p.DBPass,
		"DB_NAME": p.DBName,
	}

	// autoclearzombie.sh: DB credentials only.
	if err := b.patchScript(ctx,
		filepath.Join(p.APIDir, "autoclearzombie.sh"),
		dbReplacements,
	); err != nil {
		return fmt.Errorf("patch autoclearzombie.sh: %w", err)
	}

	// autobackups3.sh: DB credentials + S3 destination.
	backupReplacements := make(map[string]string, len(dbReplacements)+3)
	for k, v := range dbReplacements {
		backupReplacements[k] = v
	}
	if !s3.IsZero() {
		backupReplacements["REMOTE"] = s3.Remote
		backupReplacements["BUCKET"] = s3.Bucket
		// BACKUP_PATH = <root>/<instance>  (matches bash setup_api()).
		backupReplacements["BACKUP_PATH"] = path.Join(s3.BackupRoot, p.InstanceName)
	}
	if err := b.patchScript(ctx,
		filepath.Join(p.APIDir, "autobackups3.sh"),
		backupReplacements,
	); err != nil {
		return fmt.Errorf("patch autobackups3.sh: %w", err)
	}
	return nil
}

// patchScript loads scriptPath, rewrites each `^KEY=...$` line whose
// KEY appears in replacements, and writes the file back with mode 0700
// (preserving the executable bit the bash flow sets via chmod +x).
//
// If the script doesn't exist, returns nil (idempotent skip). Any
// other read error is returned wrapped.
func (b *FreeRADIUSAPIBootstrap) patchScript(ctx context.Context, scriptPath string, replacements map[string]string) error {
	content, err := b.FS.ReadFile(ctx, scriptPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", scriptPath, err)
	}
	patched := applyShellVarPatches(content, replacements)
	if err := b.FS.WriteFile(ctx, scriptPath, patched, 0o700); err != nil {
		return fmt.Errorf("write %s: %w", scriptPath, err)
	}
	return nil
}

// applyShellVarPatches rewrites lines matching `^KEY=...$` (anchored at
// start of a line, allowing leading whitespace is intentional) for any
// KEY in replacements. Other lines pass through untouched, including
// comments, blanks, and any in-line definitions where `KEY=` is not at
// the start of the line. This matches sed `-e "s|^KEY=.*|KEY=...|"`.
func applyShellVarPatches(content []byte, replacements map[string]string) []byte {
	if len(replacements) == 0 {
		return content
	}
	keys := make([]string, 0, len(replacements))
	for k := range replacements {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	// Build a single regex `^(KEY1|KEY2|...)=.*$` (multiline) to keep
	// the rewrite a single pass over the file.
	re := regexp.MustCompile(`(?m)^(` + joinAlt(keys) + `)=.*$`)
	return re.ReplaceAllFunc(content, func(line []byte) []byte {
		// Extract the matched KEY (everything before the first '=').
		eq := indexByte(line, '=')
		if eq < 0 {
			return line
		}
		key := string(line[:eq])
		val, ok := replacements[key]
		if !ok {
			return line
		}
		return []byte(key + `="` + val + `"`)
	})
}

// joinAlt joins regex alternatives without depending on strings.Join
// import (kept tiny to avoid bloating the patch surface).
func joinAlt(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "|" + p
	}
	return out
}

// indexByte mirrors bytes.IndexByte without forcing the import here.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// renderAPIEnvFile produces a .env that matches what bash setup_api()
// generates verbatim. Format must remain in sync with freeradius-api's
// expectations (see config loader in that repo).
func renderAPIEnvFile(p SetupInstanceParams) string {
	swUser := p.SwaggerUser
	if swUser == "" {
		swUser = "admin"
	}
	return fmt.Sprintf(`# Application Settings
APP_NAME=%s-api
APP_DEBUG=False

# Swagger/BASIC Auth Credentials
SWAGGER_USERNAME=%s
SWAGGER_PASSWORD=%s

# Database Settings
DB_TYPE=mariadb
DB_HOST=%s
DB_PORT=%d
DB_NAME=%s
DB_USER=%s
DB_PASSWORD=%s
`, p.InstanceName, swUser, p.SwaggerPass, p.DBHost, p.DBPort, p.DBName, p.DBUser, p.DBPass)
}
