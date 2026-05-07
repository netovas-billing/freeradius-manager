package system

import (
	"context"
	"fmt"
	"sync"
)

// Call records one invocation of an interface method for later inspection
// in tests. Args are stringified to keep diffs readable.
type Call struct {
	Method string
	Args   []string
}

// MockSystemctl is a thread-safe in-memory Systemctl implementation that
// records every call and supports per-method failure injection.
type MockSystemctl struct {
	mu       sync.Mutex
	Calls    []Call
	UnitContent map[string]string // captured WriteUnit payloads
	Active   map[string]bool     // backing for IsActive

	// Failures: if a method name maps to a non-nil error, that call returns it.
	Failures map[string]error
}

func NewMockSystemctl() *MockSystemctl {
	return &MockSystemctl{
		UnitContent: map[string]string{},
		Active:      map[string]bool{},
		Failures:    map[string]error{},
	}
}

func (m *MockSystemctl) record(method string, args ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
	return m.Failures[method]
}

func (m *MockSystemctl) WriteUnit(_ context.Context, unitName, content string) error {
	if err := m.record("WriteUnit", unitName, fmt.Sprintf("%dB", len(content))); err != nil {
		return err
	}
	m.mu.Lock()
	m.UnitContent[unitName] = content
	m.mu.Unlock()
	return nil
}

func (m *MockSystemctl) DaemonReload(_ context.Context) error {
	return m.record("DaemonReload")
}

func (m *MockSystemctl) Enable(_ context.Context, unitName string) error {
	return m.record("Enable", unitName)
}

func (m *MockSystemctl) Disable(_ context.Context, unitName string) error {
	return m.record("Disable", unitName)
}

func (m *MockSystemctl) Start(_ context.Context, unitName string) error {
	if err := m.record("Start", unitName); err != nil {
		return err
	}
	m.mu.Lock()
	m.Active[unitName] = true
	m.mu.Unlock()
	return nil
}

func (m *MockSystemctl) Stop(_ context.Context, unitName string) error {
	if err := m.record("Stop", unitName); err != nil {
		return err
	}
	m.mu.Lock()
	m.Active[unitName] = false
	m.mu.Unlock()
	return nil
}

func (m *MockSystemctl) Restart(_ context.Context, unitName string) error {
	if err := m.record("Restart", unitName); err != nil {
		return err
	}
	m.mu.Lock()
	m.Active[unitName] = true
	m.mu.Unlock()
	return nil
}

func (m *MockSystemctl) IsActive(_ context.Context, unitName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "IsActive", Args: []string{unitName}})
	if err := m.Failures["IsActive"]; err != nil {
		return false, err
	}
	return m.Active[unitName], nil
}

func (m *MockSystemctl) RemoveUnit(_ context.Context, unitName string) error {
	if err := m.record("RemoveUnit", unitName); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.UnitContent, unitName)
	delete(m.Active, unitName)
	m.mu.Unlock()
	return nil
}

// MockFreeRADIUS records reload/restart calls.
type MockFreeRADIUS struct {
	mu       sync.Mutex
	Calls    []Call
	Failures map[string]error
}

func NewMockFreeRADIUS() *MockFreeRADIUS {
	return &MockFreeRADIUS{Failures: map[string]error{}}
}

func (f *MockFreeRADIUS) Reload(_ context.Context) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Method: "Reload"})
	f.mu.Unlock()
	return f.Failures["Reload"]
}

func (f *MockFreeRADIUS) Restart(_ context.Context) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Method: "Restart"})
	f.mu.Unlock()
	return f.Failures["Restart"]
}

// MockFilesystem records all write/remove/symlink/chown calls and stores
// file contents in memory for round-trip assertions.
type MockFilesystem struct {
	mu           sync.Mutex
	Calls        []Call
	Files        map[string][]byte
	Symlinks     map[string]string // link -> target
	Owners       map[string]string // path -> "user:group"
	Dirs         map[string]string // dst dir -> src lineage (CopyDir tracking)
	PresetExists map[string]bool   // optional override for Exists() probe
	Failures     map[string]error
}

func NewMockFilesystem() *MockFilesystem {
	return &MockFilesystem{
		Files:        map[string][]byte{},
		Symlinks:     map[string]string{},
		Owners:       map[string]string{},
		Dirs:         map[string]string{},
		PresetExists: map[string]bool{},
		Failures:     map[string]error{},
	}
}

