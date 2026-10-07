package api

// GH #1997: a mail domain's Forwarders and Shared Folders tabs listed every
// mail domain of the account. The lists behind them take ?domain_id= now.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- an in-memory account: u1 owns domains A and B, u2 owns C ---

type dfWorld struct {
	domA, domB, domC string
	domains          map[string]*models.Domain
	mailboxes        map[string]*models.Mailbox
	fwds             []models.EmailForwarder
	shares           []models.MailboxShare
}

func newDFWorld() *dfWorld {
	w := &dfWorld{domA: ids.NewULID(), domB: ids.NewULID(), domC: ids.NewULID()}
	w.domains = map[string]*models.Domain{
		w.domA: {ID: w.domA, Name: "a.test", UserID: "u1"},
		w.domB: {ID: w.domB, Name: "b.test", UserID: "u1"},
		w.domC: {ID: w.domC, Name: "c.test", UserID: "u2"},
	}
	w.mailboxes = map[string]*models.Mailbox{
		"mbA":  {ID: "mbA", DomainID: w.domA, LocalPart: "info"},
		"mbA2": {ID: "mbA2", DomainID: w.domA, LocalPart: "sales"},
		"mbB":  {ID: "mbB", DomainID: w.domB, LocalPart: "info"},
		"mbC":  {ID: "mbC", DomainID: w.domC, LocalPart: "info"},
	}
	w.fwds = []models.EmailForwarder{
		{ID: "fA1", MailboxID: sptr("mbA"), DomainID: w.domA, Type: "alias", LocalPart: sptr("hello")},
		{ID: "fA2", MailboxID: sptr("mbA2"), DomainID: w.domA, Type: "external", Target: "x@out.test"},
		{ID: "fAimp", MailboxID: nil, DomainID: w.domA, Type: "alias", LocalPart: sptr("old"), Target: "y@out.test"},
		{ID: "fB1", MailboxID: sptr("mbB"), DomainID: w.domB, Type: "alias", LocalPart: sptr("hi")},
		{ID: "fBimp", MailboxID: nil, DomainID: w.domB, Type: "alias", LocalPart: sptr("legacy"), Target: "z@out.test"},
		{ID: "fC1", MailboxID: sptr("mbC"), DomainID: w.domC, Type: "alias", LocalPart: sptr("hey")},
	}
	w.shares = []models.MailboxShare{
		{ID: "sAA", OwnerMailboxID: "mbA", SharedWithMailboxID: "mbA2"},
		{ID: "sBB", OwnerMailboxID: "mbB", SharedWithMailboxID: "mbB"},
		{ID: "sAB", OwnerMailboxID: "mbA", SharedWithMailboxID: "mbB"},
		{ID: "sCC", OwnerMailboxID: "mbC", SharedWithMailboxID: "mbC"},
	}
	return w
}

func (w *dfWorld) userOfMailbox(id string) string {
	if mb := w.mailboxes[id]; mb != nil {
		return w.domains[mb.DomainID].UserID
	}
	return ""
}

type dfDomains struct {
	repository.DomainRepository
	w *dfWorld
}

func (f dfDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f.w.domains[id]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f dfDomains) FindByIDs(_ context.Context, list []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range list {
		if d, ok := f.w.domains[id]; ok {
			out = append(out, *d)
		}
	}
	return out, nil
}

type dfMailboxes struct {
	repository.MailboxRepository
	w *dfWorld
}

func (f dfMailboxes) FindByIDs(_ context.Context, list []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	for _, id := range list {
		if mb, ok := f.w.mailboxes[id]; ok {
			out = append(out, *mb)
		}
	}
	return out, nil
}

type dfForwarders struct {
	repository.EmailForwarderRepository
	w *dfWorld
}

func (f dfForwarders) pick(keep func(models.EmailForwarder) bool) ([]models.EmailForwarder, int64, error) {
	var out []models.EmailForwarder
	for _, r := range f.w.fwds {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out, int64(len(out)), nil
}

func (f dfForwarders) ListAll(context.Context, repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.pick(func(models.EmailForwarder) bool { return true })
}

func (f dfForwarders) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.pick(func(r models.EmailForwarder) bool {
		return r.MailboxID != nil && f.w.domains[r.DomainID].UserID == userID
	})
}

func (f dfForwarders) ListByDomainID(_ context.Context, domainID string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.pick(func(r models.EmailForwarder) bool { return r.DomainID == domainID })
}

func (f dfForwarders) ListMailboxForwardersByDomainID(_ context.Context, domainID string, _ repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	return f.pick(func(r models.EmailForwarder) bool { return r.MailboxID != nil && r.DomainID == domainID })
}

type dfShares struct {
	repository.MailboxShareRepository
	w *dfWorld
}

func (f dfShares) pick(keep func(models.MailboxShare) bool) ([]models.MailboxShare, int64, error) {
	var out []models.MailboxShare
	for _, s := range f.w.shares {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out, int64(len(out)), nil
}

func (f dfShares) ListAll(context.Context, repository.ListOptions) ([]models.MailboxShare, int64, error) {
	return f.pick(func(models.MailboxShare) bool { return true })
}

func (f dfShares) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.MailboxShare, int64, error) {
	return f.pick(func(s models.MailboxShare) bool { return f.w.userOfMailbox(s.OwnerMailboxID) == userID })
}

func (f dfShares) ListByUserAndDomainID(_ context.Context, userID, domainID string, _ repository.ListOptions) ([]models.MailboxShare, int64, error) {
	return f.pick(func(s models.MailboxShare) bool {
		if f.w.userOfMailbox(s.OwnerMailboxID) != userID {
			return false
		}
		owner, with := f.w.mailboxes[s.OwnerMailboxID], f.w.mailboxes[s.SharedWithMailboxID]
		return owner.DomainID == domainID || (with != nil && with.DomainID == domainID)
	})
}

