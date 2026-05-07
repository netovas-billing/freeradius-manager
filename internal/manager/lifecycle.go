package manager

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/heirro/freeradius-manager/pkg/types"
)

// unitNameFor returns the systemd unit name for the per-instance API.
// Bash convention: "{name}-api.service".
func unitNameFor(name string) string { return name + "-api.service" }

// ensureExists is a precondition for all lifecycle ops.
func (i *impl) ensureExists(name string) error {
	if _, err := i.readInstanceFile(name); err != nil {
		return err // ErrInstanceNotFound bubbles up
	}
	return nil
}

func (i *impl) StartInstance(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := i.requireDeps(); err != nil {
		return err
	}
	if err := i.ensureExists(name); err != nil {
		return err
	}
	return i.cfg.Systemctl.Start(ctx, unitNameFor(name))
}

func (i *impl) StopInstance(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := i.requireDeps(); err != nil {
		return err
	}
	if err := i.ensureExists(name); err != nil {
		return err
	}
	return i.cfg.Systemctl.Stop(ctx, unitNameFor(name))
}

func (i *impl) RestartInstance(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := i.requireDeps(); err != nil {
		return err
	}
	if err := i.ensureExists(name); err != nil {
		return err
	}
	return i.cfg.Systemctl.Restart(ctx, unitNameFor(name))
}

// TestInstance probes whether each declared port is reachable. We do
// not require systemctl or freeradius to be up — this is purely a
// network-level check that any consumer of the instance can repro.
func (i *impl) TestInstance(ctx context.Context, name string) (*types.TestResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	state, err := i.readInstanceFile(name)
	if err != nil {
		return nil, err
	}

	checks := map[string]bool{
		"auth": probePort(ctx, state.AuthPort),
		"acct": probePort(ctx, state.AcctPort),
		"coa":  probePort(ctx, state.CoAPort),
		"api":  probeTCP(ctx, state.APIPort),
	}
	return &types.TestResult{
		Name:       name,
		PortChecks: checks,
	}, nil
}

// probePort reports true if a UDP listener answers on localhost:port.
// FreeRADIUS auth/acct/coa are UDP. We open a UDP socket and try to
// receive — kernel will refuse the local read if nothing is bound.
//
// In practice this check is best-effort; UDP probing without sending
// traffic is unreliable. We instead just check that *we* can bind to
// the port via net.ListenPacket — if we cannot, something else has
// it (which is what we want for an active instance).
func probePort(ctx context.Context, port int) bool {
	if port == 0 {
		return false
	}
	conn, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		// Already in use → something is listening, so we treat as up.
		return true
	}
	conn.Close()
	return false
}

// probeTCP returns true if a TCP server accepts a connection on
// localhost:port within a short timeout.
func probeTCP(_ context.Context, port int) bool {
	if port == 0 {
		return false
	}
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := d.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
