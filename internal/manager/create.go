package manager

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/heirro/freeradius-manager/internal/templates"
	"github.com/heirro/freeradius-manager/pkg/types"
)

// CreateInstance orchestrates the full instance bring-up. The transactional
// pattern follows SRS-RadiusManagerAPI.md §10.1: each step appends a
// cleanup function; on any failure cleanups run in reverse order.
func (i *impl) CreateInstance(ctx context.Context, req types.CreateInstanceRequest) (*types.CreateInstanceResponse, error) {
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if err := i.requireDeps(); err != nil {
		return nil, err
	}

	// Idempotency: refuse if instance file already present.
	if _, err := i.readInstanceFile(req.Name); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrInstanceExists, req.Name)
	}

	name := req.Name
	dbName, dbUser := name, name

	// dbPass is taken from request or generated.
	dbPass := derefStr(req.DBPassword)
	if dbPass == "" {
		var err error
		dbPass, err = GeneratePassword(20)
		if err != nil {
			return nil, fmt.Errorf("generate db password: %w", err)
		}
	}

	// Cleanup registry. Each successful step appends to this slice; on
	// any error before the final commit, cleanups run in reverse.
	type cleanupFn func() error
	var cleanups []cleanupFn
	addCleanup := func(fn cleanupFn) { cleanups = append(cleanups, fn) }

	committed := false
	defer func() {
		if committed {
			return
		}
		// Run cleanups in reverse order, swallowing errors (best-effort).
		for k := len(cleanups) - 1; k >= 0; k-- {
			_ = cleanups[k]()
		}
	}()

	// ---- Step 1: allocate ports ----
	authPort, err := i.cfg.Ports.AllocateAuthPort(name)
	if err != nil {
		return nil, fmt.Errorf("allocate ports: %w", err)
	}
	addCleanup(func() error { return i.cfg.Ports.Unregister(name) })

	apiPort, err := i.cfg.Ports.AllocateAPIPort(name)
	if err != nil {
		return nil, fmt.Errorf("allocate api port: %w", err)
	}
	// Single Unregister(name) above already covers both auth and api
	// since both use the same admin name.

	acctPort := authPort + 1
	coaPort := authPort + 2000
	innerPort := authPort + 5000

	// ---- Step 2: create database & user ----
	withDB := true
	if req.WithDB != nil {
		withDB = *req.WithDB
	}
	if withDB {
		if err := i.cfg.DB.CreateDatabase(ctx, dbName); err != nil {
			return nil, err
		}
		addCleanup(func() error { return i.cfg.DB.DropDatabase(context.Background(), dbName, dbUser) })

		if err := i.cfg.DB.CreateUserAndGrant(ctx, dbName, dbUser, dbPass); err != nil {
			return nil, err
		}
		// (drop user covered by DropDatabase cleanup)

		// ---- Step 3: import schema ----
		if len(i.cfg.Migrations) > 0 {
			if err := i.cfg.DB.ImportSchema(ctx, dbName, i.cfg.Migrations); err != nil {
				return nil, err
			}
		}
	}

	// ---- Step 4: render + write FreeRADIUS configs ----
	dbHost := i.instanceDBHost()
	dbPort := i.instanceDBPort()
	tvars := templates.Vars{
		InstanceName: name,
		DBHost:       dbHost,
		DBPort:       dbPort,
		DBName:       dbName,
		DBUser:       dbUser,
		DBPass:       dbPass,
		AuthPort:     authPort,
		AcctPort:     acctPort,
		CoAPort:      coaPort,
		InnerPort:    innerPort,
	}

	type renderJob struct {
		tmpl, modName, kind string // kind = "mods" or "sites"
	}
	jobs := []renderJob{
		{"sql_module", "sql_" + name, "mods"},
		{"eap_module", "eap_" + name, "mods"},
		{"inner_tunnel", "inner-tunnel-" + name, "sites"},
		{"virtual_server", name, "sites"},
	}
	for _, j := range jobs {
		var buf bytes.Buffer
		if err := templates.Render(&buf, j.tmpl, tvars); err != nil {
			return nil, fmt.Errorf("render %s: %w", j.tmpl, err)
		}
		availPath, enabledPath := i.frPaths(j.kind, j.modName)
		if err := i.cfg.FS.WriteFile(ctx, availPath, buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		availPathCopy := availPath
		addCleanup(func() error { return i.cfg.FS.RemoveFile(context.Background(), availPathCopy) })

		if err := i.cfg.FS.Symlink(ctx, availPath, enabledPath); err != nil {
			return nil, err
		}
		enabledPathCopy := enabledPath
		addCleanup(func() error { return i.cfg.FS.RemoveSymlink(context.Background(), enabledPathCopy) })

		if err := i.cfg.FS.Chown(ctx, enabledPath, i.frUser(), i.frGroup()); err != nil {
			return nil, err
		}
	}

	// ---- Step 5: reload freeradius ----
	if err := i.cfg.FreeRADIUS.Reload(ctx); err != nil {
		return nil, fmt.Errorf("freeradius reload: %w", err)
	}

	// ---- Step 6: generate swagger password ----
	swPass, err := GeneratePassword(20)
	if err != nil {
		return nil, err
	}
	apiDir := filepath.Join(i.apiDirBase(), name+"-api")

	// ---- Step 6b: bootstrap freeradius-api (v0.2.0) ----
	// Skipped if no bootstrap configured (v0.1.x behavior — caller
	// handled venv/clone out of band).
	if i.cfg.APIBootstrap != nil {
		if err := i.cfg.APIBootstrap.EnsureTemplate(ctx); err != nil {
			return nil, fmt.Errorf("ensure freeradius-api template: %w", err)
		}
		if err := i.cfg.APIBootstrap.SetupInstance(ctx, SetupInstanceParams{
			APIDir:       apiDir,
			InstanceName: name,
			DBHost:       dbHost,
			DBPort:       dbPort,
			DBName:       dbName,
			DBUser:       dbUser,
			DBPass:       dbPass,
			SwaggerUser:  "admin",
			SwaggerPass:  swPass,
		}); err != nil {
			return nil, fmt.Errorf("bootstrap api dir %s: %w", apiDir, err)
		}
		bootstrap := i.cfg.APIBootstrap
		apiDirCopy := apiDir
		addCleanup(func() error { return bootstrap.Teardown(context.Background(), apiDirCopy) })
	}

	// ---- Step 7: write systemd unit + start ----
	unitName := name + "-api.service"
	unitContent := i.renderAPIServiceUnit(name, apiDir, apiPort)
	if err := i.cfg.Systemctl.WriteUnit(ctx, unitName, unitContent); err != nil {
		return nil, fmt.Errorf("write unit %s: %w", unitName, err)
	}
	unitNameCopy := unitName
	addCleanup(func() error { return i.cfg.Systemctl.RemoveUnit(context.Background(), unitNameCopy) })

	if err := i.cfg.Systemctl.DaemonReload(ctx); err != nil {
		return nil, err
	}
	if err := i.cfg.Systemctl.Enable(ctx, unitName); err != nil {
		return nil, err
	}
	addCleanup(func() error { return i.cfg.Systemctl.Disable(context.Background(), unitNameCopy) })

	if err := i.cfg.Systemctl.Start(ctx, unitName); err != nil {
		return nil, err
	}
	addCleanup(func() error { return i.cfg.Systemctl.Stop(context.Background(), unitNameCopy) })

	// ---- Step 7: write .instance_<name> ----
	created := time.Now().UTC()
	apiURL := fmt.Sprintf("http://%s:%d", i.apiPublishIP(), apiPort)
	state := &instanceFile{
		AdminUsername:   name,
		DBHost:          dbHost,
		DBPort:          dbPort,
		DBName:          dbName,
		DBUser:          dbUser,
		DBPass:          dbPass,
		AuthPort:        authPort,
		AcctPort:        acctPort,
		CoAPort:         coaPort,
		InnerPort:       innerPort,
		APIPort:         apiPort,
		SwaggerUsername: "admin",
		SwaggerPassword: swPass,
		WebAPIURL:       apiURL + "/docs",
		Created:         created,
	}
	if err := i.writeInstanceFile(name, state); err != nil {
		return nil, err
	}
	stateName := name
	addCleanup(func() error {
		return i.cfg.FS.RemoveFile(context.Background(), i.instanceFilePath(stateName))
	})

	// ---- COMMIT ----
	committed = true

	return &types.CreateInstanceResponse{
		Name:   name,
		Status: types.StatusRunning,
		Ports: types.Ports{
			Auth: authPort, Acct: acctPort, CoA: coaPort, Inner: innerPort, API: apiPort,
		},
		Database: types.Database{
			Host:          dbHost,
			Port:          dbPort,
			Name:          dbName,
			User:          dbUser,
			Password:      dbPass,
			PasswordKnown: true,
		},
		Swagger: types.Credentials{
			Username:      "admin",
			Password:      swPass,
			PasswordKnown: true,
		},
		APIURL:     apiURL,
		SwaggerURL: apiURL + "/docs",
		CreatedAt:  created,
	}, nil
}