func dfRouter(w *dfWorld, claims *auth.AccessClaims) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("", func(c *gin.Context) { ginctx.SetClaims(c, claims); c.Next() })
	RegisterMailboxForwarderRoutes(g, MailboxForwarderHandlerConfig{
		Mailboxes: dfMailboxes{w: w}, Domains: dfDomains{w: w}, Forwarders: dfForwarders{w: w},
	})
	RegisterMailboxShareRoutes(g, MailboxShareHandlerConfig{
		Mailboxes: dfMailboxes{w: w}, Domains: dfDomains{w: w}, MailboxShares: dfShares{w: w},
	})
	return r
}

func dfGet(t *testing.T, r *gin.Engine, path string) (int, []string, int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var env struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
		Error string           `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: decode %q: %v", path, rec.Body.String(), err)
	}
	var got []string
	for _, row := range env.Data {
		got = append(got, row["id"].(string))
	}
	sort.Strings(got)
	return rec.Code, got, env.Total, env.Error
}

func dfWant(t *testing.T, path string, code int, got []string, total int, want ...string) {
	t.Helper()
	sort.Strings(want)
	if code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, code)
	}
	if len(got) != len(want) {
		t.Fatalf("GET %s: rows %v, want %v", path, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GET %s: rows %v, want %v", path, got, want)
		}
	}
	if total != len(want) {
		t.Fatalf("GET %s: total %d, want %d (the rows the list shows)", path, total, len(want))
	}
}

var dfTenant = &auth.AccessClaims{UserID: "u1"}

func TestMailForwarders_DomainFilter_ListsThatDomainOnly(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, dfTenant)

	path := "/mail/forwarders?domain_id=" + w.domA
	code, got, total, _ := dfGet(t, r, path)
	dfWant(t, path, code, got, total, "fA1", "fA2")

	path = "/mail/forwarders?domain_id=" + w.domB
	code, got, total, _ = dfGet(t, r, path)
	dfWant(t, path, code, got, total, "fB1")
}

func TestMailForwardersDomainScoped_DomainFilter_ListsThatDomainOnly(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, dfTenant)

	path := "/mail/forwarders/domain-scoped?domain_id=" + w.domA
	code, got, total, _ := dfGet(t, r, path)
	dfWant(t, path, code, got, total, "fAimp")
}

func TestMailShares_DomainFilter_ListsSharesThatTouchTheDomain(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, dfTenant)

	// sAB is shared from a.test to b.test, so it shows on both pages.
	path := "/mail/shares?domain_id=" + w.domA
	code, got, total, _ := dfGet(t, r, path)
	dfWant(t, path, code, got, total, "sAA", "sAB")

	path = "/mail/shares?domain_id=" + w.domB
	code, got, total, _ = dfGet(t, r, path)
	dfWant(t, path, code, got, total, "sBB", "sAB")
}

func TestMailLists_DomainFilter_AdminGetsThatDomainOnly(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, &auth.AccessClaims{UserID: "adm", IsAdmin: true})

	path := "/mail/forwarders?domain_id=" + w.domC
	code, got, total, _ := dfGet(t, r, path)
	dfWant(t, path, code, got, total, "fC1")

	path = "/mail/shares?domain_id=" + w.domA
	code, got, total, _ = dfGet(t, r, path)
	dfWant(t, path, code, got, total, "sAA", "sAB")
}

func TestMailLists_DomainFilter_Refusals(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, dfTenant)
	unknown := ids.NewULID()

	for _, base := range []string{"/mail/forwarders", "/mail/forwarders/domain-scoped", "/mail/shares"} {
		for _, tc := range []struct {
			name, domainID string
			code           int
			errCode        string
		}{
			{"malformed", "not-a-ulid", http.StatusBadRequest, "invalid_domain_id"},
			{"empty", "", http.StatusBadRequest, "invalid_domain_id"},
			{"unknown", unknown, http.StatusNotFound, "domain_not_found"},
			{"another account's", w.domC, http.StatusForbidden, "forbidden"},
		} {
			path := base + "?domain_id=" + tc.domainID
			code, got, _, errCode := dfGet(t, r, path)
			if code != tc.code || errCode != tc.errCode {
				t.Errorf("%s (%s): status %d %q, want %d %q", path, tc.name, code, errCode, tc.code, tc.errCode)
			}
			if len(got) != 0 {
				t.Errorf("%s (%s): listed %v, want nothing", path, tc.name, got)
			}
		}
	}
}

// Guard: without the parameter the lists stay account-wide, as the mailbox
// pages and the edit-mailbox drawer rely on.
func TestMailLists_NoDomainFilter_StayAccountWide(t *testing.T) {
	w := newDFWorld()
	r := dfRouter(w, dfTenant)

	code, got, total, _ := dfGet(t, r, "/mail/forwarders")
	dfWant(t, "/mail/forwarders", code, got, total, "fA1", "fA2", "fB1")

	code, got, _, _ = dfGet(t, r, "/mail/forwarders/domain-scoped")
	if code != http.StatusOK || len(got) != 2 {
		t.Fatalf("/mail/forwarders/domain-scoped: %d %v, want fAimp and fBimp", code, got)
	}

	code, got, total, _ = dfGet(t, r, "/mail/shares")
	dfWant(t, "/mail/shares", code, got, total, "sAA", "sBB", "sAB")
}
