package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/netovas-billing/freeradius-manager/pkg/types"
)

var startTime = time.Now()

func (i *impl) GetInstance(ctx context.Context, name string, includeSecrets bool) (*types.Instance, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	f, err := i.readInstanceFile(name)
	if err != nil {
		return nil, err
	}
	inst := f.toInstance(includeSecrets)
	// TODO(phase-1): probe actual port listen status + freeradius-api process status.
	// For now we mark as "unknown" so consumers don't assume running.
	return inst, nil
}

func (i *impl) ListInstances(ctx context.Context) ([]types.Instance, error) {
	dir := i.cfg.FreeRADIUSDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read freeradius dir %s: %w", dir, err)
	}

	var out []types.Instance
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".instance_") {
			continue
		}
		name := strings.TrimPrefix(e.Name(), ".instance_")
		if err := validateName(name); err != nil {
			continue
		}
		f, err := i.readInstanceFile(name)
		if err != nil {
			continue
		}
		out = append(out, *f.toInstance(false))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (i *impl) ServerInfo(ctx context.Context) (*types.ServerInfo, error) {
	hostname, _ := os.Hostname()

	// instance count via dir scan (no secrets needed).
	count := 0
	if entries, err := os.ReadDir(i.cfg.FreeRADIUSDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), ".instance_") {
				count++
			}
		}
	}

	return &types.ServerInfo{
		Hostname:          hostname,
		VPNIP:             i.cfg.VPNIP,
		FreeRADIUSVersion: detectFreeRADIUSVersion(),
		MariaDBVersion:    "unknown", // TODO(phase-1): query SELECT VERSION()
		CapacityMax:       i.cfg.CapacityMax,
		InstancesCount:    count,
		UptimeSeconds:     int64(time.Since(startTime).Seconds()),
		RMAPIVersion:      i.cfg.APIVersion,
	}, nil
}

func (i *impl) HealthCheck(ctx context.Context) (*types.Health, error) {
	checks := map[string]string{
		"go_runtime":   runtime.Version(),
		"freeradius_dir": dirCheck(i.cfg.FreeRADIUSDir),
		// TODO(phase-1): actual checks for freeradius service + mariadb connectivity.
		"freeradius": "unknown",
		"mariadb":    "unknown",
	}
	var issues []string
	for k, v := range checks {
		if strings.HasPrefix(v, "error:") {
			issues = append(issues, k+": "+strings.TrimPrefix(v, "error:"))
		}
	}
	status := "healthy"
	if len(issues) > 0 {
		status = "degraded"
	}
	return &types.Health{
		Status: status,
		Checks: checks,
		Issues: issues,
	}, nil
}

func dirCheck(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return "error:" + err.Error()
	}
	if !st.IsDir() {
		return "error:not a directory"
	}
	return "ok"
}

func detectFreeRADIUSVersion() string {
	// TODO(phase-1): exec `freeradius -v | head -1` and parse.
	// For scaffold we just stat the binary.
	if _, err := os.Stat("/usr/sbin/freeradius"); err == nil {
		return "installed (version probe TBD)"
	}
	if _, err := os.Stat(filepath.Join("/", "usr", "local", "sbin", "freeradius")); err == nil {
		return "installed (version probe TBD)"
	}
	return "not_installed"
}
