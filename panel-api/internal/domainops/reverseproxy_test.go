package domainops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakePorts records reservations and releases. Release honours a cancelled ctx
// the way the GORM-backed repository does.
type fakePorts struct {
	repository.PortAllocationRepository
	autoErr, specificErr error
	allocCalls           int
	held                 map[string]int
	releaseKinds         []string
}

func newFakePorts() *fakePorts { return &fakePorts{held: map[string]int{}} }

func (p *fakePorts) AllocateReverseProxy(_ context.Context, id string) (int, error) {
	p.allocCalls++
	if p.autoErr != nil {
		return 0, p.autoErr
	}
	p.held[id] = 30000
	return 30000, nil
}

func (p *fakePorts) AllocateReverseProxySpecific(_ context.Context, id string, port int) (int, error) {
	p.allocCalls++
	if p.specificErr != nil {
		return 0, p.specificErr
	}
	p.held[id] = port
	return port, nil
}

func (p *fakePorts) Release(ctx context.Context, kind, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.releaseKinds = append(p.releaseKinds, kind)
	delete(p.held, id)
	return nil
}

type fakeProbe struct {
	resp  string
	err   error
	calls []string
	ports []any
}

func (a *fakeProbe) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	a.ports = append(a.ports, params.(map[string]any)["port"])
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(a.resp), nil
}

