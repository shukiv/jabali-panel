package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeLoopbackPolicyRepo struct {
	repository.UserEgressPolicyRepository
	ports []repository.LoopbackAppPort
	err   error
}

func (f *fakeLoopbackPolicyRepo) ListLoopbackAppPorts(context.Context) ([]repository.LoopbackAppPort, error) {
	return f.ports, f.err
}

func loopbackFixture(repo *fakeLoopbackPolicyRepo) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithUserEgressPolicies(repo)
	return r, ag
}

// Every egress apply carries the loopback app ports with their owners; a port
// no account owns goes without a username.
func TestApplyUserEgress_SendsLoopbackAppPorts(t *testing.T) {
	r, ag := loopbackFixture(&fakeLoopbackPolicyRepo{ports: []repository.LoopbackAppPort{
		{Port: 20001, Username: "alice"},
		{Port: 10003},
	}})
	r.applyUserEgress(context.Background(), nil, nil)

	if len(ag.calls) != 1 || ag.calls[0].method != "user.egress.apply" {
		t.Fatalf("calls = %+v, want one user.egress.apply", ag.calls)
	}
	got := ag.calls[0].params.(map[string]any)["loopback_app_ports"]
	want := []map[string]any{
		{"port": 20001, "username": "alice"},
		{"port": 10003},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loopback_app_ports = %#v, want %#v", got, want)
	}
}

// A failed read skips the apply: a payload without the ports would lift the
// protection on the host until the next tick.
func TestApplyUserEgress_LoopbackPortsReadFailureSkipsApply(t *testing.T) {
	r, ag := loopbackFixture(&fakeLoopbackPolicyRepo{err: errors.New("db down")})
	r.applyUserEgress(context.Background(), nil, nil)
	if len(ag.calls) != 0 {
		t.Fatalf("agent called %d times after a failed read, want 0", len(ag.calls))
	}
}