// requireDeps checks that all injected dependencies are present.
func (i *impl) requireDeps() error {
	missing := []string{}
	if i.cfg.DB == nil {
		missing = append(missing, "DB")
	}
	if i.cfg.Ports == nil {
		missing = append(missing, "Ports")
	}
	if i.cfg.Systemctl == nil {
		missing = append(missing, "Systemctl")
	}
	if i.cfg.FreeRADIUS == nil {
		missing = append(missing, "FreeRADIUS")
	}
	if i.cfg.FS == nil {
		missing = append(missing, "FS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: read-only mode (missing dependencies: %s)",
			ErrNotImplemented, strings.Join(missing, ","))
	}
	return nil
}

// frPaths returns (available_path, enabled_path) for a FreeRADIUS module
// or site. kind ∈ {"mods", "sites"}.
func (i *impl) frPaths(kind, modName string) (string, string) {
	root := i.cfg.FreeRADIUSDir
	switch kind {
	case "mods":
		return filepath.Join(root, "mods-available", modName),
			filepath.Join(root, "mods-enabled", modName)
	case "sites":
		return filepath.Join(root, "sites-available", modName),
			filepath.Join(root, "sites-enabled", modName)
	}
	return filepath.Join(root, modName), filepath.Join(root, modName)
}

// renderAPIServiceUnit produces the systemd unit for the per-instance
// freeradius-api process. The unit references the API directory which
// is expected to be bootstrapped by a separate provisioning step
// (template-once + copy strategy per RM-Q answer).
func (i *impl) renderAPIServiceUnit(name, apiDir string, apiPort int) string {
	return fmt.Sprintf(`[Unit]
Description=RadiusAPI with Uvicorn - %s
After=network.target

[Service]
User=root
Group=root
WorkingDirectory=%s
ExecStart=%s/venv/bin/uvicorn main:app --host 0.0.0.0 --port %d --workers 4
Restart=always
RestartSec=5
SyslogIdentifier=%s-api

[Install]
WantedBy=multi-user.target
`, name, apiDir, apiDir, apiPort, name)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
