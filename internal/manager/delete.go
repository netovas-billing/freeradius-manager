package manager

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/heirro/freeradius-manager/pkg/types"
)

// DeleteInstance is the inverse of CreateInstance, in reverse step order:
//
//  1. Stop + disable + remove systemd unit (so freeradius-api releases DB
//     before we drop it).
//  2. Remove FreeRADIUS sites/mods configs (both available + enabled symlink).
//  3. Reload FreeRADIUS so it stops listening on the instance ports.
//  4. Drop database + user (only when withDB=true).
//  5. Unregister ports.
//  6. Remove .instance_<name> file.
//
// Best-effort: continues on individual step errors but reports the first
// one in the response so operators can investigate. Bash behaves the same
// way — delete tolerates partial state.
func (i *impl) DeleteInstance(ctx context.Context, name string, withDB bool) (*types.DeleteInstanceResponse, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := i.requireDeps(); err != nil {
		return nil, err
	}

	state, err := i.readInstanceFile(name)
	if err != nil {
		return nil, err
	}

	var firstErr error
	keep := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}

	// 0. Tear down maintenance timers first so they can't fire mid-delete
	// (e.g. autobackups3 hitting an in-progress DROP DATABASE). Idempotent
	// — missing jobs are not an error per the system.Maintenance contract.
	if i.cfg.Maintenance != nil {
		keep(i.cfg.Maintenance.TeardownForInstance(ctx, name))
	}

	// 1. Tear down the API service.
	unitName := name + "-api.service"
	keep(i.cfg.Systemctl.Stop(ctx, unitName))
	keep(i.cfg.Systemctl.Disable(ctx, unitName))
	keep(i.cfg.Systemctl.RemoveUnit(ctx, unitName))
	keep(i.cfg.Systemctl.DaemonReload(ctx))

	// 2. Remove FreeRADIUS configs.
	for _, j := range []struct{ kind, modName string }{
		{"sites", name},
		{"sites", "inner-tunnel-" + name},
		{"mods", "eap_" + name},
		{"mods", "sql_" + name},
	} {
		avail, enabled := i.frPaths(j.kind, j.modName)
		keep(i.cfg.FS.RemoveSymlink(ctx, enabled))
		keep(i.cfg.FS.RemoveFile(ctx, avail))
	}

	// 3. Reload FreeRADIUS so the instance ports actually go down.
	keep(i.cfg.FreeRADIUS.Reload(ctx))

	// 4. Drop database (optional).
	dbDropped := false
	if withDB {
		if err := i.cfg.DB.DropDatabase(ctx, state.DBName, state.DBUser); err != nil {
			keep(fmt.Errorf("drop database: %w", err))
		} else {
			dbDropped = true
		}
	}

	// 5. Release ports.
	keep(i.cfg.Ports.Unregister(name))

	// 6. Remove .instance_<name>. We do it via stdlib because the mock
	// Filesystem in tests does not touch real disk for this path.
	if err := os.Remove(i.instanceFilePath(name)); err != nil && !os.IsNotExist(err) {
		keep(fmt.Errorf("remove instance file: %w", err))
	}

	_ = firstErr // surfaced via audit log in Phase 4.

	return &types.DeleteInstanceResponse{
		Name:            name,
		DeletedAt:       time.Now().UTC(),
		DatabaseDropped: dbDropped,
	}, nil
}
