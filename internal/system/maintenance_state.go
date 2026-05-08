package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

// DefaultMaintenanceStateFile is the on-disk JSON ledger that the
// real and supervisord Maintenance backends use to track installed
// jobs. It survives manager restarts so ListJobs can answer without
// querying the underlying scheduler.
const DefaultMaintenanceStateFile = "/var/lib/radius-manager-api/maintenance-jobs.json"

// maintenanceState is the JSON document layout. Each known job is keyed
// by its name. We keep the schedule + command for forensic value: an
// operator inspecting the ledger can see what got installed without
// reading systemd unit files.
type maintenanceState struct {
	Jobs map[string]maintenanceStateEntry `json:"jobs"`
}

type maintenanceStateEntry struct {
	Schedule string            `json:"schedule"`
	Command  string            `json:"command"`
	Env      map[string]string `json:"env,omitempty"`
}

// maintenanceStore is the persistence helper shared by RealMaintenance
// and SupervisordMaintenance. It serializes access via flock(2) so
// concurrent radius-manager-api processes (e.g., during a rolling
// restart) cannot corrupt the ledger.
type maintenanceStore struct {
	// Path is the JSON file location. If empty,
	// DefaultMaintenanceStateFile is used.
	Path string

	// processMu protects in-process callers; flock guards
	// cross-process callers. Both layers are needed because flock(2)
	// on Linux is process-scoped — it does not synchronize goroutines
	// inside the same PID.
	processMu sync.Mutex
}

// newMaintenanceStore returns a store rooted at the given file path.
// An empty path falls back to DefaultMaintenanceStateFile.
func newMaintenanceStore(path string) *maintenanceStore {
	if path == "" {
		path = DefaultMaintenanceStateFile
	}
	return &maintenanceStore{Path: path}
}

// withLock takes the in-process mutex, opens (or creates) the ledger
// file with an exclusive flock, hands the parsed state to fn, and
// writes it back atomically before releasing the lock.
func (s *maintenanceStore) withLock(fn func(*maintenanceState) error) error {
	s.processMu.Lock()
	defer s.processMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("maintenance state: mkdir %s: %w", filepath.Dir(s.Path), err)
	}

	f, err := os.OpenFile(s.Path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("maintenance state: open %s: %w", s.Path, err)
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("maintenance state: flock %s: %w", s.Path, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	state, err := readState(f)
	if err != nil {
		return fmt.Errorf("maintenance state: read %s: %w", s.Path, err)
	}

	if err := fn(state); err != nil {
		return err
	}

	if err := writeState(f, state); err != nil {
		return fmt.Errorf("maintenance state: write %s: %w", s.Path, err)
	}
	return nil
}

// readState reads + parses the JSON ledger from f. An empty file (or
// unreadable JSON in an empty-stat file) returns a fresh state. A
// half-written/corrupt file returns the parse error so the caller
// notices instead of silently rewriting it.
func readState(f *os.File) (*maintenanceState, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	state := &maintenanceState{Jobs: map[string]maintenanceStateEntry{}}
	if info.Size() == 0 {
		return state, nil
	}
	dec := json.NewDecoder(f)
	if err := dec.Decode(state); err != nil {
		return nil, err
	}
	if state.Jobs == nil {
		state.Jobs = map[string]maintenanceStateEntry{}
	}
	return state, nil
}

// writeState rewinds f, truncates, and writes the encoded state.
// Pretty-printed for human readability — the ledger is small and the
// extra bytes are worth the readability when an operator inspects it.
func writeState(f *os.File, state *maintenanceState) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(state)
}

// upsert persists a job entry. Re-inserting a name overwrites.
func (s *maintenanceStore) upsert(name string, entry maintenanceStateEntry) error {
	if name == "" {
		return errors.New("maintenance state: empty job name")
	}
	envCopy := make(map[string]string, len(entry.Env))
	for k, v := range entry.Env {
		envCopy[k] = v
	}
	entry.Env = envCopy
	return s.withLock(func(state *maintenanceState) error {
		state.Jobs[name] = entry
		return nil
	})
}

// remove drops a job entry. Missing names are not an error.
func (s *maintenanceStore) remove(name string) error {
	return s.withLock(func(state *maintenanceState) error {
		delete(state.Jobs, name)
		return nil
	})
}

// list returns the sorted set of job names currently recorded.
func (s *maintenanceStore) list() ([]string, error) {
	var names []string
	err := s.withLock(func(state *maintenanceState) error {
		names = make([]string, 0, len(state.Jobs))
		for k := range state.Jobs {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}
