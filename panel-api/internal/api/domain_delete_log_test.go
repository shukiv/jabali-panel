package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-279 AC4: a domain delete releases the domain's reverse-proxy port. The
// row is the port's last handle (port_allocations has no foreign key and no
// orphan sweep), so when the release fails the only trace is the warning the
// shared delete path logs. The REST door must hand that path a logger, or the
// stranded port is dropped without a word.

type delLogDomains struct {
	repository.DomainRepository
	d       *models.Domain
	deleted []string
}

func (r *delLogDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if r.d == nil || r.d.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.d, nil
}

func (r *delLogDomains) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

type failingReleasePorts struct {
	repository.PortAllocationRepository
}

func (failingReleasePorts) Release(context.Context, string, string) error {
	return errors.New("db down")
}

// lockedBuffer is written by the async host-teardown goroutine too.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDomainDelete_PortReleaseFailureIsLogged(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dom := &delLogDomains{d: &models.Domain{ID: "d1", UserID: "u1", Name: "app.example.com", DNSDisabled: true}}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dom, PortAllocations: failingReleasePorts{}}}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})
	r.DELETE("/domains/:id", h.delete)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/domains/d1", nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("a failed port release must not fail the delete, got %d %s", w.Code, w.Body.String())
	}
	if len(dom.deleted) != 1 || dom.deleted[0] != "d1" {
		t.Fatalf("want the row deleted, got %v", dom.deleted)
	}
	out := logs.String()
	if !strings.Contains(out, "reverse-proxy port release failed") || !strings.Contains(out, "domain_id=d1") {
		t.Fatalf("the stranded port must be logged with the domain id, got logs:\n%s", out)
	}
}
