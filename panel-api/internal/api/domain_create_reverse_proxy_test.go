package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-279 AC4/AC6 — the reverse-proxy port reservation on the REST create door
// (GH #1175 auto-assign, GH #1401 tenant-chosen port). Pins the wire shape each
// reservation failure maps to, the fail-open system-uid probe, and that every
// failed create after a reservation hands the port back to the pool.

// rpPorts is a PortAllocationRepository fake that records what was reserved and
// released. Release honours a cancelled ctx the way the GORM-backed repository
// does, so a compensation that runs on the request ctx is observably lost.
type rpPorts struct {
	repository.PortAllocationRepository
	specificErr error
	autoErr     error
	allocCalls  int
	held        map[string]int
	released    []string
}

func newRPPorts() *rpPorts { return &rpPorts{held: map[string]int{}} }

func (p *rpPorts) AllocateReverseProxy(_ context.Context, domainID string) (int, error) {
	p.allocCalls++
	if p.autoErr != nil {
		return 0, p.autoErr
	}
	p.held[domainID] = 30000
	return 30000, nil
}

func (p *rpPorts) AllocateReverseProxySpecific(_ context.Context, domainID string, port int) (int, error) {
	p.allocCalls++
	if p.specificErr != nil {
		return 0, p.specificErr
	}
	p.held[domainID] = port
	return port, nil
}

func (p *rpPorts) Release(ctx context.Context, ownerKind, ownerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ownerKind != models.PortOwnerReverseProxy {
		return errors.New("unexpected owner kind " + ownerKind)
	}
	delete(p.held, ownerID)
	p.released = append(p.released, ownerID)
	return nil
}

// rpAgent answers net.loopback_listener_uid with a canned payload.
type rpAgent struct {
	resp  string
	err   error
	calls []string
}

func (a *rpAgent) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, command)
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(a.resp), nil
}

// rpDomains adds the preview-enabled listing (for the preview-slug check) and
// a Create hook to the shared dcDomains fake.
type rpDomains struct {
	*dcDomains
	others   []models.Domain
	onCreate func()
}