func TestReserveReverseProxyPort(t *testing.T) {
	ctx := context.Background()

	t.Run("nil pool → ErrReverseProxyUnavailable", func(t *testing.T) {
		_, err := ReserveReverseProxyPort(ctx, PortDeps{}, "d1", 0)
		if !errors.Is(err, ErrReverseProxyUnavailable) {
			t.Fatalf("want ErrReverseProxyUnavailable, got %v", err)
		}
	})

	t.Run("auto-assign draws from the pool without probing", func(t *testing.T) {
		p, ag := newFakePorts(), &fakeProbe{resp: `{"bound":true,"uid":0}`}
		port, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p, Agent: ag}, "d1", 0)
		if err != nil || port != 30000 || p.held["d1"] != 30000 {
			t.Fatalf("want 30000 held, got port=%d err=%v held=%v", port, err, p.held)
		}
		if len(ag.calls) != 0 {
			t.Fatalf("auto-assign must not probe, got %v", ag.calls)
		}
	})

	t.Run("pool exhausted → ErrReverseProxyPortUnavailable wrapping the store error", func(t *testing.T) {
		p := newFakePorts()
		p.autoErr = repository.ErrPortPoolExhausted
		_, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p}, "d1", 0)
		if !errors.Is(err, ErrReverseProxyPortUnavailable) || !errors.Is(err, repository.ErrPortPoolExhausted) {
			t.Fatalf("want Unavailable wrapping ErrPortPoolExhausted, got %v", err)
		}
	})

	t.Run("static-policy rejection carries the validator's reason verbatim", func(t *testing.T) {
		p, ag := newFakePorts(), &fakeProbe{resp: `{"bound":false}`}
		for _, port := range []int{80, 8443, 30001, 40050, 70000} {
			verr := repository.ValidateReverseProxyPort(port)
			if verr == nil {
				t.Fatalf("precondition: port %d must fail the static policy", port)
			}
			_, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p, Agent: ag}, "d1", port)
			if !errors.Is(err, ErrReverseProxyPortInvalid) {
				t.Fatalf("port %d: want ErrReverseProxyPortInvalid, got %v", port, err)
			}
			var ipe *InvalidPortError
			if !errors.As(err, &ipe) || err.Error() != verr.Error() {
				t.Fatalf("port %d: want InvalidPortError with %q, got %v", port, verr, err)
			}
		}
		if p.allocCalls != 0 || len(ag.calls) != 0 {
			t.Fatalf("a rejected port must reach neither the probe nor the pool, allocs=%d probes=%v", p.allocCalls, ag.calls)
		}
	})

	t.Run("system uid below the floor is refused before the pool", func(t *testing.T) {
		for _, uid := range []int{0, 33, ReverseProxyTenantUIDFloor - 1} {
			p := newFakePorts()
			ag := &fakeProbe{resp: `{"bound":true,"uid":` + itoa(uid) + `}`}
			_, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p, Agent: ag}, "d1", 5000)
			if !errors.Is(err, ErrReverseProxyPortSystemBound) {
				t.Fatalf("uid %d: want ErrReverseProxyPortSystemBound, got %v", uid, err)
			}
			if p.allocCalls != 0 {
				t.Fatalf("uid %d: a system-bound port must not be reserved", uid)
			}
			if len(ag.calls) != 1 || ag.calls[0] != "net.loopback_listener_uid" || ag.ports[0] != 5000 {
				t.Fatalf("uid %d: want one probe for port 5000, got %v %v", uid, ag.calls, ag.ports)
			}
		}
	})

	t.Run("tenant uid, unbound port, or probe trouble all proceed (fail open)", func(t *testing.T) {
		cases := map[string]*fakeProbe{
			"tenant uid at the floor": {resp: `{"bound":true,"uid":1000}`},
			"not bound":               {resp: `{"bound":false,"uid":-1}`},
			"bound, uid unknown":      {resp: `{"bound":true,"uid":-1}`},
			"agent error":             {err: errors.New("agent down")},
			"garbage payload":         {resp: `not json`},
		}
		for name, ag := range cases {
			p := newFakePorts()
			port, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p, Agent: ag}, "d1", 5000)
			if err != nil || port != 5000 || p.held["d1"] != 5000 {
				t.Fatalf("%s: want 5000 reserved, got port=%d err=%v", name, port, err)
			}
		}
		p := newFakePorts()
		if port, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p}, "d1", 5000); err != nil || port != 5000 {
			t.Fatalf("nil agent: want 5000 reserved, got port=%d err=%v", port, err)
		}
	})

	t.Run("port held elsewhere → ErrReverseProxyPortInUse; other store errors → Unavailable", func(t *testing.T) {
		p := newFakePorts()
		p.specificErr = repository.ErrPortInUse
		_, err := ReserveReverseProxyPort(ctx, PortDeps{Ports: p}, "d1", 5000)
		if !errors.Is(err, ErrReverseProxyPortInUse) || !errors.Is(err, repository.ErrPortInUse) {
			t.Fatalf("want InUse wrapping ErrPortInUse, got %v", err)
		}
		p.specificErr = errors.New("db down")
		_, err = ReserveReverseProxyPort(ctx, PortDeps{Ports: p}, "d1", 5000)
		if !errors.Is(err, ErrReverseProxyPortUnavailable) || errors.Is(err, ErrReverseProxyPortInUse) {
			t.Fatalf("want Unavailable (not InUse), got %v", err)
		}
	})
}

func TestReleaseReverseProxyPort_SurvivesCancelledContext(t *testing.T) {
	p := newFakePorts()
	p.held["d1"] = 30000
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ReleaseReverseProxyPort(ctx, p, "d1"); err != nil {
		t.Fatalf("release must run despite a cancelled caller ctx, got %v", err)
	}
	if len(p.held) != 0 || len(p.releaseKinds) != 1 || p.releaseKinds[0] != models.PortOwnerReverseProxy {
		t.Fatalf("want the reverse_proxy reservation released, held=%v kinds=%v", p.held, p.releaseKinds)
	}
	if err := ReleaseReverseProxyPort(context.Background(), nil, "d1"); err != nil {
		t.Fatalf("nil pool must be a no-op, got %v", err)
	}
}

type fakeCreator struct {
	err     error
	cancel  context.CancelFunc
	created []*models.Domain
}

