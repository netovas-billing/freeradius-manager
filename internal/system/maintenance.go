package system

import "context"

// Maintenance schedules periodic maintenance jobs (autoclearzombie,
// autobackups3) per FreeRADIUS instance. The interface abstracts the
// scheduling backend:
//
//   - RealMaintenance: writes systemd .timer + .service unit pair,
//     daemon-reloads, enables, starts. Production Linux deployment.
//   - SupervisordMaintenance: writes a sleep-loop supervisord program
//     that approximates the schedule. Used by the Docker dev stack
//     where systemd is not available.
//   - MockMaintenance: records calls for unit tests.
//
// The contract is intentionally narrow: install one job, remove one job.
// The caller (manager.MaintenanceManager) holds the policy of WHICH jobs
// each instance gets and HOW OFTEN they run.
type Maintenance interface {
	// InstallJob registers a recurring job under the given name.
	//   name      — unique key, used to derive unit file names
	//               (e.g., "mitra_a-zombie" → "mitra_a-zombie.timer" + ".service")
	//   schedule  — "every 15m", "daily", "hourly", or a systemd
	//               OnCalendar expression (e.g., "*:0/15")
	//   command   — absolute path of the script/binary to execute. Must
	//               already be present on the host (no fetch/install
	//               happens here).
	//   env       — extra environment variables to inject when running
	//               the command (e.g., DB credentials so the script
	//               doesn't have to be sed-patched).
	//
	// Idempotent: re-installing the same name overwrites previous unit.
	InstallJob(ctx context.Context, name, schedule, command string, env map[string]string) error

	// RemoveJob unregisters a recurring job. Idempotent: missing jobs
	// are not an error.
	RemoveJob(ctx context.Context, name string) error

	// ListJobs returns names of installed jobs (best-effort; used for
	// drift detection between manager state and backend reality).
	ListJobs(ctx context.Context) ([]string, error)
}

// MaintenanceSchedule constants for the canonical schedules used by
// autoclearzombie and autobackups3. Implementations translate these to
// their backend dialect.
const (
	ScheduleEvery15m = "every 15m"
	ScheduleDaily    = "daily"
)
