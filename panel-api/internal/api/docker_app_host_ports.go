package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Docker app host ports. A published port binds 127.0.0.1 or 0.0.0.0 on the
// server, and 0.0.0.0:P collides with 127.0.0.1:P, so a host port is free only
// when no install holds it on any bind interface and nothing on the server
// already has a socket on it (agent net.loopback_listener_uid with
// any_address). An install given a port that something else held failed to
// start. Every door that gives out a host port (the admin and tenant install
// and edit handlers, and the CLI installs) goes through these.

var (
	// ErrHostPortInUse means a host port the request pins is held by another
	// install, by another port of the same install, or by something on the
	// server.
	ErrHostPortInUse = errors.New("host port in use")
	// ErrHostPortUnavailable means no free host port was found, or the server
	// couldn't be asked whether one is free.
	ErrHostPortUnavailable = errors.New("no host port available")
)

// hostPortProbeTimeout bounds one agent probe.
const hostPortProbeTimeout = 10 * time.Second

// maxHostPortProbes bounds how many ports in use on the server one allocation
// steps over before it gives up.
const maxHostPortProbes = 64

// hostPortHeld asks the agent whether something on the server has a socket on
// port. With no agent wired (unit tests) nothing is asked; an install needs
// the agent anyway.
func hostPortHeld(ctx context.Context, ag agent.AgentInterface, port int, protocol string) (bool, error) {
	if ag == nil {
		return false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, hostPortProbeTimeout)
	defer cancel()
	raw, err := ag.Call(cctx, "net.loopback_listener_uid", map[string]any{
		"port": port, "protocol": protocol, "any_address": true,
	})
	if err != nil {
		return false, err
	}
	var st struct {
		Bound *bool `json:"bound"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.Bound == nil {
		return false, errors.New("unexpected answer from the agent")
	}
	return *st.Bound, nil
}

// AllocateDockerHostPort gives out the lowest free host port for protocol and
// adds it to taken, the ports this install already has. A port is free when
// no install holds it, it is not reserved, it is not in taken, and nothing on
// the server has a socket on it. A port that couldn't be checked is not given
// out: the allocation fails with ErrHostPortUnavailable.
func AllocateDockerHostPort(ctx context.Context, repo repository.DockerAppRepository, ag agent.AgentInterface,
	protocol string, taken map[int]bool) (int, error) {
	var probeErr error
	probes := 0
	port, err := repo.FindFreeHostPort(ctx, protocol, func(p int) bool {
		if probeErr != nil || taken[p] || isReservedHostPort(p) {
			return true
		}
		if probes == maxHostPortProbes {
			probeErr = fmt.Errorf("the first %d free %s host ports are all in use on the server", maxHostPortProbes, protocol)
			return true
		}
		probes++
		held, herr := hostPortHeld(ctx, ag, p, protocol)
		if herr != nil {
			probeErr = fmt.Errorf("could not check host port %d/%s on the server: %v", p, protocol, herr)
			return true
		}
		return held
	})
	switch {
	case probeErr != nil:
		return 0, fmt.Errorf("%w: %v", ErrHostPortUnavailable, probeErr)
	case errors.Is(err, repository.ErrNotFound):
		return 0, fmt.Errorf("%w: every %s host port is taken", ErrHostPortUnavailable, protocol)
	case err != nil:
		return 0, err
	}
	if taken != nil {
		taken[port] = true
	}
	return port, nil
}

// checkPinnedHostPort refuses a host port the request pins (portName's) when
// another port of this install already has it, another install holds it, or
// something on the server has a socket on it, and adds it to taken. appID's
// own rows don't count, so an edit can keep its ports.
func checkPinnedHostPort(ctx context.Context, repo repository.DockerAppRepository, ag agent.AgentInterface,
	protocol string, port int, appID, portName string, taken map[int]bool) error {
	if taken[port] {
		return fmt.Errorf("%w: host port %d/%s is set for two ports of this install (port %s)", ErrHostPortInUse, port, protocol, portName)
	}
	inUse, err := repo.HostPortInUse(ctx, protocol, port, appID)
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("%w: host port %d/%s is already held by another install (port %s)", ErrHostPortInUse, port, protocol, portName)
	}
	held, err := hostPortHeld(ctx, ag, port, protocol)
	if err != nil {
		return fmt.Errorf("%w: could not check host port %d/%s on the server: %v", ErrHostPortUnavailable, port, protocol, err)
	}
	if held {
		return fmt.Errorf("%w: host port %d/%s is already in use on this server (port %s)", ErrHostPortInUse, port, protocol, portName)
	}
	taken[port] = true
	return nil
}

// dockerPortErrorStatus maps a port resolution error to its HTTP status and
// error code.
func dockerPortErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrHostPortInUse):
		return http.StatusConflict, "host_port_in_use"
	case errors.Is(err, ErrHostPortUnavailable):
		return http.StatusServiceUnavailable, "host_port_unavailable"
	}
	return http.StatusBadRequest, "port_resolution_failed"
}
