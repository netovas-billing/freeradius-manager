package system

import (
	"context"
	"sort"
	"sync"
)

// MockMaintenance records every InstallJob/RemoveJob call. Tests can
// assert on the recorded jobs rather than reaching into a real systemd
// or supervisord backend.
type MockMaintenance struct {
	mu       sync.Mutex
	Calls    []Call
	Jobs     map[string]MockJob // key = job name
	Failures map[string]error
}

type MockJob struct {
	Schedule string
	Command  string
	Env      map[string]string
}

func NewMockMaintenance() *MockMaintenance {
	return &MockMaintenance{
		Jobs:     map[string]MockJob{},
		Failures: map[string]error{},
	}
}

func (m *MockMaintenance) InstallJob(_ context.Context, name, schedule, command string, env map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "InstallJob", Args: []string{name, schedule, command}})
	if err := m.Failures["InstallJob"]; err != nil {
		return err
	}
	envCopy := make(map[string]string, len(env))
	for k, v := range env {
		envCopy[k] = v
	}
	m.Jobs[name] = MockJob{Schedule: schedule, Command: command, Env: envCopy}
	return nil
}

func (m *MockMaintenance) RemoveJob(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "RemoveJob", Args: []string{name}})
	if err := m.Failures["RemoveJob"]; err != nil {
		return err
	}
	delete(m.Jobs, name)
	return nil
}

func (m *MockMaintenance) ListJobs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "ListJobs"})
	if err := m.Failures["ListJobs"]; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m.Jobs))
	for k := range m.Jobs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
