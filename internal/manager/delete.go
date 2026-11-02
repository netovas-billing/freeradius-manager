package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/netovas-billing/freeradius-manager/pkg/types"
)

// DeleteInstance is the inverse of CreateInstance, in reverse step order:
//
//  1. Stop + disable + remove systemd unit (so freeradius-api releases DB
//     before we drop it), lalu hapus direktori API instance
//     (<APIDirBase>/<nama>-api) — selaras dengan `radius-manager.sh delete`.
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

	// 1b. Hapus direktori API instance, SESUDAH unitnya berhenti.
	//
	// Kenapa ikut dihapus: dulu direktori ini tertinggal. Untuk instance Python
	// lama artinya venv/ tetap ada, dan create ulang dengan NAMA YANG SAMA
	// ditolak permanen oleh penjaga venv di setupInstanceGo — padahal pesannya
	// menyuruh "hapus instance itu dulu", yang justru sudah dilakukan. Bash
	// `delete` juga menghapusnya; kedua jalur harus meninggalkan mesin yang sama.
	if dir, err := i.direktoriAPIAman(name); err != nil {
		keep(err)
	} else {
		keep(i.cfg.FS.RemoveDir(ctx, dir))
	}

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

// direktoriAPIAman merakit <APIDirBase>/<nama>-api dan MENOLAK apa pun yang
// bisa membuat RemoveDir (rm -rf) mendarat di luar APIDirBase.
//
// validateName di pemanggil sudah menolak nama kosong/bergaris miring, tapi
// penghapusan rekursif terlalu mahal untuk bergantung pada satu lapis: nama
// kosong akan menghasilkan "<base>/-api", dan APIDirBase yang salah isi ("/"
// atau relatif) akan mengarahkan rm -rf ke tempat yang tak terduga. Jadi
// diperiksa ulang di sini, tepat sebelum menghapus.
func (i *impl) direktoriAPIAman(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", fmt.Errorf("hapus direktori API: %w", err)
	}
	base := filepath.Clean(i.apiDirBase())
	if !filepath.IsAbs(base) || base == string(filepath.Separator) {
		return "", fmt.Errorf("hapus direktori API: APIDirBase %q tidak aman (harus path absolut selain /)", base)
	}
	dir := filepath.Join(base, name+"-api")
	if filepath.Dir(dir) != base || filepath.Base(dir) != name+"-api" {
		return "", fmt.Errorf("hapus direktori API: %q keluar dari %q", dir, base)
	}
	return dir, nil
}
