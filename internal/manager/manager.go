// Package manager implements the core lifecycle operations for FreeRADIUS
// instances. It is the Go-native equivalent of radius-manager.sh and is
// shared with the HTTP layer (internal/api).
//
// See docs/SRS-RadiusManagerAPI.md §7 for design.
package manager

import (
	"context"
	"errors"

	"github.com/heirro/freeradius-manager/internal/system"
	"github.com/heirro/freeradius-manager/pkg/types"
)

var (
	ErrNotImplemented   = errors.New("operation not yet implemented in v0.1.0 scaffold")
	ErrInstanceNotFound = errors.New("instance not found")
	ErrInstanceExists   = errors.New("instance already exists")
	ErrPortExhausted    = errors.New("no available port in range")
	ErrInvalidName      = errors.New("invalid instance name")
)

type Manager interface {
	CreateInstance(ctx context.Context, req types.CreateInstanceRequest) (*types.CreateInstanceResponse, error)
	DeleteInstance(ctx context.Context, name string, withDB bool) (*types.DeleteInstanceResponse, error)
	GetInstance(ctx context.Context, name string, includeSecrets bool) (*types.Instance, error)
	ListInstances(ctx context.Context) ([]types.Instance, error)
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	RestartInstance(ctx context.Context, name string) error
	TestInstance(ctx context.Context, name string) (*types.TestResult, error)
	ServerInfo(ctx context.Context) (*types.ServerInfo, error)
	HealthCheck(ctx context.Context) (*types.Health, error)
}

type Config struct {
	FreeRADIUSDir string
	StateDir      string
	VPNIP         string
	CapacityMax   int
	APIVersion    string

	// Optional dependency injection. If left nil, write operations
	// will return ErrNotImplemented (Phase 1 read-only mode).
	DB         *DBManager
	Ports      *PortRegistry
	Systemctl  system.Systemctl
	FreeRADIUS system.FreeRADIUS
	FS         system.Filesystem

	// Migrations applied at instance create time.
	Migrations []Migration

	// FreeRADIUS user/group for chowning symlinks.
	FRUser  string // default "freerad"
	FRGroup string // default "freerad"

	// API hosting paths (matches radius-manager.sh):
	APIDirBase string // default "/root"; per-instance dir = APIDirBase/{name}-api

	// Listening IP that goes into WEB_API_URL written to .instance_<name>.
	APIPublishIP string // default "0.0.0.0"

	// InstanceDBHost / InstanceDBPort are written into the per-instance
	// FreeRADIUS sql module config and into the freeradius-api .env file.
	// On a single-VM RADIUS server these default to localhost:3306; in a
	// multi-container Docker stack they should be the MariaDB service
	// hostname. Empty/zero values fall back to "localhost"/3306.
	InstanceDBHost string
	InstanceDBPort int

	// PortRegistry path. If empty, derived from FreeRADIUSDir/.port_registry.
	PortRegistryPath string

	// freeradius-api bootstrap (v0.2.0). When non-nil, CreateInstance
	// will git-clone the template (once), copy it into APIDir, set up
	// the Python venv, and write `.env` before starting the systemd unit.
	// When nil, CreateInstance assumes the API directory has been
	// pre-provisioned out of band (the v0.1.x behavior).
	APIBootstrap *FreeRADIUSAPIBootstrap

	// Maintenance schedules per-instance autoclearzombie + autobackups3
	// timers via the system.Maintenance backend (v0.3.0). Nil preserves
	// v0.2.0 behavior (no timers installed) so existing tests / older
	// deployments continue to work unchanged. See §20 of the SRS.
	Maintenance *MaintenanceManager

	// MaintenanceS3 is the S3 destination passed through to
	// FreeRADIUSAPIBootstrap.PatchScripts when populated. Independent
	// from Maintenance because PatchScripts can patch the autobackups3
	// script even if scheduling is disabled (e.g. operator runs the
	// script manually). Zero-value disables the autobackups3 patch.
	MaintenanceS3 S3Config
}

func New(cfg Config) Manager {
	return &impl{cfg: cfg}
}

type impl struct {
	cfg Config
}

func (i *impl) frUser() string {
	if i.cfg.FRUser == "" {
		return "freerad"
	}
	return i.cfg.FRUser
}

func (i *impl) frGroup() string {
	if i.cfg.FRGroup == "" {
		return "freerad"
	}
	return i.cfg.FRGroup
}

func (i *impl) apiDirBase() string {
	if i.cfg.APIDirBase == "" {
		return "/root"
	}
	return i.cfg.APIDirBase
}

func (i *impl) apiPublishIP() string {
	if i.cfg.APIPublishIP == "" {
		return "0.0.0.0"
	}
	return i.cfg.APIPublishIP
}

// instanceDBHost / instanceDBPort return what gets written into
// per-instance FreeRADIUS configs and the freeradius-api .env. They
// default to localhost:3306 (single-VM behaviour) but can be overridden
// for multi-host deployments (e.g. the Docker dev stack where MariaDB
// is a separate service).
func (i *impl) instanceDBHost() string {
	if i.cfg.InstanceDBHost == "" {
		return "localhost"
	}
	return i.cfg.InstanceDBHost
}

func (i *impl) instanceDBPort() int {
	if i.cfg.InstanceDBPort == 0 {
		return 3306
	}
	return i.cfg.InstanceDBPort
}
