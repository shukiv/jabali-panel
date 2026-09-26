package domainops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-279 AC4: every delete releases the domain's reverse-proxy port. The
// port_allocations row has no foreign key to domains and nothing sweeps
// orphans, so a release that does not happen once the domain row is gone
// leaves the port reserved for good.

// ctxPorts is a PortAllocationRepository whose Release honours cancellation,
// like the real GORM-backed repository does.
type ctxPorts struct {
	repository.PortAllocationRepository
	mu       sync.Mutex
	err      error
	released []string
}

func (p *ctxPorts) Release(ctx context.Context, ownerKind, ownerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, ownerKind+"|"+ownerID)
	return nil
}

// cancellingDomains deletes the row and then cancels the caller's context,
// the way a client that disconnects mid-request cancels the REST handler's
// context after the row delete has committed.
type cancellingDomains struct {
	repository.DomainRepository
	cancel context.CancelFunc
}

func (c *cancellingDomains) Delete(context.Context, string) error {
	c.cancel()
	return nil
}

func TestDelete_ReleasesPortWhenCallerCancels(t *testing.T) {
	for _, async := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		ports := &ctxPorts{}
		d := DeleteDeps{
			Domains: &cancellingDomains{cancel: cancel},
			Ports:   ports,
			Agent:   &selectiveAgent{},
		}
		if _, err := Delete(ctx, d, "dom1", "gone.example", async); err != nil {
			t.Fatalf("async=%v: the row delete succeeded, err must be nil: %v", async, err)
		}
		ports.mu.Lock()
		got := strings.Join(ports.released, ",")
		ports.mu.Unlock()
		if want := models.PortOwnerReverseProxy + "|dom1"; got != want {
			t.Fatalf("async=%v: released %q, want %q — a cancelled caller must not leak the port", async, got, want)
		}
	}
}

func TestDelete_LogsPortReleaseFailure(t *testing.T) {
	var buf bytes.Buffer
	ports := &ctxPorts{err: errors.New("port_allocations: connection refused")}
	d := DeleteDeps{
		Domains: &stubDomainsRepo{},
		Ports:   ports,
		Agent:   &selectiveAgent{},
		Log:     slog.New(slog.NewTextHandler(&buf, nil)),
	}
	pending, err := Delete(context.Background(), d, "dom1", "gone.example", false)
	if err != nil || pending {
		t.Fatalf("a failed port release must not fail the delete (the row is gone): pending=%v err=%v", pending, err)
	}
	logged := buf.String()
	for _, want := range []string{"reverse-proxy port", "dom1", "gone.example", "connection refused"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("a failed port release must be logged with %q; log = %q", want, logged)
		}
	}
}