func (c *fakeCreator) Create(ctx context.Context, d *models.Domain) error {
	if c.cancel != nil {
		c.cancel()
		return ctx.Err()
	}
	if c.err != nil {
		return c.err
	}
	c.created = append(c.created, d)
	return nil
}

func TestPersistDomain(t *testing.T) {
	t.Run("success keeps the reservation", func(t *testing.T) {
		p := newFakePorts()
		p.held["d1"] = 30000
		c := &fakeCreator{}
		if err := PersistDomain(context.Background(), c, p, &models.Domain{ID: "d1", ReverseProxyPort: 30000}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(c.created) != 1 || p.held["d1"] != 30000 {
			t.Fatalf("want row created and port kept, created=%d held=%v", len(c.created), p.held)
		}
	})

	t.Run("conflict → ErrDomainExists and the port is released", func(t *testing.T) {
		p := newFakePorts()
		p.held["d1"] = 30000
		err := PersistDomain(context.Background(), &fakeCreator{err: repository.ErrConflict}, p, &models.Domain{ID: "d1", ReverseProxyPort: 30000})
		if !errors.Is(err, ErrDomainExists) || !errors.Is(err, repository.ErrConflict) || errors.Is(err, ErrPersist) {
			t.Fatalf("want ErrDomainExists wrapping ErrConflict, got %v", err)
		}
		if len(p.held) != 0 {
			t.Fatalf("want the port released, held=%v", p.held)
		}
	})

	t.Run("other failure → ErrPersist and the port is released", func(t *testing.T) {
		p := newFakePorts()
		p.held["d1"] = 30000
		err := PersistDomain(context.Background(), &fakeCreator{err: errors.New("db down")}, p, &models.Domain{ID: "d1", ReverseProxyPort: 30000})
		if !errors.Is(err, ErrPersist) || errors.Is(err, ErrDomainExists) {
			t.Fatalf("want ErrPersist, got %v", err)
		}
		if len(p.held) != 0 {
			t.Fatalf("want the port released, held=%v", p.held)
		}
	})

	t.Run("insert cancelled by the caller still releases the port", func(t *testing.T) {
		p := newFakePorts()
		p.held["d1"] = 30000
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := PersistDomain(ctx, &fakeCreator{cancel: cancel}, p, &models.Domain{ID: "d1", ReverseProxyPort: 30000})
		if !errors.Is(err, ErrPersist) {
			t.Fatalf("want ErrPersist, got %v", err)
		}
		if len(p.held) != 0 {
			t.Fatalf("a cancelled insert must not leak the port, held=%v", p.held)
		}
	})

	t.Run("a non-proxy domain touches no port", func(t *testing.T) {
		p := newFakePorts()
		_ = PersistDomain(context.Background(), &fakeCreator{err: errors.New("db down")}, p, &models.Domain{ID: "d1"})
		if len(p.releaseKinds) != 0 {
			t.Fatalf("no release expected for a domain without a port, got %v", p.releaseKinds)
		}
	})
}

// The CLI prints these errors; they must read as the store error alone, with
// no "domainops:" sentinel text, exactly as before the module existed.
func TestWrappedErrorsPrintTheStoreErrorOnly(t *testing.T) {
	p := newFakePorts()
	p.autoErr = repository.ErrPortPoolExhausted
	_, err := ReserveReverseProxyPort(context.Background(), PortDeps{Ports: p}, "d1", 0)
	if err == nil || err.Error() != repository.ErrPortPoolExhausted.Error() {
		t.Fatalf("pool exhausted: want %q, got %v", repository.ErrPortPoolExhausted, err)
	}
	storeErr := errors.New("Error 1205: Lock wait timeout exceeded")
	err = PersistDomain(context.Background(), &fakeCreator{err: storeErr}, p, &models.Domain{ID: "d1"})
	if err == nil || err.Error() != storeErr.Error() {
		t.Fatalf("persist: want %q, got %v", storeErr, err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
