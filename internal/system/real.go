package system

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RealSystemctl shells out to the systemctl(1) binary. Designed for
// Linux production. On macOS dev machines, the WriteUnit/RemoveUnit
// methods still work (they just write/remove files); the systemctl
// commands will fail at runtime.
type RealSystemctl struct {
	UnitDir string // default /etc/systemd/system
	Bin     string // default /bin/systemctl
}

func NewRealSystemctl() *RealSystemctl {
	return &RealSystemctl{
		UnitDir: "/etc/systemd/system",
		Bin:     "systemctl",
	}
}

func (s *RealSystemctl) unitPath(unitName string) string {
	return filepath.Join(s.UnitDir, unitName)
}

func (s *RealSystemctl) WriteUnit(_ context.Context, unitName, content string) error {
	if err := os.MkdirAll(s.UnitDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.unitPath(unitName), []byte(content), 0o644)
}

func (s *RealSystemctl) RemoveUnit(_ context.Context, unitName string) error {
	err := os.Remove(s.unitPath(unitName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *RealSystemctl) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s failed: %w; output: %s",
			strings.Join(args, " "), err, out.String())
	}
	return nil
}

func (s *RealSystemctl) DaemonReload(ctx context.Context) error { return s.run(ctx, "daemon-reload") }
func (s *RealSystemctl) Enable(ctx context.Context, n string) error {
	return s.run(ctx, "enable", "--quiet", n)
}
func (s *RealSystemctl) Disable(ctx context.Context, n string) error {
	return s.run(ctx, "disable", "--quiet", n)
}
func (s *RealSystemctl) Start(ctx context.Context, n string) error   { return s.run(ctx, "start", n) }
func (s *RealSystemctl) Stop(ctx context.Context, n string) error    { return s.run(ctx, "stop", n) }
func (s *RealSystemctl) Restart(ctx context.Context, n string) error { return s.run(ctx, "restart", n) }
func (s *RealSystemctl) IsActive(ctx context.Context, n string) (bool, error) {
	cmd := exec.CommandContext(ctx, s.Bin, "is-active", "--quiet", n)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
		// systemctl is-active exit code 3 = inactive (not an error).
		return false, nil
	}
	return false, err
}

// RealFreeRADIUS reloads/restarts the freeradius service.
type RealFreeRADIUS struct {
	Systemctl Systemctl
	UnitName  string // default "freeradius"
}

func NewRealFreeRADIUS(s Systemctl) *RealFreeRADIUS {
	return &RealFreeRADIUS{Systemctl: s, UnitName: "freeradius"}
}

func (f *RealFreeRADIUS) Reload(ctx context.Context) error  { return f.Systemctl.Restart(ctx, f.UnitName) }
func (f *RealFreeRADIUS) Restart(ctx context.Context) error { return f.Systemctl.Restart(ctx, f.UnitName) }

// RealFilesystem performs real OS file operations. All paths must be
// absolute to avoid surprises.
type RealFilesystem struct{}

func NewRealFilesystem() *RealFilesystem { return &RealFilesystem{} }

func (RealFilesystem) WriteFile(_ context.Context, path string, content []byte, mode uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, os.FileMode(mode))
}

func (RealFilesystem) RemoveFile(_ context.Context, path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (RealFilesystem) Symlink(_ context.Context, target, link string) error {
	// Idempotent: remove existing link first.
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, link)
}

func (RealFilesystem) RemoveSymlink(_ context.Context, link string) error {
	err := os.Remove(link)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (RealFilesystem) Chown(_ context.Context, path, user, group string) error {
	// chown by name requires looking up uids; shell out for simplicity.
	cmd := exec.Command("chown", "-h", user+":"+group, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chown %s %s:%s: %w; output: %s", path, user, group, err, out)
	}
	return nil
}

// CopyDir recursively copies src into dst. Walks src and replicates the
// tree at dst. Preserves file mode bits but does not preserve owner
// (filesystem chown happens via Chown explicitly).
func (RealFilesystem) CopyDir(_ context.Context, src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("src %s is not a directory", src)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}

	return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		// Regular file.
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		return nil
	})
}

// RemoveDir deletes a directory tree. Idempotent.
func (RealFilesystem) RemoveDir(_ context.Context, path string) error {
	err := os.RemoveAll(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Exists reports whether a path exists on disk.
func (RealFilesystem) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// RealGit shells out to the system git binary.
type RealGit struct {
	Bin string // default "git"
}

func NewRealGit() *RealGit { return &RealGit{Bin: "git"} }

func (g *RealGit) bin() string {
	if g.Bin == "" {
		return "git"
	}
	return g.Bin
}

func (g *RealGit) Clone(ctx context.Context, repoURL, dir string) error {
	cmd := exec.CommandContext(ctx, g.bin(), "clone", "--quiet", repoURL, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s -> %s: %w; output: %s", repoURL, dir, err, out)
	}
	return nil
}

func (g *RealGit) Pull(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, g.bin(), "-C", dir, "pull", "--quiet", "--ff-only")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull in %s: %w; output: %s", dir, err, out)
	}
	return nil
}

// RealPython shells out to the system python3 + pip.
type RealPython struct {
	Bin string // default "python3"
}

func NewRealPython() *RealPython { return &RealPython{Bin: "python3"} }

func (p *RealPython) bin() string {
	if p.Bin == "" {
		return "python3"
	}
	return p.Bin
}

func (p *RealPython) CreateVenv(ctx context.Context, venvDir string) error {
	// Idempotent: skip if a python interpreter already lives there.
	if _, err := os.Stat(filepath.Join(venvDir, "bin", "python")); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(venvDir, "bin", "python3")); err == nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, p.bin(), "-m", "venv", venvDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("python -m venv %s: %w; output: %s", venvDir, err, out)
	}
	return nil
}

func (p *RealPython) PipInstall(ctx context.Context, venvDir, requirementsFile string) error {
	pip := filepath.Join(venvDir, "bin", "pip")
	cmd := exec.CommandContext(ctx, pip, "install", "-q", "-r", requirementsFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pip install -r %s in %s: %w; output: %s",
			requirementsFile, venvDir, err, out)
	}
	return nil
}