func (m *MockFilesystem) WriteFile(_ context.Context, path string, content []byte, mode uint32) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "WriteFile", Args: []string{path, fmt.Sprintf("mode=%o", mode)}})
	if err := m.Failures["WriteFile"]; err != nil {
		m.mu.Unlock()
		return err
	}
	c := make([]byte, len(content))
	copy(c, content)
	m.Files[path] = c
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) RemoveFile(_ context.Context, path string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "RemoveFile", Args: []string{path}})
	if err := m.Failures["RemoveFile"]; err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.Files, path)
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) Symlink(_ context.Context, target, link string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Symlink", Args: []string{target, link}})
	if err := m.Failures["Symlink"]; err != nil {
		m.mu.Unlock()
		return err
	}
	m.Symlinks[link] = target
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) RemoveSymlink(_ context.Context, link string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "RemoveSymlink", Args: []string{link}})
	if err := m.Failures["RemoveSymlink"]; err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.Symlinks, link)
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) Chown(_ context.Context, path, user, group string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Chown", Args: []string{path, user, group}})
	if err := m.Failures["Chown"]; err != nil {
		m.mu.Unlock()
		return err
	}
	m.Owners[path] = user + ":" + group
	m.mu.Unlock()
	return nil
}

// Dirs holds CopyDir/RemoveDir state for the mock.
//
// We track them in a separate map (not Files) because Files is keyed
// by exact path while Dirs is keyed by directory root.
type mockDirs struct {
	mu   sync.Mutex
	dirs map[string]string // dst -> src lineage (for assertions)
}

func (m *MockFilesystem) CopyDir(_ context.Context, src, dst string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "CopyDir", Args: []string{src, dst}})
	if err := m.Failures["CopyDir"]; err != nil {
		m.mu.Unlock()
		return err
	}
	if m.Dirs == nil {
		m.Dirs = map[string]string{}
	}
	m.Dirs[dst] = src
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) RemoveDir(_ context.Context, path string) error {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "RemoveDir", Args: []string{path}})
	if err := m.Failures["RemoveDir"]; err != nil {
		m.mu.Unlock()
		return err
	}
	if m.Dirs != nil {
		delete(m.Dirs, path)
	}
	m.mu.Unlock()
	return nil
}

func (m *MockFilesystem) Exists(_ context.Context, path string) (bool, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, Call{Method: "Exists", Args: []string{path}})
	if err := m.Failures["Exists"]; err != nil {
		m.mu.Unlock()
		return false, err
	}
	_, fileExists := m.Files[path]
	_, dirExists := m.Dirs[path]
	exists := fileExists || dirExists
	if v, ok := m.PresetExists[path]; ok {
		exists = v
	}
	m.mu.Unlock()
	return exists, nil
}

// MockGit records Clone/Pull calls and treats Clone as creating a
// directory entry that subsequent CopyDir calls can reference.
type MockGit struct {
	mu       sync.Mutex
	Calls    []Call
	Repos    map[string]string // dir -> repoURL
	Failures map[string]error
}

func NewMockGit() *MockGit {
	return &MockGit{
		Repos:    map[string]string{},
		Failures: map[string]error{},
	}
}

func (g *MockGit) Clone(_ context.Context, repoURL, dir string) error {
	g.mu.Lock()
	g.Calls = append(g.Calls, Call{Method: "Clone", Args: []string{repoURL, dir}})
	if err := g.Failures["Clone"]; err != nil {
		g.mu.Unlock()
		return err
	}
	g.Repos[dir] = repoURL
	g.mu.Unlock()
	return nil
}

func (g *MockGit) Pull(_ context.Context, dir string) error {
	g.mu.Lock()
	g.Calls = append(g.Calls, Call{Method: "Pull", Args: []string{dir}})
	g.mu.Unlock()
	return g.Failures["Pull"]
}

// MockPython records venv + pip operations.
type MockPython struct {
	mu       sync.Mutex
	Calls    []Call
	Venvs    map[string]bool // venvDir -> created
	Failures map[string]error
}

func NewMockPython() *MockPython {
	return &MockPython{
		Venvs:    map[string]bool{},
		Failures: map[string]error{},
	}
}

func (p *MockPython) CreateVenv(_ context.Context, venvDir string) error {
	p.mu.Lock()
	p.Calls = append(p.Calls, Call{Method: "CreateVenv", Args: []string{venvDir}})
	if err := p.Failures["CreateVenv"]; err != nil {
		p.mu.Unlock()
		return err
	}
	p.Venvs[venvDir] = true
	p.mu.Unlock()
	return nil
}

func (p *MockPython) PipInstall(_ context.Context, venvDir, requirementsFile string) error {
	p.mu.Lock()
	p.Calls = append(p.Calls, Call{Method: "PipInstall", Args: []string{venvDir, requirementsFile}})
	p.mu.Unlock()
	return p.Failures["PipInstall"]
}
