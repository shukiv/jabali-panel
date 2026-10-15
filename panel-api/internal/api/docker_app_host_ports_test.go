package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// A Docker app's host port binds 127.0.0.1 or 0.0.0.0 on the server, so a port
// is free only when no install holds it on any bind interface and nothing on
// the server already has a socket on it. Before, the allocator counted only
// the installs on the same bind interface, so an install could be given a
// port another container already used, and failed to start.

// fakePortRepo holds published ports by protocol, on any bind interface.
type fakePortRepo struct {
	repository.DockerAppRepository
	used map[string]map[int]string // protocol -> host port -> app id
}

func (f *fakePortRepo) FindFreeHostPort(_ context.Context, protocol string, skip func(int) bool) (int, error) {
	for p := 10000; p <= 19999; p++ {
		if _, ok := f.used[protocol][p]; ok {
			continue
		}
		if skip != nil && skip(p) {
			continue
		}
		return p, nil
	}
	return 0, repository.ErrNotFound
}

func (f *fakePortRepo) HostPortInUse(_ context.Context, protocol string, port int, excludeAppID string) (bool, error) {
	app, ok := f.used[protocol][port]
	return ok && app != excludeAppID, nil
}

// fakePortAgent answers net.loopback_listener_uid from held.
type fakePortAgent struct {
	held   map[int]bool
	err    error
	probes []map[string]any
}

func (f *fakePortAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != "net.loopback_listener_uid" {
		return nil, errors.New("unexpected command " + cmd)
	}
	m, _ := params.(map[string]any)
	f.probes = append(f.probes, m)
	if f.err != nil {
		return nil, f.err
	}
	port, _ := m["port"].(int)
	if f.held[port] {
		return json.RawMessage(`{"bound":true,"uid":0}`), nil
	}
	return json.RawMessage(`{"bound":false,"uid":-1}`), nil
}

func portHandler(repo *fakePortRepo, ag *fakePortAgent) *dockerAppHandler {
	cfg := DockerAppHandlerConfig{Repo: repo}
	if ag != nil {
		cfg.Agent = ag
	}
	return &dockerAppHandler{cfg: cfg}
}

// Gitea's shape: http on loopback and ssh on a public bind. 127.0.0.1:P and
// 0.0.0.0:P collide, so they must not share a port.
func TestResolvePorts_PortsOnDifferentBindsGetDifferentHostPorts(t *testing.T) {
	entry := dockerapp.Entry{Ports: []dockerapp.PortSpec{
		{Name: "http", ContainerPort: 3000, Protocol: "tcp", DefaultEnabled: true, DefaultBind: "loopback", DefaultReverseProxy: true},
		{Name: "ssh", ContainerPort: 22, Protocol: "tcp", DefaultBind: "public"},
	}}
	repo := &fakePortRepo{used: map[string]map[int]string{"tcp": {10000: "other"}}}
	h := portHandler(repo, &fakePortAgent{})

	rows, runtime, err := h.resolvePorts(context.Background(), entry, []installPortRequest{{Name: "ssh", Enabled: boolPtr(true)}}, "app1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].HostPort == rows[1].HostPort {
		t.Fatalf("host ports %v, want two different ones", rows)
	}
	if rows[0].HostPort == 10000 || rows[1].HostPort == 10000 {
		t.Fatalf("host ports %d/%d: 10000 is another install's", rows[0].HostPort, rows[1].HostPort)
	}
	if runtime["http"].HostPort != rows[0].HostPort || runtime["ssh"].HostPort != rows[1].HostPort {
		t.Fatalf("runtime %v does not match rows", runtime)
	}
}

// Two auto ports of one install on the same bind interface.
func TestResolvePorts_TwoAutoPortsOfOneInstallAreDistinct(t *testing.T) {
	entry := dockerapp.Entry{Ports: []dockerapp.PortSpec{
		{Name: "web", ContainerPort: 80, Protocol: "tcp", DefaultEnabled: true, DefaultBind: "loopback"},
		{Name: "admin", ContainerPort: 81, Protocol: "tcp", DefaultEnabled: true, DefaultBind: "loopback"},
	}}
	h := portHandler(&fakePortRepo{}, &fakePortAgent{})

	rows, _, err := h.resolvePorts(context.Background(), entry, nil, "app1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].HostPort == rows[1].HostPort {
		t.Fatalf("rows %v, want two different host ports", rows)
	}
}

func TestAllocateDockerHostPort_SkipsAPortInUseOnTheServer(t *testing.T) {
	ag := &fakePortAgent{held: map[int]bool{10000: true}}
	taken := map[int]bool{}

	got, err := AllocateDockerHostPort(context.Background(), &fakePortRepo{}, ag, "tcp", taken)
	if err != nil || got != 10001 {
		t.Fatalf("got %d, %v; want 10001", got, err)
	}
	if !taken[10001] {
		t.Fatal("the port given out is not marked taken")
	}
	if len(ag.probes) != 2 || ag.probes[0]["any_address"] != true || ag.probes[0]["protocol"] != "tcp" {
		t.Fatalf("probes %v, want 10000 and 10001 asked with any_address and protocol", ag.probes)
	}
}

