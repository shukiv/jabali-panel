package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a backup carries each domain's web domain aliases (GH #1625), and
// a restore adds them back through the alias page's checks. A restore only
// adds aliases; it never removes one.

// alRepo keeps the aliases on this server and the ones Apply creates.
type alRepo struct {
	repository.WebDomainAliasRepository
	t          *testing.T
	rows       []models.WebDomainAlias
	created    []models.WebDomainAlias
	batchCalls int
	// noPerDomain fails a per-domain read: the builder batches (JAB-374).
	noPerDomain bool
}

func (r *alRepo) ListByDomainIDs(_ context.Context, ids []string) ([]models.WebDomainAlias, error) {
	r.batchCalls++
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []models.WebDomainAlias
	for _, a := range r.rows {
		if want[a.DomainID] {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *alRepo) ListByDomain(_ context.Context, id string) ([]models.WebDomainAlias, error) {
	if r.noPerDomain {
		r.t.Fatal("Build must batch aliases via ListByDomainIDs (JAB-374)")
	}
	var out []models.WebDomainAlias
	for _, a := range append(append([]models.WebDomainAlias{}, r.rows...), r.created...) {
		if a.DomainID == id {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *alRepo) Create(_ context.Context, a *models.WebDomainAlias) error {
	r.created = append(r.created, *a)
	return nil
}

func alAlias(id, domainID, host, status string) models.WebDomainAlias {
	a := models.WebDomainAlias{ID: id, DomainID: domainID, Hostname: host}
	a.OwnershipStatus = status
	return a
}

func TestBuild_CarriesDomainAliases(t *testing.T) {
	al := &alRepo{t: t, noPerDomain: true, rows: []models.WebDomainAlias{
		alAlias("a1", "d1", "shop.example.net", models.OwnershipVerified),
		alAlias("a2", "d1", "promo.example.net", models.OwnershipPending),
		alAlias("a3", "d-other", "elsewhere.example.net", models.OwnershipVerified),
	}}
	doms := &fDomains{rows: []models.Domain{{ID: "d1", Name: "shop.org"}, {ID: "d2", Name: "bare.org"}}}
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{Domains: doms, WebDomainAliases: al})
	if len(m.Domains) != 2 {
		t.Fatalf("backup has %d domains, want 2", len(m.Domains))
	}
	want := []internalbackup.MetadataDomainAlias{
		{ID: "a1", Hostname: "shop.example.net", OwnershipStatus: models.OwnershipVerified},
		{ID: "a2", Hostname: "promo.example.net", OwnershipStatus: models.OwnershipPending},
	}
	if got := m.Domains[0].Aliases; len(got) != 2 || got[0] != want[0] || got[1] != want[1] || len(m.Domains[1].Aliases) != 0 {
		t.Fatalf("aliases %+v / %+v, want %+v on shop.org only", got, m.Domains[1].Aliases, want)
	}
	if al.batchCalls != 1 {
		t.Fatalf("alias reads %d, want one batch", al.batchCalls)
	}
}

// alCheck records each alias the checks see, with the domain they see it on,
// and refuses the hostnames in refuse.
type alCheck struct {
	seen   []string
	refuse map[string]string
	// created is the number of domains restored when the first alias is
	// checked: the aliases come after every domain.
	created  int
	domCount func() int
}

func (c *alCheck) check(_ context.Context, dom *models.Domain, host string) (string, error) {
	if len(c.seen) == 0 && c.domCount != nil {
		c.created = c.domCount()
	}
	web := "web"
	if dom.WebDisabled {
		web = "noweb"
	}
	c.seen = append(c.seen, dom.ID+" "+web+" "+host)
	if why, ok := c.refuse[host]; ok {
		return "", errors.New(why)
	}
	return strings.ToLower(strings.TrimSuffix(host, ".")), nil
}

func alMeta() *internalbackup.AccountMetadata {
	m := dcMeta()
	m.AppInstalls = nil
	m.Domains[0].Mailboxes, m.Domains[1].Mailboxes = nil, nil
	m.Domains[0].Aliases = []internalbackup.MetadataDomainAlias{
		{ID: "a-1", Hostname: "Shop.Example.net.", OwnershipStatus: models.OwnershipVerified},
		{ID: "a-2", Hostname: "promo.example.net", OwnershipStatus: models.OwnershipPending},
		{ID: "a-3", Hostname: "old.example.net"},
	}
	m.Domains[1].Aliases = []internalbackup.MetadataDomainAlias{{ID: "a-9", Hostname: "bad-alias.example.net"}}
	return m
}

// alApply restores meta into a new account; bad.org fails the domain checks
// when refuseBad.
func alApply(meta *internalbackup.AccountMetadata, c *alCheck, al *alRepo, refuseBad bool) (*dcDomains, ApplyResult) {
	dom, _, _, d := dcDeps()
	d.CheckDomain = func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		if refuseBad && row.Name == "bad.org" {
			return nil, errors.New("refused")
		}
		return nil, nil
	}
	if c != nil {
		c.domCount = func() int { return len(dom.created) }
		d.CheckAlias = c.check
	}
	if al != nil {
		d.WebDomainAliases = al
	}
	return dom, Apply(context.Background(), meta, d)
}

func TestApply_RestoredDomainGetsItsAliases(t *testing.T) {
	c, al := &alCheck{}, &alRepo{}
	m := alMeta()
	m.Domains[0].WebDisabled = true
	_, r := alApply(m, c, al, false)

	// The checks see each alias on its restored row, once every domain is in:
	// an alias must not take a name a domain of the same backup has.
	want := "d-good noweb Shop.Example.net.|d-good noweb promo.example.net|d-good noweb old.example.net|d-bad web bad-alias.example.net"
	if strings.Join(c.seen, "|") != want || c.created != 2 {
		t.Fatalf("checks saw %v after %d domains; want %s after both domains", c.seen, c.created, want)
	}
	var got []string
	for _, a := range al.created {
		if a.ID == "" || strings.HasPrefix(a.ID, "a-") {
			t.Fatalf("alias %s kept the file's id %q; a restore gives each alias a new one", a.Hostname, a.ID)
		}
		got = append(got, a.DomainID+" "+a.Hostname+" "+a.OwnershipStatus+"/"+a.OwnershipMethod)
	}
	wantRows := "d-good shop.example.net verified/restore|d-good promo.example.net pending/|d-good old.example.net verified/restore|d-bad bad-alias.example.net verified/restore"
	if strings.Join(got, "|") != wantRows || hasError(r.Errors, "alias") {
		t.Fatalf("aliases %v errors %v, want %s", got, r.Errors, wantRows)
	}
}

// An alias the checks refuse is left out, with the reason.
func TestApply_AliasTheChecksRefuseIsReported(t *testing.T) {
	c := &alCheck{refuse: map[string]string{"promo.example.net": "that hostname is already an alias"}}
	al := &alRepo{}
	_, r := alApply(alMeta(), c, al, true)
	if len(al.created) != 2 || !hasError(r.Errors, "domain d-good (good.org): alias promo.example.net not restored: that hostname is already an alias") {
		t.Fatalf("created %d errors %v", len(al.created), r.Errors)
	}
}

// Without the checks or the alias store, no alias is restored; the report
// says so.
func TestApply_AliasesNeedTheirChecksAndStore(t *testing.T) {
	al := &alRepo{}
	_, r := alApply(alMeta(), nil, al, true)
	if len(al.created) != 0 || !hasError(r.Errors, "domain d-good (good.org): 3 aliases not restored: the alias checks are not wired") {
		t.Fatalf("no checks: created %d errors %v", len(al.created), r.Errors)
	}
	c := &alCheck{}
	_, r = alApply(alMeta(), c, nil, true)
	if len(c.seen) != 0 || !hasError(r.Errors, "domain d-good (good.org): 3 aliases not restored: the alias store is not wired") {
		t.Fatalf("no store: checked %v errors %v", c.seen, r.Errors)
	}
}

// The aliases of a domain the restore refused are not restored.
func TestApply_RefusedDomainGetsNoAliases(t *testing.T) {
	c, al := &alCheck{}, &alRepo{}
	alApply(alMeta(), c, al, true)
	for _, s := range c.seen {
		if strings.Contains(s, "bad-alias") {
			t.Fatalf("checked %v: an alias of the refused bad.org", c.seen)
		}
	}
	if len(c.seen) != 3 {
		t.Fatalf("checked %v, want good.org's three aliases", c.seen)
	}
}

// An existing domain of the account gets the backup's aliases it doesn't have,
// in both restore modes, whether the backup names it by its id here or by
// another; one it has stays as it is.
func TestApply_AliasesAttachToTheAccountsExistingDomain(t *testing.T) {
	for _, backupID := range []string{"d-own", "d-bk"} {
		for _, overwrite := range []bool{false, true} {
			f := odSetup("d-own", overwrite, "")
			al := &alRepo{rows: []models.WebDomainAlias{alAlias("x1", "d-own", "shop.example.net", models.OwnershipVerified)}}
			c := &alCheck{}
			f.deps.WebDomainAliases, f.deps.CheckAlias = al, c.check
			m := odMeta(backupID)
			m.Domains[0].Aliases = []internalbackup.MetadataDomainAlias{{Hostname: "SHOP.example.net"}, {Hostname: "new.example.net"}}
			r := Apply(context.Background(), m, f.deps)

			if strings.Join(c.seen, "|") != "d-own web new.example.net" || len(al.created) != 1 || al.created[0].DomainID != "d-own" || al.created[0].Hostname != "new.example.net" {
				t.Fatalf("backup id %s overwrite=%v: checked %v created %+v errors %v; want only new.example.net added to d-own", backupID, overwrite, c.seen, al.created, r.Errors)
			}
			scheduled := false
			for _, id := range f.scheduled {
				scheduled = scheduled || id == "d-own"
			}
			if !scheduled {
				t.Fatalf("backup id %s overwrite=%v: scheduled %v; an added alias must reach the vhost and the certificate", backupID, overwrite, f.scheduled)
			}
		}
	}
}

// A name the backup lists twice is added once, without a report line.
func TestApply_AliasListedTwiceIsAddedOnce(t *testing.T) {
	c, al := &alCheck{}, &alRepo{}
	m := alMeta()
	m.Domains[0].Aliases = []internalbackup.MetadataDomainAlias{{Hostname: "Shop.Example.net."}, {Hostname: "shop.example.net"}}
	_, r := alApply(m, c, al, true)
	if len(al.created) != 1 || len(c.seen) != 1 || hasError(r.Errors, "alias") {
		t.Fatalf("checked %v created %d errors %v; want shop.example.net added once", c.seen, len(al.created), r.Errors)
	}
}
