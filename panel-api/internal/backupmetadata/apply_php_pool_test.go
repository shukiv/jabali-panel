package backupmetadata

import (
	"context"
	"errors"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: restoring an uploaded account backup into an account created on
// this server lost every domain. Creating the account gave it its own PHP pool
// for the default version; the backup's pool (another server's id, same user
// and version) then hit uniq_user_phpver, and each domain still pointed at the
// backup's pool id, so its row failed the php_pool foreign key. With no domain
// row, the mailboxes failed too (email_cached comes from the domain's name).

// ppPools is a PHP pool repo that enforces one pool per (user, version), as
// the uniq_user_phpver index does.
type ppPools struct {
	repository.PHPPoolRepository
	rows      []models.PHPPool
	createErr error
}

func (r *ppPools) FindByID(_ context.Context, id string) (*models.PHPPool, error) {
	for i := range r.rows {
		if r.rows[i].ID == id {
			return &r.rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *ppPools) FindByUserAndVersion(_ context.Context, userID, version string) (*models.PHPPool, error) {
	for i := range r.rows {
		if r.rows[i].UserID == userID && r.rows[i].PHPVersion == version {
			return &r.rows[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *ppPools) Create(_ context.Context, p *models.PHPPool) error {
	if r.createErr != nil {
		return r.createErr
	}
	for _, row := range r.rows {
		if row.UserID == p.UserID && row.PHPVersion == p.PHPVersion {
			return errors.New("Error 1062 (23000): Duplicate entry for key 'uniq_user_phpver'")
		}
	}
	r.rows = append(r.rows, *p)
	return nil
}

func (r *ppPools) Update(_ context.Context, p *models.PHPPool) error {
	for i := range r.rows {
		if r.rows[i].ID == p.ID {
			r.rows[i] = *p
			return nil
		}
	}
	return repository.ErrNotFound
}

// ppDomains fails Create when the row names a PHP pool that doesn't exist,
// like the fk_domain_php_pool foreign key.
type ppDomains struct {
	repository.DomainRepository
	pools   *ppPools
	created []models.Domain
	failAll error
}

func (r *ppDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *ppDomains) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}

func (r *ppDomains) Create(ctx context.Context, d *models.Domain) error {
	if r.failAll != nil {
		return r.failAll
	}
	if d.PHPPoolID != nil {
		if _, err := r.pools.FindByID(ctx, *d.PHPPoolID); err != nil {
			return errors.New("Error 1452 (23000): a foreign key constraint fails (fk_domain_php_pool)")
		}
	}
	r.created = append(r.created, *d)
	return nil
}

// Update stands for the write Apply makes after the insert, for a switch the
// insert turned on.
func (r *ppDomains) Update(context.Context, *models.Domain) error { return nil }

func ppMeta() *internalbackup.AccountMetadata {
	uname := "alice"
	src := "p-src"
	return &internalbackup.AccountMetadata{
		User:     internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &uname},
		PHPPools: []internalbackup.MetadataPHPPool{{ID: src, PHPVersion: "8.4", PmMode: "ondemand", PmMaxChildren: 5}},
		Domains: []internalbackup.MetadataDomain{{
			ID: "d1", Name: "alice.org", DocRoot: "/home/alice/domains/alice.org/public_html", PHPPoolID: &src,
			Mailboxes: []internalbackup.MetadataMailbox{{ID: "mb1", LocalPart: "info"}},
		}},
	}
}

func ppDeps(pools *ppPools, doms *ppDomains, mb *dcMailboxes) Deps {
	return Deps{
		Users: &createGuardUsersRepo{}, PHPPools: pools, Domains: doms, Mailboxes: mb,
		MailAddresses: &dcReleaser{mb: mb},
		CheckDomain:   func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil },
	}
}

// The account on this server already has its own 8.4 pool: the backup's 8.4
// pool maps onto it, and the domain is restored bound to that pool.
func TestApply_ReusesTheTargetsPoolOfTheSameVersion(t *testing.T) {
	pools := &ppPools{rows: []models.PHPPool{{ID: "p-target", UserID: "u1", PHPVersion: "8.4"}}}
	doms := &ppDomains{pools: pools}
	mb := &dcMailboxes{}

	r := Apply(context.Background(), ppMeta(), ppDeps(pools, doms, mb))

	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	if len(pools.rows) != 1 {
		t.Fatalf("pools = %+v, want only the target's own pool", pools.rows)
	}
	if len(doms.created) != 1 || doms.created[0].PHPPoolID == nil || *doms.created[0].PHPPoolID != "p-target" {
		t.Fatalf("domains = %+v, want alice.org bound to p-target", doms.created)
	}
	if mb.created != 1 {
		t.Fatalf("mailboxes created = %d, want 1", mb.created)
	}
}

// A pool that can't be restored must not cost the domain: it is restored
// unbound (the reconciler binds it to the account's default pool), with a note.
func TestApply_PoolNotRestoredLeavesTheDomainOnTheDefaultPool(t *testing.T) {
	pools := &ppPools{createErr: errors.New("boom")}
	doms := &ppDomains{pools: pools}
	mb := &dcMailboxes{}

	r := Apply(context.Background(), ppMeta(), ppDeps(pools, doms, mb))

	if len(doms.created) != 1 || doms.created[0].PHPPoolID != nil {
		t.Fatalf("domains = %+v, want alice.org restored with no pool", doms.created)
	}
	if !hasError(r.Errors, "default PHP pool") {
		t.Fatalf("errors %v should say the domain uses the default PHP pool", r.Errors)
	}
	if mb.created != 1 {
		t.Fatalf("mailboxes created = %d, want 1", mb.created)
	}
}

// When the domain row itself can't be written, its mailboxes are not attempted:
// they can't exist without it, and their errors would bury the real one.
func TestApply_DomainThatFailsToSaveTakesItsChildren(t *testing.T) {
	pools := &ppPools{}
	doms := &ppDomains{pools: pools, failAll: errors.New("disk full")}
	mb := &dcMailboxes{}

	r := Apply(context.Background(), ppMeta(), ppDeps(pools, doms, mb))

	if mb.created != 0 {
		t.Fatalf("mailboxes created = %d, want 0 when the domain failed to save", mb.created)
	}
	if !hasError(r.Errors, "disk full") {
		t.Fatalf("errors %v should carry the domain failure", r.Errors)
	}
}

// SECURITY: an uploaded backup is untrusted. A bundle naming a pool id that
// belongs to another account must not bind the restored domain to it — that
// pool runs PHP as the other account's user.
func TestApply_NeverBindsADomainToAnotherAccountsPool(t *testing.T) {
	foreign := "p-bob"
	pools := &ppPools{rows: []models.PHPPool{{ID: foreign, UserID: "u-bob", PHPVersion: "8.4"}}}
	doms := &ppDomains{pools: pools}
	mb := &dcMailboxes{}
	meta := ppMeta()
	meta.PHPPools[0].ID = foreign
	meta.Domains[0].PHPPoolID = &foreign

	r := Apply(context.Background(), meta, ppDeps(pools, doms, mb))

	if len(doms.created) != 1 {
		t.Fatalf("domains = %+v, want alice.org restored", doms.created)
	}
	if got := doms.created[0].PHPPoolID; got != nil && *got == foreign {
		t.Fatalf("alice.org bound to another account's pool %s", foreign)
	}
	if !hasError(r.Errors, "another account") {
		t.Fatalf("errors %v should say the pool belongs to another account", r.Errors)
	}
}

// A domain naming a pool id the bundle doesn't list is restored unbound, not
// bound to whatever pool has that id on this server.
func TestApply_DomainNamingAnUnlistedPoolIsLeftUnbound(t *testing.T) {
	foreign := "p-bob"
	pools := &ppPools{rows: []models.PHPPool{{ID: foreign, UserID: "u-bob", PHPVersion: "8.4"}}}
	doms := &ppDomains{pools: pools}
	mb := &dcMailboxes{}
	meta := ppMeta()
	meta.PHPPools = nil
	meta.Domains[0].PHPPoolID = &foreign

	Apply(context.Background(), meta, ppDeps(pools, doms, mb))

	if len(doms.created) != 1 || doms.created[0].PHPPoolID != nil {
		t.Fatalf("domains = %+v, want alice.org restored with no pool", doms.created)
	}
}

// ppIni is an ini-override repo keyed by pool.
type ppIni struct {
	repository.PHPPoolIniOverrideRepository
	rows []models.PHPPoolIniOverride
}

func (r *ppIni) ListByPool(_ context.Context, poolID string) ([]models.PHPPoolIniOverride, error) {
	var out []models.PHPPoolIniOverride
	for _, o := range r.rows {
		if o.PoolID == poolID {
			out = append(out, o)
		}
	}
	return out, nil
}

func (r *ppIni) Create(_ context.Context, o *models.PHPPoolIniOverride) error {
	r.rows = append(r.rows, *o)
	return nil
}

// The backup pool's PHP settings come with it onto the account's own pool of
// the same version. A setting the pool already has keeps its value: restore
// adds, it doesn't overwrite.
func TestApply_PoolSettingsComeWithTheMappedPool(t *testing.T) {
	pools := &ppPools{rows: []models.PHPPool{{ID: "p-target", UserID: "u1", PHPVersion: "8.4"}}}
	ini := &ppIni{rows: []models.PHPPoolIniOverride{{ID: "o-have", PoolID: "p-target", Directive: "memory_limit", Value: "256M", Kind: "value"}}}
	meta := ppMeta()
	meta.PHPPools[0].IniOverrides = []internalbackup.MetadataPHPPoolIniOverride{
		{ID: "o1", Directive: "memory_limit", Value: "512M", Kind: "value"},
		{ID: "o2", Directive: "max_execution_time", Value: "120", Kind: "value"},
	}
	deps := ppDeps(pools, &ppDomains{pools: pools}, &dcMailboxes{})
	deps.PHPPoolIni = ini

	r := Apply(context.Background(), meta, deps)

	got := map[string]string{}
	for _, o := range ini.rows {
		if o.PoolID != "p-target" {
			t.Fatalf("override %+v written to pool %s, want p-target", o, o.PoolID)
		}
		if _, dup := got[o.Directive]; dup {
			t.Fatalf("directive %s written twice: %+v", o.Directive, ini.rows)
		}
		got[o.Directive] = o.Value
	}
	if got["memory_limit"] != "256M" || got["max_execution_time"] != "120" {
		t.Fatalf("pool settings = %v (errors %v), want memory_limit kept at 256M and max_execution_time=120 added", got, r.Errors)
	}
}