// A port that couldn't be checked is not handed out.
func TestAllocateDockerHostPort_RefusesWhenTheServerCantBeAsked(t *testing.T) {
	ag := &fakePortAgent{err: errors.New("agent unreachable")}

	_, err := AllocateDockerHostPort(context.Background(), &fakePortRepo{}, ag, "tcp", map[int]bool{})
	if !errors.Is(err, ErrHostPortUnavailable) {
		t.Fatalf("err = %v, want ErrHostPortUnavailable", err)
	}
}

func TestAllocateDockerHostPort_SkipsReservedPorts(t *testing.T) {
	used := map[int]string{}
	for p := 10000; p < 11211; p++ {
		used[p] = "other"
	}
	ag := &fakePortAgent{}

	got, err := AllocateDockerHostPort(context.Background(), &fakePortRepo{used: map[string]map[int]string{"tcp": used}}, ag, "tcp", map[int]bool{})
	if err != nil || got != 11212 {
		t.Fatalf("got %d, %v; want 11212 (11211 is memcached's)", got, err)
	}
}

func TestAllocateDockerHostPort_GivesUpAfterManyPortsInUse(t *testing.T) {
	held := map[int]bool{}
	for p := 10000; p <= 19999; p++ {
		held[p] = true
	}
	ag := &fakePortAgent{held: held}

	_, err := AllocateDockerHostPort(context.Background(), &fakePortRepo{}, ag, "tcp", map[int]bool{})
	if !errors.Is(err, ErrHostPortUnavailable) {
		t.Fatalf("err = %v, want ErrHostPortUnavailable", err)
	}
	if len(ag.probes) != maxHostPortProbes {
		t.Fatalf("%d probes, want %d", len(ag.probes), maxHostPortProbes)
	}
}

func TestResolvePorts_PinnedPort(t *testing.T) {
	entry := dockerapp.Entry{Ports: []dockerapp.PortSpec{
		{Name: "web", ContainerPort: 80, Protocol: "tcp", DefaultEnabled: true, DefaultBind: "loopback"},
		{Name: "api", ContainerPort: 81, Protocol: "tcp", DefaultBind: "loopback"},
	}}
	pin := func(ports ...int) []installPortRequest {
		req := []installPortRequest{{Name: "web", HostPort: ports[0]}}
		if len(ports) > 1 {
			req = append(req, installPortRequest{Name: "api", Enabled: boolPtr(true), HostPort: ports[1]})
		}
		return req
	}
	for name, tc := range map[string]struct {
		used map[int]string
		held map[int]bool
		req  []installPortRequest
		want error
	}{
		"free":                 {req: pin(12000), want: nil},
		"another install's":    {used: map[int]string{12000: "other"}, req: pin(12000), want: ErrHostPortInUse},
		"this install's own":   {used: map[int]string{12000: "app1"}, req: pin(12000), want: nil},
		"in use on the server": {held: map[int]bool{12000: true}, req: pin(12000), want: ErrHostPortInUse},
		"twice in one request": {req: pin(12000, 12000), want: ErrHostPortInUse},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &fakePortRepo{used: map[string]map[int]string{"tcp": tc.used}}
			ag := &fakePortAgent{held: tc.held}
			rows, _, err := portHandler(repo, ag).resolvePorts(context.Background(), entry, tc.req, "app1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want == nil && rows[0].HostPort != tc.req[0].HostPort {
				t.Fatalf("pinned port %d, got %d", tc.req[0].HostPort, rows[0].HostPort)
			}
		})
	}

	// An auto port after a pinned one doesn't take the pinned port.
	entry.Ports[1].DefaultEnabled = true
	rows, _, err := portHandler(&fakePortRepo{}, &fakePortAgent{}).resolvePorts(context.Background(), entry, pin(10000), "app1")
	if err != nil || len(rows) != 2 || rows[1].HostPort == 10000 {
		t.Fatalf("rows %v, %v: the auto port took the pinned 10000", rows, err)
	}
}

func TestDockerPortErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrHostPortInUse, http.StatusConflict, "host_port_in_use"},
		{ErrHostPortUnavailable, http.StatusServiceUnavailable, "host_port_unavailable"},
		{errors.New("host port 22 is reserved"), http.StatusBadRequest, "port_resolution_failed"},
	} {
		status, code := dockerPortErrorStatus(tc.err)
		if status != tc.status || code != tc.code {
			t.Errorf("%v: %d %s, want %d %s", tc.err, status, code, tc.status, tc.code)
		}
	}
}
