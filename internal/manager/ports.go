package manager

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PortRegistry persists port allocations in the same flat-file format
// radius-manager.sh writes to:
//
//   <port> # <admin> <kind>
//
// where kind ∈ {auth, acct, coa, inner, api}.
//
// The bash script and the Go RM-API both flock this file so cross-process
// races are safe (advisory POSIX lock).
type PortRegistry struct {
	Path           string
	AuthPortMin    int
	AuthPortMax    int
	APIPortStart   int
	maxAttempts    int
}

func NewPortRegistry(path string) *PortRegistry {
	return &PortRegistry{
		Path:         path,
		AuthPortMin:  10000,
		AuthPortMax:  59000,
		APIPortStart: 8100,
		maxAttempts:  10000,
	}
}

type portEntry struct {
	port  int
	admin string
	kind  string
}

// withLock executes fn while holding an exclusive flock on the registry file.
// The file is created if it does not exist.
func (r *PortRegistry) withLock(fn func(f *os.File) error) error {
	f, err := os.OpenFile(r.Path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open port registry %s: %w", r.Path, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", r.Path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn(f)
}

func parseRegistry(f *os.File) ([]portEntry, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	var out []portEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Format: "<port> # <admin> <kind>"
		hashIdx := strings.Index(line, "#")
		var portStr, rest string
		if hashIdx == -1 {
			portStr = strings.TrimSpace(line)
		} else {
			portStr = strings.TrimSpace(line[:hashIdx])
			rest = strings.TrimSpace(line[hashIdx+1:])
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		entry := portEntry{port: port}
		if rest != "" {
			parts := strings.Fields(rest)
			if len(parts) >= 1 {
				entry.admin = parts[0]
			}
			if len(parts) >= 2 {
				entry.kind = parts[1]
			}
		}
		out = append(out, entry)
	}
	return out, scanner.Err()
}

func writeRegistry(f *os.File, entries []portEntry) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range entries {
		fmt.Fprintf(w, "%d # %s %s\n", e.port, e.admin, e.kind)
	}
	return w.Flush()
}

// usedPortsMap collects every registered port into a fast lookup set.
func usedPortsMap(entries []portEntry) map[int]bool {
	out := make(map[int]bool, len(entries))
	for _, e := range entries {
		out[e.port] = true
	}
	return out
}

// UsedPorts returns the set of all currently registered ports.
// Useful for tests + diagnostics.
func (r *PortRegistry) UsedPorts() (map[int]bool, error) {
	var out map[int]bool
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		out = usedPortsMap(entries)
		return nil
	})
	return out, err
}

// AllocateAuthPort finds a free quad (auth, acct=auth+1, coa=auth+2000,
// inner=auth+5000) and registers all four under the given admin name.
//
// Returns the chosen auth port. Equivalent to bash's
// find_available_port + register_port.
func (r *PortRegistry) AllocateAuthPort(admin string) (int, error) {
	var chosen int
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		used := usedPortsMap(entries)

		for attempt := 0; attempt < r.maxAttempts; attempt++ {
			port, err := randPortInRange(r.AuthPortMin, r.AuthPortMax)
			if err != nil {
				return err
			}
			quad := []int{port, port + 1, port + 2000, port + 5000}
			conflict := false
			for _, q := range quad {
				if used[q] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			// Future-proof: also check actual port liveness.
			if anyListening(quad) {
				continue
			}
			// Register the quad.
			for _, q := range quad {
				kind := portKind(q, port)
				entries = append(entries, portEntry{port: q, admin: admin, kind: kind})
			}
			if err := writeRegistry(f, entries); err != nil {
				return err
			}
			chosen = port
			return nil
		}
		return ErrPortExhausted
	})
	if err != nil {
		return 0, err
	}
	return chosen, nil
}

// AllocateAPIPort returns the next sequential available API port starting
// at APIPortStart. Equivalent to bash's find_available_api_port.
func (r *PortRegistry) AllocateAPIPort(admin string) (int, error) {
	var chosen int
	err := r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		used := usedPortsMap(entries)
		for port := r.APIPortStart; port < r.APIPortStart+r.maxAttempts; port++ {
			if used[port] {
				continue
			}
			if anyListening([]int{port}) {
				continue
			}
			entries = append(entries, portEntry{port: port, admin: admin, kind: "api"})
			if err := writeRegistry(f, entries); err != nil {
				return err
			}
			chosen = port
			return nil
		}
		return ErrPortExhausted
	})
	if err != nil {
		return 0, err
	}
	return chosen, nil
}

// Unregister removes every entry whose admin matches name. Idempotent.
func (r *PortRegistry) Unregister(admin string) error {
	return r.withLock(func(f *os.File) error {
		entries, err := parseRegistry(f)
		if err != nil {
			return err
		}
		kept := entries[:0]
		for _, e := range entries {
			if e.admin == admin {
				continue
			}
			kept = append(kept, e)
		}
		return writeRegistry(f, kept)
	})
}

func portKind(q, base int) string {
	switch q - base {
	case 0:
		return "auth"
	case 1:
		return "acct"
	case 2000:
		return "coa"
	case 5000:
		return "inner"
	}
	return "unknown"
}

func randPortInRange(min, max int) (int, error) {
	if max <= min {
		return 0, fmt.Errorf("invalid port range [%d,%d]", min, max)
	}
	span := uint32(max - min + 1)
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	n := binary.BigEndian.Uint32(b[:]) % span
	return int(n) + min, nil
}

// anyListening reports true if at least one port in the slice has a TCP
// or UDP listener bound on localhost. Conservative: any failure is
// treated as "not listening" so we don't block allocation when net APIs
// are unavailable (e.g., sandboxed test runners).
//
// In v0.1.0 we no-op so unit tests are deterministic. The real probe is
// added in Phase 3 alongside the test endpoint.
func anyListening(_ []int) bool { return false }
