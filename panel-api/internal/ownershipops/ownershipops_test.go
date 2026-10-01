package ownershipops

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// memStore is an in-memory DomainOwnershipRepository with the same
// conditional semantics as the SQL one, plus the domain and alias readers.
type memStore struct {
	repository.DomainOwnershipRepository
	domains map[string]*models.Domain
	aliases map[string]*models.WebDomainAlias
	// stalePending, when set, is what ListPendingDomains returns (a snapshot
	// taken before a concurrent write).
	stalePending []models.Domain
	deleted      []string
}

func newMem() *memStore {
	return &memStore{domains: map[string]*models.Domain{}, aliases: map[string]*models.WebDomainAlias{}}
}

func (m *memStore) addDomain(d *models.Domain)        { m.domains[d.ID] = d }
func (m *memStore) addAlias(a *models.WebDomainAlias) { m.aliases[a.ID] = a }

func (m *memStore) FindByID(_ context.Context, id string) (*models.Domain, error) {
	d, ok := m.domains[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (m *memStore) FindByName(_ context.Context, name string) (*models.Domain, error) {
	for _, d := range m.domains {
		if strings.EqualFold(d.Name, name) {
			c := *d
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

type memAliases struct{ m *memStore }

func (a memAliases) FindByID(_ context.Context, id string) (*models.WebDomainAlias, error) {
	r, ok := a.m.aliases[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	c := *r
	return &c, nil
}

func markVerified(st *models.OwnershipState, method, expectToken string, at time.Time) bool {
	if st.OwnershipStatus != models.OwnershipPending || (expectToken != "" && st.OwnershipToken != expectToken) {
		return false
	}
	st.OwnershipStatus = models.OwnershipVerified
	st.OwnershipMethod = method
	st.OwnershipVerifiedAt = &at
	st.OwnershipLastResult = models.OwnershipResultVerified
	st.OwnershipNextCheckAt = nil
	return true
}

func markPending(st *models.OwnershipState, token string, at time.Time, fromVerified, clearVerifiedAt bool) bool {
	if fromVerified && st.OwnershipStatus != models.OwnershipVerified {
		return false
	}
	st.OwnershipStatus = models.OwnershipPending
	st.OwnershipMethod = ""
	st.OwnershipToken = token
	st.OwnershipPendingSince = &at
	st.OwnershipNextCheckAt = &at
	st.OwnershipLastResult = ""
	if clearVerifiedAt {
		st.OwnershipVerifiedAt = nil
	}
	return true
}

func recordCheck(st *models.OwnershipState, expectToken, result string, checked, next time.Time) bool {
	if st.OwnershipStatus != models.OwnershipPending || st.OwnershipToken != expectToken {
		return false
	}
	st.OwnershipCheckedAt = &checked
	st.OwnershipNextCheckAt = &next
	st.OwnershipLastResult = result
	return true
}

func (m *memStore) MarkDomainVerified(_ context.Context, id, method, expectToken string, at time.Time) (bool, error) {
	d, ok := m.domains[id]
	return ok && markVerified(&d.OwnershipState, method, expectToken, at), nil
}

func (m *memStore) MarkDomainPending(_ context.Context, id, token string, at time.Time, fromVerified, clear bool) (bool, error) {
	d, ok := m.domains[id]
	return ok && markPending(&d.OwnershipState, token, at, fromVerified, clear), nil
}

func (m *memStore) RecordDomainCheck(_ context.Context, id, expectToken, result string, checked, next time.Time) (bool, error) {
	d, ok := m.domains[id]
	return ok && recordCheck(&d.OwnershipState, expectToken, result, checked, next), nil
}

func (m *memStore) EnsureDomainToken(_ context.Context, id, token string) (bool, error) {
	d, ok := m.domains[id]
	if !ok || d.OwnershipToken != "" {
		return false, nil
	}
	d.OwnershipToken = token
	return true, nil
}

func (m *memStore) MarkDomainExpiryNotified(_ context.Context, id string, at time.Time) error {
	m.domains[id].OwnershipExpiryNotifiedAt = &at
	return nil
}

// sortedDomains returns copies ordered by name, as the SQL listings are.
func (m *memStore) sortedDomains() []models.Domain {
	var out []models.Domain
	for _, d := range m.domains {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *memStore) ListDomainsDue(_ context.Context, now time.Time, _ int) ([]models.Domain, error) {
	var out []models.Domain
	for _, d := range m.sortedDomains() {
		if d.OwnershipStatus == models.OwnershipPending && (d.OwnershipNextCheckAt == nil || !d.OwnershipNextCheckAt.After(now)) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memStore) ListPendingDomains(context.Context) ([]models.Domain, error) {
	if m.stalePending != nil {
		return m.stalePending, nil
	}
	var out []models.Domain
	for _, d := range m.sortedDomains() {
		if d.OwnershipStatus != models.OwnershipVerified {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memStore) ListParentProvenUnder(_ context.Context, name string) ([]models.Domain, error) {
	var out []models.Domain
	for _, d := range m.sortedDomains() {
		if d.OwnershipStatus == models.OwnershipVerified && d.OwnershipMethod == models.OwnershipMethodParent && under(d.Name, name) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memStore) DeleteExpiredDomain(_ context.Context, id string, cutoff time.Time) error {
	d, ok := m.domains[id]
	if !ok || d.OwnershipStatus != models.OwnershipPending || d.OwnershipVerifiedAt != nil ||
		d.OwnershipPendingSince == nil || d.OwnershipPendingSince.After(cutoff) ||
		d.IsPanelPrimary || d.ManagedBy == models.DomainManagedByDockerApp {
		return repository.ErrOwnershipChanged
	}
	delete(m.domains, id)
	m.deleted = append(m.deleted, id)
	return nil
}

func (m *memStore) sortedAliases() []models.WebDomainAlias {
	var out []models.WebDomainAlias
	for _, a := range m.aliases {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}

func (m *memStore) MarkAliasVerified(_ context.Context, id, method, expectToken string, at time.Time) (bool, error) {
	a, ok := m.aliases[id]
	return ok && markVerified(&a.OwnershipState, method, expectToken, at), nil
}

func (m *memStore) MarkAliasPending(_ context.Context, id, token string, at time.Time, fromVerified, clear bool) (bool, error) {
	a, ok := m.aliases[id]
	return ok && markPending(&a.OwnershipState, token, at, fromVerified, clear), nil
}

func (m *memStore) RecordAliasCheck(_ context.Context, id, expectToken, result string, checked, next time.Time) (bool, error) {
	a, ok := m.aliases[id]
	return ok && recordCheck(&a.OwnershipState, expectToken, result, checked, next), nil
}

func (m *memStore) MarkAliasExpiryNotified(_ context.Context, id string, at time.Time) error {
	m.aliases[id].OwnershipExpiryNotifiedAt = &at
	return nil
}

func (m *memStore) ListAliasesDue(context.Context, time.Time, int) ([]models.WebDomainAlias, error) {
	return nil, nil
}

func (m *memStore) ListPendingAliases(context.Context) ([]models.WebDomainAlias, error) {
	var out []models.WebDomainAlias
	for _, a := range m.sortedAliases() {
		if a.OwnershipStatus != models.OwnershipVerified {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memStore) ListParentProvenAliasesUnder(_ context.Context, name string) ([]models.WebDomainAlias, error) {
	var out []models.WebDomainAlias
	for _, a := range m.sortedAliases() {
		if a.OwnershipStatus == models.OwnershipVerified && a.OwnershipMethod == models.OwnershipMethodParent && under(a.Hostname, name) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memStore) DeleteExpiredAlias(_ context.Context, id string, cutoff time.Time) error {
	a, ok := m.aliases[id]
	if !ok || a.OwnershipStatus != models.OwnershipPending || a.OwnershipVerifiedAt != nil ||
		a.OwnershipPendingSince == nil || a.OwnershipPendingSince.After(cutoff) {
		return repository.ErrOwnershipChanged
	}
	delete(m.aliases, id)
	m.deleted = append(m.deleted, id)
	return nil
}

type recNotify struct{ envs []notifications.Envelope }

func (r *recNotify) Publish(_ context.Context, env notifications.Envelope) (string, error) {
	r.envs = append(r.envs, env)
	return "1", nil
}

func (r *recNotify) kinds(userID string) []string {
	var out []string
	for _, e := range r.envs {
		if e.UserID == userID {
			out = append(out, e.EventKind)
		}
	}
	return out
}

type settingsStub struct {
	st  *models.ServerSettings
	err error
}

func (s settingsStub) Get(context.Context) (*models.ServerSettings, error) { return s.st, s.err }

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func pendingDomain(id, name, owner, token string, since time.Duration) *models.Domain {
	return &models.Domain{ID: id, Name: name, UserID: owner, ManagedBy: models.DomainManagedByTenant,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: token,
			OwnershipPendingSince: ago(since)}}
}

func verifiedDomain(id, name, owner, method string) *models.Domain {
	return &models.Domain{ID: id, Name: name, UserID: owner, ManagedBy: models.DomainManagedByTenant,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified, OwnershipMethod: method,
			OwnershipToken: "old-" + id, OwnershipVerifiedAt: ago(48 * time.Hour)}}
}

// txtAll answers each challenge name in values on all three resolvers.
func txtAll(values map[string]string) domainops.OwnershipLookups {
	return domainops.OwnershipLookups{
		TXT: func(_ context.Context, name string) []dnsverify.TXTAnswer {
			var recs []string
			if v, ok := values[name]; ok {
				recs = []string{v}
			}
			out := make([]dnsverify.TXTAnswer, 3)
			for i := range out {
				out[i] = dnsverify.TXTAnswer{Definitive: true, Records: recs}
			}
			return out
		},
		NS: func(context.Context, string) ([]string, bool) { return nil, true },
	}
}

// recAudit keeps the audit events the service records.
type recAudit struct{ evs []*models.AuditEvent }

func (r *recAudit) Record(e *models.AuditEvent) { r.evs = append(r.evs, e) }

// actions lists "action target_id actor_kind" per recorded event.
func (r *recAudit) actions() []string {
	out := make([]string, 0, len(r.evs))
	for _, e := range r.evs {
		out = append(out, e.Action+" "+e.TargetID+" "+e.ActorKind)
	}
	return out
}

type harness struct {
	m         *memStore
	notify    *recNotify
	audit     *recAudit
	scheduled []string
	svc       *Service
}

func newHarness(t *testing.T, look domainops.OwnershipLookups, st settingsStub) *harness {
	t.Helper()
	h := &harness{m: newMem(), notify: &recNotify{}, audit: &recAudit{}}
	h.svc = New(Deps{
		Store: h.m, Domains: h.m, Aliases: memAliases{h.m}, Settings: st,
		Lookups:  look,
		Schedule: func(id string) { h.scheduled = append(h.scheduled, id) },
		Notify:   h.notify,
		Audit:    h.audit,
		Now:      func() time.Time { return now },
	})
	return h
}

func primary() settingsStub {
	return settingsStub{st: &models.ServerSettings{ServerRole: models.ServerRolePrimary, NS1Name: "ns1.panel.test", NS2Name: "ns2.panel.test"}}
}

func (h *harness) status(t *testing.T, id string) (string, string) {
	t.Helper()
	if d, ok := h.m.domains[id]; ok {
		return d.OwnershipStatus, d.OwnershipMethod
	}
	if a, ok := h.m.aliases[id]; ok {
		return a.OwnershipStatus, a.OwnershipMethod
	}
	t.Fatalf("no row %s", id)
	return "", ""
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// GH #1816: a TXT proof verifies the domain, converges it, tells the owner
// and the admin bell, and lets the pending names under it that the parent
// rule now covers follow, shallowest first.
func TestCheckDomain_VerifiesAndCascadesThePendingNamesUnderIt(t *testing.T) {
	h := newHarness(t, txtAll(map[string]string{"_jabali-challenge.example.com": "jabali-verify=tok"}), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))
	h.m.addDomain(pendingDomain("d2", "shop.example.com", "u1", "t2", time.Hour))
	// Listed before shop.example.com by name: it is covered only once its
	// nearest ancestor shop.example.com is verified.
	h.m.addDomain(pendingDomain("d3", "a.shop.example.com", "u1", "t3", time.Hour))
	h.m.addDomain(pendingDomain("d4", "x.example.com", "u2", "t4", time.Hour)) // another owner
	h.m.addAlias(&models.WebDomainAlias{ID: "a1", DomainID: "d1", Hostname: "www.example.com",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: "ta"}})

	d, _ := h.m.FindByID(context.Background(), "d1")
	res, err := h.svc.CheckDomain(context.Background(), d)
	if err != nil || res != models.OwnershipResultVerified {
		t.Fatalf("check: %q %v", res, err)
	}
	if st, m := h.status(t, "d1"); st != models.OwnershipVerified || m != models.OwnershipMethodDNSTXT {
		t.Fatalf("d1 = %s/%s, want verified/dns_txt", st, m)
	}
	for _, id := range []string{"d2", "d3", "a1"} {
		if st, m := h.status(t, id); st != models.OwnershipVerified || m != models.OwnershipMethodParent {
			t.Errorf("%s = %s/%s, want verified/parent", id, st, m)
		}
	}
	if st, _ := h.status(t, "d4"); st != models.OwnershipPending {
		t.Errorf("another owner's name must stay pending, got %s", st)
	}
	for _, id := range []string{"d1", "d2", "d3"} {
		if !contains(h.scheduled, id) {
			t.Errorf("%s was not reconciled: %v", id, h.scheduled)
		}
	}
	if !contains(h.notify.kinds("u1"), EventVerified) || !contains(h.notify.kinds(""), EventVerified) {
		t.Errorf("owner and admin bell must hear of it: %+v", h.notify.envs)
	}
	// Every name the panel verified on its own is in the audit log, as a
	// system event on its owner; the other owner's name is not.
	got := h.audit.actions()
	for _, want := range []string{
		AuditVerify + " d1 " + models.AuditActorSystem,
		AuditVerify + " d2 " + models.AuditActorSystem,
		AuditVerify + " d3 " + models.AuditActorSystem,
		AuditAliasVerify + " a1 " + models.AuditActorSystem,
	} {
		if !contains(got, want) {
			t.Errorf("audit %v lacks %q", got, want)
		}
	}
	if len(got) != 4 {
		t.Errorf("audit = %v, want exactly the four verified names", got)
	}
	for _, e := range h.audit.evs {
		if e.SubjectUserID == nil || *e.SubjectUserID != "u1" || e.ActorUserID != nil {
			t.Errorf("event %s: subject %v actor %v, want subject u1 and no actor user", e.TargetID, e.SubjectUserID, e.ActorUserID)
		}
	}
}

// GH #1816: a TXT proof verifies a web alias, converges its domain and is
// audited as a system event on the domain's owner.
func TestCheckAlias_VerifiesAndIsAudited(t *testing.T) {
	h := newHarness(t, txtAll(map[string]string{"_jabali-challenge.shop.example": "jabali-verify=ta"}), primary())
	h.m.addDomain(verifiedDomain("d1", "example.com", "u1", models.OwnershipMethodDNSTXT))
	h.m.addAlias(&models.WebDomainAlias{ID: "a1", DomainID: "d1", Hostname: "shop.example",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: "ta"}})

	res, err := h.svc.CheckAlias(context.Background(), h.m.aliases["a1"])
	if err != nil || res != models.OwnershipResultVerified {
		t.Fatalf("check: %q %v", res, err)
	}
	if st, m := h.status(t, "a1"); st != models.OwnershipVerified || m != models.OwnershipMethodDNSTXT {
		t.Fatalf("a1 = %s/%s, want verified/dns_txt", st, m)
	}
	if !contains(h.scheduled, "d1") {
		t.Errorf("the alias's domain was not reconciled: %v", h.scheduled)
	}
	got := h.audit.actions()
	if len(got) != 1 || got[0] != AuditAliasVerify+" a1 "+models.AuditActorSystem {
		t.Fatalf("audit = %v, want the verified alias", got)
	}
	if sub := h.audit.evs[0].SubjectUserID; sub == nil || *sub != "u1" {
		t.Errorf("subject = %v, want the domain's owner u1", sub)
	}
}

func TestCheckDomain_NoProofRecordsTheResultAndBacksOff(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", 2*time.Minute))
	d, _ := h.m.FindByID(context.Background(), "d1")
	res, err := h.svc.CheckDomain(context.Background(), d)
	if err != nil || res != models.OwnershipResultNotFound {
		t.Fatalf("check: %q %v", res, err)
	}
	row := h.m.domains["d1"]
	if row.OwnershipStatus != models.OwnershipPending || row.OwnershipLastResult != models.OwnershipResultNotFound {
		t.Fatalf("row = %+v", row.OwnershipState)
	}
	if row.OwnershipNextCheckAt == nil || !row.OwnershipNextCheckAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("next check = %v, want a minute from now", row.OwnershipNextCheckAt)
	}
	if len(h.scheduled) != 0 || len(h.notify.envs) != 0 {
		t.Fatalf("no side effects for a failed check: %v %+v", h.scheduled, h.notify.envs)
	}
}

// Nameservers that already point here: the owner cannot prove the name by
// DNS, so the admins are told once (not on every check) and the row is only
// re-checked daily.
func TestCheckDomain_NSPointsHereTellsTheAdminsOnce(t *testing.T) {
	look := txtAll(nil)
	look.NS = func(context.Context, string) ([]string, bool) {
		return []string{"ns1.panel.test.", "ns2.panel.test."}, true
	}
	h := newHarness(t, look, primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))

	for i := 0; i < 2; i++ {
		d, _ := h.m.FindByID(context.Background(), "d1")
		res, err := h.svc.CheckDomain(context.Background(), d)
		if err != nil || res != models.OwnershipResultNSPointsHere {
			t.Fatalf("check %d: %q %v", i, res, err)
		}
	}
	if got := h.notify.kinds(""); len(got) != 1 || got[0] != EventNeedsAdmin {
		t.Fatalf("want exactly one admin notice, got %v", got)
	}
	if next := h.m.domains["d1"].OwnershipNextCheckAt; next == nil || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("next check = %v, want daily", next)
	}
}

// A check that lost the race (an admin revoke rolled the token meanwhile)
// changes nothing and has no side effects.
func TestCheckDomain_LostRaceHasNoSideEffects(t *testing.T) {
	h := newHarness(t, txtAll(map[string]string{"_jabali-challenge.example.com": "jabali-verify=stale"}), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "fresh", time.Hour))
	stale := pendingDomain("d1", "example.com", "u1", "stale", time.Hour)
	res, err := h.svc.CheckDomain(context.Background(), stale)
	if err != nil || res != models.OwnershipResultVerified {
		t.Fatalf("check: %q %v", res, err)
	}
	if st, _ := h.status(t, "d1"); st != models.OwnershipPending {
		t.Fatalf("a proof of an old token must not verify the row, got %s", st)
	}
	if len(h.scheduled) != 0 || len(h.notify.envs) != 0 {
		t.Fatalf("the loser must not reconcile or notify: %v %+v", h.scheduled, h.notify.envs)
	}
}

// GH #1816 / ADR-0170 section 5: a revoke sends the domain back to pending
// and cascades, shallowest first, to the parent-proven names under it. A
// name with its own proof keeps the names under it covered.
func TestRevokeDomain_CascadesToParentProvenNames(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(verifiedDomain("d1", "example.com", "u1", models.OwnershipMethodDNSTXT))
	h.m.addDomain(verifiedDomain("d2", "shop.example.com", "u1", models.OwnershipMethodParent))
	// Listed before shop.example.com by name: it must be re-checked after
	// shop.example.com went pending, or it stays covered by it.
	h.m.addDomain(verifiedDomain("d3", "a.shop.example.com", "u1", models.OwnershipMethodParent))
	h.m.addDomain(verifiedDomain("d4", "own.example.com", "u1", models.OwnershipMethodDNSTXT))
	h.m.addDomain(verifiedDomain("d5", "b.own.example.com", "u1", models.OwnershipMethodParent))
	h.m.addAlias(&models.WebDomainAlias{ID: "a1", DomainID: "d1", Hostname: "www.example.com",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipVerified, OwnershipMethod: models.OwnershipMethodParent}})

	changed, err := h.svc.RevokeDomain(context.Background(), "d1")
	if err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	for _, id := range []string{"d1", "d2", "d3", "a1"} {
		if st, _ := h.status(t, id); st != models.OwnershipPending {
			t.Errorf("%s = %s, want pending", id, st)
		}
	}
	for _, id := range []string{"d4", "d5"} {
		if st, _ := h.status(t, id); st != models.OwnershipVerified {
			t.Errorf("%s = %s, want verified (own proof in between)", id, st)
		}
	}
	if h.m.domains["d1"].OwnershipVerifiedAt == nil {
		t.Error("a revoked row keeps verified_at, so it never expires")
	}
	if h.m.domains["d1"].OwnershipToken == "old-d1" {
		t.Error("a revoke must roll the challenge token")
	}
	if !contains(h.notify.kinds("u1"), EventRevoked) {
		t.Errorf("the owner must hear of the revoke: %+v", h.notify.envs)
	}
}

// Repeating a revoke finishes a cascade an earlier call left half done.
func TestRevokeDomain_RepeatFinishesTheCascade(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))
	h.m.domains["d1"].OwnershipVerifiedAt = ago(time.Hour)
	h.m.addDomain(verifiedDomain("d2", "shop.example.com", "u1", models.OwnershipMethodParent))

	changed, err := h.svc.RevokeDomain(context.Background(), "d1")
	if err != nil || changed {
		t.Fatalf("revoke of a pending row: changed=%v err=%v", changed, err)
	}
	if st, _ := h.status(t, "d2"); st != models.OwnershipPending {
		t.Fatalf("the cascade must still run, d2 = %s", st)
	}
}

func TestRevokeDomain_RefusesThePanelRowAndDockerAppDomains(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	p := verifiedDomain("d1", "panel.example.com", "", models.OwnershipMethodAdmin)
	p.IsPanelPrimary = true
	h.m.addDomain(p)
	da := verifiedDomain("d2", "app.example.com", "u1", models.OwnershipMethodAdmin)
	da.ManagedBy = models.DomainManagedByDockerApp
	h.m.addDomain(da)

	if _, err := h.svc.RevokeDomain(context.Background(), "d1"); !errors.Is(err, ErrPanelPrimary) {
		t.Fatalf("panel row: %v", err)
	}
	if _, err := h.svc.RevokeDomain(context.Background(), "d2"); !errors.Is(err, ErrDockerAppDomain) {
		t.Fatalf("docker-app domain: %v", err)
	}
	for _, id := range []string{"d1", "d2"} {
		if st, _ := h.status(t, id); st != models.OwnershipVerified {
			t.Errorf("%s must stay verified, got %s", id, st)
		}
	}
}

// flushAgent records the agent calls, and the status domain watch had at
// each one.
type flushAgent struct {
	m        *memStore
	watch    string
	calls    []string
	statusAt []string
	err      error
}

func (a *flushAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	a.statusAt = append(a.statusAt, a.m.domains[a.watch].OwnershipStatus)
	return json.RawMessage(`{}`), a.err
}

// A revoked domain's mailboxes must stop signing in at once. queryLogin
// refuses them over IMAP and SMTP, but Stalwart answers webmail (JMAP) from
// its HTTP login cache, so the revoke flushes that cache — after the row is
// pending, or a login in between is cached again.
func TestRevokeDomain_FlushesTheMailLoginCache(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(verifiedDomain("d1", "example.com", "u1", models.OwnershipMethodDNSTXT))
	h.m.addDomain(verifiedDomain("d2", "shop.example.com", "u1", models.OwnershipMethodParent))
	ag := &flushAgent{m: h.m, watch: "d1"}
	h.svc.d.Agent = ag

	if changed, err := h.svc.RevokeDomain(context.Background(), "d1"); err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	if len(ag.calls) != 1 || ag.calls[0] != "mail.auth_cache.flush" {
		t.Fatalf("agent calls %v, want one mail.auth_cache.flush for the whole cascade", ag.calls)
	}
	if ag.statusAt[0] != models.OwnershipPending {
		t.Fatalf("the flush ran while the domain was %s; it must run after the row is pending", ag.statusAt[0])
	}
}

// A repeated revoke whose cascade still moved a name flushes too; one that
// changed nothing does not.
func TestRevokeDomain_FlushesOnlyWhenADomainWentPending(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))
	h.m.domains["d1"].OwnershipVerifiedAt = ago(time.Hour)
	h.m.addDomain(verifiedDomain("d2", "shop.example.com", "u1", models.OwnershipMethodParent))
	ag := &flushAgent{m: h.m, watch: "d2"}
	h.svc.d.Agent = ag

	if _, err := h.svc.RevokeDomain(context.Background(), "d1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls %v after the cascade moved shop.example.com, want one flush", ag.calls)
	}

	if _, err := h.svc.RevokeDomain(context.Background(), "d1"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls %v after a revoke that changed nothing, want no second flush", ag.calls)
	}
}

// The flush is best-effort: the rows are pending already, so an agent
// failure (or an agent that predates the verb) must not fail the revoke.
func TestRevokeDomain_AFailedFlushDoesNotFailTheRevoke(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(verifiedDomain("d1", "example.com", "u1", models.OwnershipMethodDNSTXT))
	h.svc.d.Agent = &flushAgent{m: h.m, watch: "d1", err: errors.New("unknown command")}

	changed, err := h.svc.RevokeDomain(context.Background(), "d1")
	if err != nil || !changed {
		t.Fatalf("revoke with a failing flush: changed=%v err=%v", changed, err)
	}
}

func TestApproveDomain(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))
	h.m.addDomain(pendingDomain("d2", "www.example.com", "u1", "t2", time.Hour))

	changed, err := h.svc.ApproveDomain(context.Background(), "d1")
	if err != nil || !changed {
		t.Fatalf("approve: %v %v", changed, err)
	}
	if st, m := h.status(t, "d1"); st != models.OwnershipVerified || m != models.OwnershipMethodAdmin {
		t.Fatalf("d1 = %s/%s", st, m)
	}
	if st, _ := h.status(t, "d2"); st != models.OwnershipVerified {
		t.Fatalf("the parent rule must follow an approval, d2 = %s", st)
	}
	// The approval itself is audited by its caller, which knows the admin;
	// the service records only the name the parent rule verified.
	if got := h.audit.actions(); len(got) != 1 || got[0] != AuditVerify+" d2 "+models.AuditActorSystem {
		t.Fatalf("audit = %v, want only the cascaded d2", got)
	}
	again, err := h.svc.ApproveDomain(context.Background(), "d1")
	if err != nil || again {
		t.Fatalf("approving a verified row changes nothing: %v %v", again, err)
	}
}

