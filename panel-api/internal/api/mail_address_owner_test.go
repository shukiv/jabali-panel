package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Stalwart keeps every alias it has seen on the account that had it. A new
// mailbox at such an address signed in to that account, and a moved or
// deleted alias kept delivering to it.

type recAddressReleaser struct {
	released []string // "<address>" or "<address>→<owner>"
	err      error
}

func (r *recAddressReleaser) ReleaseAddress(_ context.Context, address string) error {
	r.released = append(r.released, address)
	return r.err
}

func (r *recAddressReleaser) ReleaseTo(_ context.Context, address, owner string) error {
	r.released = append(r.released, address+"→"+owner)
	return r.err
}

type addrMailboxRepo struct {
	saMailboxRepo
	created   bool
	createErr error
}

func (r *addrMailboxRepo) ExistsByDomainAndLocalPart(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *addrMailboxRepo) Create(context.Context, *models.Mailbox) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = true
	return nil
}

func addrMailboxRouter(repo *addrMailboxRepo, rel MailAddressReleaser) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	cfg := MailboxHandlerConfig{
		Mailboxes: repo,
		Domains: &saDomainRepo{byID: map[string]*models.Domain{
			"dom1": {ID: "dom1", Name: "x.test", UserID: "u1", EmailEnabled: true, OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified}},
		}},
	}
	if rel != nil {
		cfg.Addresses = rel
	}
	RegisterMailboxRoutes(r.Group(""), cfg)
	return r
}

func TestMailboxCreate_ReleasesTheAddressFirst(t *testing.T) {
	repo := &addrMailboxRepo{}
	rel := &recAddressReleaser{}
	code, body := pwBoundsCall(t, addrMailboxRouter(repo, rel), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"Sales"}`)
	if code != http.StatusCreated {
		t.Fatalf("status %d body %v, want 201", code, body)
	}
	if len(rel.released) != 1 || rel.released[0] != "sales@x.test" {
		t.Fatalf("released %v, want sales@x.test", rel.released)
	}
}

// No mail server client, or the mail server cannot be reached: the mailbox
// is not created, and the caller is told to retry.
func TestMailboxCreate_MailServerUnavailableIs503(t *testing.T) {
	for name, rel := range map[string]MailAddressReleaser{
		"not wired":   nil,
		"unreachable": &recAddressReleaser{err: errors.New("connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &addrMailboxRepo{}
			code, body := pwBoundsCall(t, addrMailboxRouter(repo, rel), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"sales"}`)
			if code != http.StatusServiceUnavailable || body["error"] != "mail_server_unavailable" {
				t.Fatalf("status %d body %v, want 503 mail_server_unavailable", code, body)
			}
			if repo.created {
				t.Fatal("the mailbox was created")
			}
		})
	}
}

// The database refuses a mailbox at an alias's, group's or shared resource's
// address (migration 000306).
func TestMailboxCreate_AddressInUseIs409(t *testing.T) {
	repo := &addrMailboxRepo{createErr: repository.ErrAddressInUse}
	code, body := pwBoundsCall(t, addrMailboxRouter(repo, &recAddressReleaser{}), http.MethodPost, "/domains/dom1/mailboxes", `{"local_part":"sales"}`)
	if code != http.StatusConflict || body["error"] != "address_in_use" {
		t.Fatalf("status %d body %v, want 409 address_in_use", code, body)
	}
}

type addrForwarders struct {
	fwFakeForwarders
	createErr error
	byID      map[string]*models.EmailForwarder
	deleted   []string
}

func (f *addrForwarders) Create(ctx context.Context, row *models.EmailForwarder) error {
	if f.createErr != nil {
		return f.createErr
	}
	return f.fwFakeForwarders.Create(ctx, row)
}

func (f *addrForwarders) FindByID(_ context.Context, id string) (*models.EmailForwarder, error) {
	if row, ok := f.byID[id]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}

func (f *addrForwarders) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func addrForwarderHandler(fw *addrForwarders, rel MailAddressReleaser) *forwarderHandler {
	h := newForwarderHandlerFake(nil)
	h.cfg.Forwarders = fw
	h.cfg.Addresses = rel
	return h
}

// An alias that moved here from another mailbox comes off that mailbox's
// account, and only off accounts other than this mailbox's.
func TestForwarderCreate_AliasMovesToItsNewMailbox(t *testing.T) {
	rel := &recAddressReleaser{}
	w := postForwarder(addrForwarderHandler(&addrForwarders{}, rel), `{"type":"alias","local_part":"sales"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	if len(rel.released) != 1 || rel.released[0] != "sales@example.com→joe@example.com" {
		t.Fatalf("released %v, want sales@example.com kept only for joe@example.com", rel.released)
	}

	// An external forward has no address of its own.
	rel = &recAddressReleaser{}
	postForwarder(addrForwarderHandler(&addrForwarders{}, rel), `{"type":"external","target":"out@elsewhere.com"}`)
	if len(rel.released) != 0 {
		t.Fatalf("an external forward released %v", rel.released)
	}
}

func TestForwarderCreate_AliasAtAMailboxAddressIs409(t *testing.T) {
	w := postForwarder(addrForwarderHandler(&addrForwarders{createErr: repository.ErrAddressInUse}, &recAddressReleaser{}),
		`{"type":"alias","local_part":"info"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"address_in_use"`) {
		t.Fatalf("status = %d body %s, want 409 address_in_use", w.Code, w.Body.String())
	}
}

// A deleted alias comes off the account that had it.
func TestForwarderDelete_AliasComesOffTheMailServer(t *testing.T) {
	lp := "sales"
	mbID := "mb1"
	fw := &addrForwarders{byID: map[string]*models.EmailForwarder{
		"f1": {ID: "f1", MailboxID: &mbID, DomainID: "dom1", Type: "alias", LocalPart: &lp},
	}}
	rel := &recAddressReleaser{}
	h := addrForwarderHandler(fw, rel)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "f1"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/forwarders/f1", nil)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
	h.del(c)

	if len(fw.deleted) != 1 {
		t.Fatalf("status %d: the forwarder was not deleted", w.Code)
	}
	if len(rel.released) != 1 || rel.released[0] != "sales@example.com" {
		t.Fatalf("released %v, want sales@example.com", rel.released)
	}
}