func (r *rpDomains) ListPreviewEnabled(context.Context) ([]models.Domain, error) {
	var out []models.Domain
	for _, d := range r.others {
		if d.TempURLEnabled {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *rpDomains) Create(ctx context.Context, d *models.Domain) error {
	if r.onCreate != nil {
		r.onCreate()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return r.dcDomains.Create(ctx, d)
}

func TestCreateDomainOp_ReverseProxyReservation(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	type env struct {
		h     *domainHandler
		dom   *rpDomains
		ports *rpPorts
		ag    *rpAgent
	}
	newEnv := func() env {
		dom := &rpDomains{dcDomains: newDCDomains()}
		ports := newRPPorts()
		ag := &rpAgent{resp: `{"bound":false,"uid":-1}`}
		h := &domainHandler{cfg: DomainHandlerConfig{
			Users: newAbUsers(owner), Domains: dom, PortAllocations: ports, Agent: ag,
		}}
		return env{h: h, dom: dom, ports: ports, ag: ag}
	}
	in := func(port uint32) createDomainInput {
		return createDomainInput{
			OwnerID: owner.ID, Name: "app.example.com",
			MailProvider: models.MailProviderNone, ReverseProxy: true, ReverseProxyPort: port,
		}
	}
	wantErr := func(t *testing.T, oerr *createDomainError, status int, code, detail string) {
		t.Helper()
		if oerr == nil {
			t.Fatalf("want %d %s, got success", status, code)
		}
		if oerr.Status != status || oerr.Code != code || oerr.Detail != detail {
			t.Fatalf("want {%d %q %q}, got {%d %q %q}", status, code, detail, oerr.Status, oerr.Code, oerr.Detail)
		}
	}

	t.Run("no port pool configured → 503 reverse_proxy_unavailable", func(t *testing.T) {
		e := newEnv()
		e.h.cfg.PortAllocations = nil
		_, oerr := createDomainOp(context.Background(), e.h, in(0))
		wantErr(t, oerr, http.StatusServiceUnavailable, "reverse_proxy_unavailable",
			"reverse-proxy domains are not enabled on this host")
		if len(e.dom.created) != 0 {
			t.Fatal("no domain may be persisted without a port pool")
		}
	})

	t.Run("auto-assign stores the pool port on the row", func(t *testing.T) {
		e := newEnv()
		d, oerr := createDomainOp(context.Background(), e.h, in(0))
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.ReverseProxyPort != 30000 || e.ports.held[d.ID] != 30000 {
			t.Fatalf("want port 30000 stored and held, got row=%d held=%v", d.ReverseProxyPort, e.ports.held)
		}
		if len(e.ag.calls) != 0 {
			t.Fatalf("auto-assign must not probe the agent, got %v", e.ag.calls)
		}
	})

	t.Run("denylisted port → 400 reverse_proxy_port_invalid with the validator's reason, nothing reserved", func(t *testing.T) {
		e := newEnv()
		verr := repository.ValidateReverseProxyPort(8443)
		if verr == nil {
			t.Fatal("precondition: 8443 must be on the infra denylist")
		}
		_, oerr := createDomainOp(context.Background(), e.h, in(8443))
		wantErr(t, oerr, http.StatusBadRequest, "reverse_proxy_port_invalid", verr.Error())
		if e.ports.allocCalls != 0 || len(e.ag.calls) != 0 {
			t.Fatalf("an invalid port must be refused before the probe and the pool, allocs=%d probes=%v", e.ports.allocCalls, e.ag.calls)
		}
	})

	t.Run("port bound by a system uid → 409 reverse_proxy_port_system_bound, nothing reserved", func(t *testing.T) {
		e := newEnv()
		e.ag.resp = `{"bound":true,"uid":33}`
		_, oerr := createDomainOp(context.Background(), e.h, in(5000))
		wantErr(t, oerr, http.StatusConflict, "reverse_proxy_port_system_bound",
			"that port is already in use by a system service — choose another")
		if e.ports.allocCalls != 0 || len(e.dom.created) != 0 {
			t.Fatalf("a system-bound port must not be reserved or persisted, allocs=%d created=%d", e.ports.allocCalls, len(e.dom.created))
		}
	})

	t.Run("port bound by a tenant uid (the floor itself) is allowed", func(t *testing.T) {
		e := newEnv()
		e.ag.resp = `{"bound":true,"uid":1000}`
		d, oerr := createDomainOp(context.Background(), e.h, in(5000))
		if oerr != nil {
			t.Fatalf("a tenant-owned listener must pass, got %v", oerr)
		}
		if d.ReverseProxyPort != 5000 {
			t.Fatalf("want the chosen port stored, got %d", d.ReverseProxyPort)
		}
	})

	t.Run("probe failure fails open to the constant denylist", func(t *testing.T) {
		e := newEnv()
		e.ag.err = errors.New("agent down")
		d, oerr := createDomainOp(context.Background(), e.h, in(5000))
		if oerr != nil {
			t.Fatalf("agent trouble must not fail the create, got %v", oerr)
		}
		if d.ReverseProxyPort != 5000 {
			t.Fatalf("want the chosen port stored, got %d", d.ReverseProxyPort)
		}
	})

	t.Run("port held by another domain → 409 reverse_proxy_port_in_use", func(t *testing.T) {
		e := newEnv()
		e.ports.specificErr = repository.ErrPortInUse
		_, oerr := createDomainOp(context.Background(), e.h, in(5000))
		wantErr(t, oerr, http.StatusConflict, "reverse_proxy_port_in_use",
			"that port is already assigned to another domain — choose another")
	})

	t.Run("pool exhausted → 503 reverse_proxy_port_unavailable", func(t *testing.T) {
		e := newEnv()
		e.ports.autoErr = repository.ErrPortPoolExhausted
		_, oerr := createDomainOp(context.Background(), e.h, in(0))
		wantErr(t, oerr, http.StatusServiceUnavailable, "reverse_proxy_port_unavailable",
			"no free reverse-proxy port available; contact the administrator")
	})

	t.Run("insert failure releases the reserved port", func(t *testing.T) {
		e := newEnv()
		e.dom.createErr = errors.New("db down")
		_, oerr := createDomainOp(context.Background(), e.h, in(0))
		wantErr(t, oerr, http.StatusInternalServerError, "internal", "")
		if len(e.ports.held) != 0 || len(e.ports.released) != 1 {
			t.Fatalf("the reservation must be released, held=%v released=%v", e.ports.held, e.ports.released)
		}
	})

	t.Run("duplicate name releases the reserved port → 409 domain_already_exists", func(t *testing.T) {
		e := newEnv()
		e.dom.createErr = repository.ErrConflict
		_, oerr := createDomainOp(context.Background(), e.h, in(0))
		wantErr(t, oerr, http.StatusConflict, "domain_already_exists", "")
		if len(e.ports.held) != 0 {
			t.Fatalf("the reservation must be released, held=%v", e.ports.held)
		}
	})

	t.Run("client disconnect during the insert still releases the reserved port", func(t *testing.T) {
		e := newEnv()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.dom.onCreate = cancel
		_, oerr := createDomainOp(ctx, e.h, in(0))
		if oerr == nil {
			t.Fatal("a cancelled insert must fail the create")
		}
		if len(e.ports.held) != 0 {
			t.Fatalf("a failed create must not leak its port reservation, held=%v", e.ports.held)
		}
	})

	t.Run("preview-slug collision releases the reserved port", func(t *testing.T) {
		e := newEnv()
		e.dom.others = []models.Domain{{ID: "other", Name: "app-example.com", TempURLEnabled: true}}
		req := in(0)
		req.TempURLEnabled = true
		_, oerr := createDomainOp(context.Background(), e.h, req)
		wantErr(t, oerr, http.StatusConflict, "temp_url_slug_conflict", "preview URL would collide with app-example.com")
		if len(e.ports.held) != 0 {
			t.Fatalf("the reservation must be released, held=%v", e.ports.held)
		}
	})
}