func TestTick_RunsOnlyOnAReadablePrimary(t *testing.T) {
	for name, st := range map[string]settingsStub{
		"standby":    {st: &models.ServerSettings{ServerRole: models.ServerRoleStandby}},
		"read error": {err: errors.New("db down")},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, txtAll(map[string]string{"_jabali-challenge.example.com": "jabali-verify=tok"}), st)
			h.m.addDomain(pendingDomain("d1", "example.com", "u1", "tok", time.Hour))
			h.m.addDomain(pendingDomain("d2", "old.example", "u1", "t2", 15*24*time.Hour))
			h.svc.Tick(context.Background())
			if st, _ := h.status(t, "d1"); st != models.OwnershipPending {
				t.Errorf("no check may run, d1 = %s", st)
			}
			if len(h.m.deleted) != 0 {
				t.Errorf("no expiry may run, deleted %v", h.m.deleted)
			}
		})
	}
}

// GH #1816 decision 4: a never-verified name is released after 14 days, with
// one notice at day 10. The panel's own row, a docker-app domain and a
// revoked row (verified once) never expire.
func TestTick_ExpiryNoticeAndRelease(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	h.m.addDomain(pendingDomain("old", "old.example", "u1", "t1", 15*24*time.Hour))
	h.m.addDomain(pendingDomain("day11", "day11.example", "u1", "t2", 11*24*time.Hour))
	notified := pendingDomain("told", "told.example", "u1", "t3", 11*24*time.Hour)
	notified.OwnershipExpiryNotifiedAt = ago(24 * time.Hour)
	h.m.addDomain(notified)
	revoked := pendingDomain("revoked", "revoked.example", "u1", "t4", 30*24*time.Hour)
	revoked.OwnershipVerifiedAt = ago(40 * 24 * time.Hour)
	h.m.addDomain(revoked)
	panel := pendingDomain("panel", "panel.example", "", "t5", 30*24*time.Hour)
	panel.IsPanelPrimary = true
	h.m.addDomain(panel)
	app := pendingDomain("app", "app.example", "u1", "t6", 30*24*time.Hour)
	app.ManagedBy = models.DomainManagedByDockerApp
	h.m.addDomain(app)
	h.m.addAlias(&models.WebDomainAlias{ID: "a1", DomainID: "revoked", Hostname: "www.old-alias.example",
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending, OwnershipToken: "ta",
			OwnershipPendingSince: ago(15 * 24 * time.Hour)}})

	h.svc.Tick(context.Background())

	if len(h.m.deleted) != 2 || !contains(h.m.deleted, "old") || !contains(h.m.deleted, "a1") {
		t.Fatalf("deleted = %v, want the old domain and the old alias only", h.m.deleted)
	}
	if h.m.domains["day11"].OwnershipExpiryNotifiedAt == nil {
		t.Error("a day-11 claim must be noticed")
	}
	var expiring, expired int
	for _, e := range h.notify.envs {
		switch e.EventKind {
		case EventExpiring:
			expiring++
		case EventExpired:
			expired++
		}
	}
	if expiring != 1 {
		t.Errorf("exactly one expiry notice (the told row is not told again), got %d", expiring)
	}
	if expired < 2 {
		t.Errorf("the owner hears of the removed domain and alias, got %d", expired)
	}
	if !contains(h.scheduled, "revoked") {
		t.Error("a removed alias must reconcile its domain")
	}
	if got := h.audit.actions(); len(got) != 2 ||
		!contains(got, AuditExpire+" old "+models.AuditActorSystem) ||
		!contains(got, AuditAliasExpire+" a1 "+models.AuditActorSystem) {
		t.Errorf("audit = %v, want the removed domain and alias", got)
	}
}

// A verification that lands between the expiry listing and the delete wins:
// the conditional delete keeps the row, and nobody is told it was removed.
func TestTick_ExpiryLosesToAConcurrentVerification(t *testing.T) {
	h := newHarness(t, txtAll(nil), primary())
	old := pendingDomain("old", "old.example", "u1", "t1", 15*24*time.Hour)
	h.m.stalePending = []models.Domain{*old}
	verified := *old
	verified.OwnershipStatus = models.OwnershipVerified
	verified.OwnershipVerifiedAt = ago(time.Second)
	h.m.addDomain(&verified)

	h.svc.Tick(context.Background())

	if _, ok := h.m.domains["old"]; !ok || len(h.m.deleted) != 0 {
		t.Fatalf("a verified row must never be deleted by the expiry, deleted %v", h.m.deleted)
	}
	for _, e := range h.notify.envs {
		if e.EventKind == EventExpired {
			t.Fatalf("no removal notice for a kept row: %+v", e)
		}
	}
	if got := h.audit.actions(); len(got) != 0 {
		t.Fatalf("no audit event for a kept row: %v", got)
	}
}
