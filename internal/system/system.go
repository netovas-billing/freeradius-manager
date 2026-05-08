// Package system defines interfaces for OS-level operations that
// radius-manager-api needs (systemctl, freeradius reload, filesystem).
//
// Production uses real implementations that shell out via os/exec.
// Tests use the mocks in mock.go which record every call for assertion.
package system

import "context"

// Systemctl manages systemd units. The "name" arguments must already
// include any required suffix (e.g., "mitra_x-api.service").
type Systemctl interface {
	WriteUnit(ctx context.Context, unitName, content string) error
	DaemonReload(ctx context.Context) error
	Enable(ctx context.Context, unitName string) error
	Disable(ctx context.Context, unitName string) error
	Start(ctx context.Context, unitName string) error
	Stop(ctx context.Context, unitName string) error
	Restart(ctx context.Context, unitName string) error
	IsActive(ctx context.Context, unitName string) (bool, error)
	RemoveUnit(ctx context.Context, unitName string) error
}

// FreeRADIUS controls the freeradius service.
type FreeRADIUS interface {
	Reload(ctx context.Context) error
	Restart(ctx context.Context) error
}

// Filesystem provides write/remove/symlink operations on FreeRADIUS
// configuration files. Wrapped in an interface so create/delete flows
// can be verified in tests without touching /etc.
type Filesystem interface {
	WriteFile(ctx context.Context, path string, content []byte, mode uint32) error
	// ReadFile returns the contents of path. Used by bootstrap to patch
	// hardcoded credentials in the freeradius-api maintenance scripts
	// (autoclearzombie.sh, autobackups3.sh) before they are scheduled.
	// Implementations should return os.ErrNotExist (or a wrapper) when
	// path is missing so callers can skip silently.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	RemoveFile(ctx context.Context, path string) error
	Symlink(ctx context.Context, target, link string) error
	RemoveSymlink(ctx context.Context, link string) error
	Chown(ctx context.Context, path, user, group string) error

	// CopyDir copies src directory tree to dst recursively, preserving
	// file modes. dst must not exist (or be empty); CopyDir does not
	// merge. Used by template-once strategy for freeradius-api.
	CopyDir(ctx context.Context, src, dst string) error

	// RemoveDir removes a directory tree. Idempotent.
	RemoveDir(ctx context.Context, path string) error

	// Exists reports whether a path exists. Used for idempotent
	// operations that need to skip if the target is already there.
	Exists(ctx context.Context, path string) (bool, error)
}

// Git wraps git binary operations. Used for the freeradius-api
// template-once strategy: clone the repo into a shared template
// directory at startup or on first CreateInstance, then re-pull on
// demand.
type Git interface {
	// Clone fetches a fresh copy of repoURL into dir.
	// dir must not exist or must be empty.
	Clone(ctx context.Context, repoURL, dir string) error

	// Pull updates an existing checkout to the latest commit on the
	// current branch. No-op if dir is not a git repo.
	Pull(ctx context.Context, dir string) error
}

// Python wraps Python tooling needed to set up the per-instance
// freeradius-api process: virtualenv creation and pip install.
type Python interface {
	// CreateVenv creates a virtualenv at venvDir. Idempotent: skips
	// if venvDir already contains a python interpreter.
	CreateVenv(ctx context.Context, venvDir string) error

	// PipInstall runs `<venvDir>/bin/pip install -r <requirementsFile>`.
	PipInstall(ctx context.Context, venvDir, requirementsFile string) error
}
