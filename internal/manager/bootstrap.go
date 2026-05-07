package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/heirro/freeradius-manager/internal/system"
)

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
