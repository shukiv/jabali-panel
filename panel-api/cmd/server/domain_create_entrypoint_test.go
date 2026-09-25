package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-279 AC1: `jabali domain create` is an adapter over domainops.Create, the
// create entrypoint the REST door also runs. createDomainDirect itself calls
// initConfig / initDB and is not unit-testable, so these tests drive the real
// module with the CLI's own request (cliCreateInput) and pin that the adapter
// maps each rejection and soft result to the message the CLI printed when the
// steps were inline. A source pin covers the store wiring.

type cliCreateStore struct {
	count     int64
	createErr error
	created   []*models.Domain
}

func (s *cliCreateStore) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (s *cliCreateStore) FindStrictSubdomains(context.Context, string) ([]models.Domain, error) {
	return nil, nil
}
func (s *cliCreateStore) CountByUserID(context.Context, string) (int64, error) { return s.count, nil }
func (s *cliCreateStore) ListPreviewEnabled(context.Context) ([]models.Domain, error) {
	return nil, nil
}
func (s *cliCreateStore) Create(_ context.Context, d *models.Domain) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, d)
	return nil
}
func (s *cliCreateStore) SetSharedCertificate(context.Context, string, *string, string) error {
	return nil
}

type cliCreateOwners map[string]*models.User

func (o cliCreateOwners) FindByID(_ context.Context, spec string) (*models.User, error) {
	if u, ok := o[spec]; ok {
		return u, nil
	}
	return nil, errors.New(`user "` + spec + `" not found`)
}

type cliCreateAliases struct {
	taken string
	err   error
}

func (a cliCreateAliases) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	if a.err != nil {
		return nil, a.err
	}
	if host == a.taken {
		return &models.WebDomainAlias{Hostname: host}, nil
	}
	return nil, repository.ErrNotFound
}

type cliCreatePorts struct {
	repository.PortAllocationRepository
}

func (cliCreatePorts) AllocateReverseProxySpecific(context.Context, string, int) (int, error) {
	return 0, repository.ErrPortInUse
}
func (cliCreatePorts) Release(context.Context, string, string) error { return nil }

func TestCLICreateError_Messages(t *testing.T) {
	uname := "alice"
	pkg := "pkg-1"
	alice := &models.User{ID: "u-alice", Username: &uname}
	admin := &models.User{ID: "u-admin", Username: &uname, IsAdmin: true}
	suspended := &models.User{ID: "u-susp", Username: &uname, Suspended: true}
	quotaFull := &models.User{ID: "u-full", Username: &uname, PackageID: &pkg}
	owners := cliCreateOwners{"alice": alice, "root": admin, "susp": suspended, "full": quotaFull}
	portInvalid := repository.ValidateReverseProxyPort(22)
	if portInvalid == nil {
		t.Fatal("port 22 must be refused by the static policy")
	}

	cases := []struct {
		name string
		in   cliDomainInput
		deps func(*domainops.CreateDeps, *cliCreateStore)
		want string
	}{
		{name: "invalid name", in: cliDomainInput{Name: "localhost", UserID: "alice"},
			want: `domain "localhost" is not a valid FQDN (need at least two labels and a 2+ letter TLD; bare hostnames + IP addresses are rejected)`},
		{name: "alias lookup fails closed", in: cliDomainInput{Name: "shop.example.com", UserID: "alice"},
			deps: func(d *domainops.CreateDeps, _ *cliCreateStore) {
				d.Aliases = cliCreateAliases{err: errors.New("db down")}
			},
			want: `verify domain name against existing aliases: alias lookup for "shop.example.com": db down`},
		{name: "alias clash", in: cliDomainInput{Name: "shop.example.com", UserID: "alice"},
			deps: func(d *domainops.CreateDeps, _ *cliCreateStore) {
				d.Aliases = cliCreateAliases{taken: "www.shop.example.com"}
			},
			want: `the name "www.shop.example.com" is already used as an alias of another domain`},
		{name: "unknown owner keeps the resolver's message", in: cliDomainInput{Name: "shop.example.com", UserID: "ghost"},
			want: `user "ghost" not found`},
		{name: "admin owner", in: cliDomainInput{Name: "shop.example.com", UserID: "root"},
			want: "admin users cannot host domains — create a regular user"},
		{name: "suspended owner names the resolved id", in: cliDomainInput{Name: "shop.example.com", UserID: "susp"},
			want: `user "u-susp" is suspended — unsuspend before adding domains`},
		{name: "quota", in: cliDomainInput{Name: "shop.example.com", UserID: "full"},
			deps: func(d *domainops.CreateDeps, s *cliCreateStore) { s.count = 2; d.Packages = quotaPackages{max: 2} },
			want: "package quota exceeded: 2/2 domains"},
		{name: "web-off reverse proxy", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", WebDisabled: true, ReverseProxy: true},
			want: "a reverse-proxy domain requires web hosting"},
		{name: "web-off document root", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", WebDisabled: true, DocRoot: "/home/alice/x"},
			want: "a web-disabled domain has no document root"},
		{name: "document root outside the owner's home", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", DocRoot: "/etc"},
			want: "invalid --doc-root: domainops: document root outside owner home"},
		{name: "document root with traversal", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", DocRoot: "/home/alice/../bob"},
			want: "invalid --doc-root: domainops: document root contains path traversal"},
		{name: "invalid mail provider", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", MailProvider: "exchange"},
			want: `invalid --mail "exchange" (want jabali|none|m365|google)`},
		{name: "no service", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", WebDisabled: true, DNSDisabled: true, MailProvider: "none"},
			want: "select at least one service: web hosting (--web-enabled), DNS (--manage-dns), or mail (--mail)"},
		{name: "port refused by policy", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", ReverseProxy: true, ReverseProxyPort: 22},
			want: portInvalid.Error()},
		{name: "port in use", in: cliDomainInput{Name: "shop.example.com", UserID: "alice", ReverseProxy: true, ReverseProxyPort: 45001},
			want: "port 45001 is already assigned to another domain — choose another"},
		{name: "duplicate domain", in: cliDomainInput{Name: "shop.example.com", UserID: "alice"},
			deps: func(_ *domainops.CreateDeps, s *cliCreateStore) { s.createErr = repository.ErrConflict },
			want: `domain "shop.example.com" already exists`},
		{name: "insert failure", in: cliDomainInput{Name: "shop.example.com", UserID: "alice"},
			deps: func(_ *domainops.CreateDeps, s *cliCreateStore) { s.createErr = errors.New("db down") },
			want: "create domain row: db down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &cliCreateStore{}
			finder := &cliRecordingOwners{owners: owners}
			deps := domainops.CreateDeps{Domains: store, Users: finder, Ports: cliCreatePorts{}}
			if tc.deps != nil {
				tc.deps(&deps, store)
			}
			_, err := domainops.Create(context.Background(), deps, domainops.CreateHooks{}, cliCreateInput(tc.in))
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if got := cliCreateError(err, tc.in, finder.resolved).Error(); got != tc.want {
				t.Fatalf("message = %q, want %q", got, tc.want)
			}
			if len(store.created) != 0 {
				t.Fatalf("a rejected create must store nothing, got %d rows", len(store.created))
			}
		})
	}
}

// cliRecordingOwners mirrors cliOwnerFinder over a fixed owner map.
type cliRecordingOwners struct {
	owners   cliCreateOwners
	resolved *models.User
}

func (f *cliRecordingOwners) FindByID(ctx context.Context, spec string) (*models.User, error) {
	u, err := f.owners.FindByID(ctx, spec)
	if err == nil {
		f.resolved = u
	}
	return u, err
}

func TestCLICreateInput_ActsAsAdmin(t *testing.T) {
	uname := "alice"
	alice := &models.User{ID: "u-alice", Username: &uname}
	// Another tenant owns the parent zone: an admin actor may still place the
	// subdomain (trusted delegation), and a document root anywhere under the
	// owner's home is accepted (the admin floor).
	store := &cliSuffixStore{parent: &models.Domain{Name: "example.com", UserID: "u-other"}}
	in := cliDomainInput{Name: "shop.example.com", UserID: "alice", DocRoot: " /home/alice/apps/shop "}
	res, err := domainops.Create(context.Background(), domainops.CreateDeps{
		Domains: store, Users: cliCreateOwners{"alice": alice},
	}, domainops.CreateHooks{}, cliCreateInput(in))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	d := res.Domain
	if d.UserID != "u-alice" || d.DocRoot != "/home/alice/apps/shop" || d.SSLMode != models.SSLModeLE ||
		d.MailProvider != models.MailProviderJabali {
		t.Fatalf("unexpected stored domain: %+v", d)
	}
}

type cliSuffixStore struct {
	cliCreateStore
	parent *models.Domain
}

func (s *cliSuffixStore) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if s.parent != nil && name == s.parent.Name {
		return s.parent, nil
	}
	return nil, repository.ErrNotFound
}

func TestCLICreateWarnings(t *testing.T) {
	ctx := context.Background()
	d := &models.Domain{ID: "d1", UserID: "u1", Name: "shop.example.com"}
	wild := `["*.example.com"]`
	cert := &models.SharedCertificate{ID: "c1", SANs: &wild}

	_, lookupErr := domainops.AttachCoveringSharedCert(ctx, domainops.SharedCertDeps{
		Certs: cliCertLister{err: errors.New("db down")}, Domains: &cliCreateStore{},
	}, &models.Domain{ID: "d1", Name: "shop.example.com"})
	if !errors.Is(lookupErr, domainops.ErrSharedCertLookup) {
		t.Fatalf("setup: want a lookup error, got %v", lookupErr)
	}

	cases := []struct {
		name string
		res  domainops.CreateResult
		want []string
	}{
		{name: "nothing to report", res: domainops.CreateResult{Domain: d}},
		{name: "shared-certificate lookup failure",
			res:  domainops.CreateResult{Domain: d, SharedCertErr: lookupErr},
			want: []string{"shared-certificate lookup skipped: db down"}},
		{name: "shared-certificate attach failure names the retry",
			res: domainops.CreateResult{Domain: d, SharedCert: cert,
				SharedCertErr: attachErr(t, cert)},
			want: []string{"shared-certificate auto-attach failed (retry with `jabali ssl shared attach --domain shop.example.com --cert-id c1`): db down"}},
		{name: "agent unavailable",
			res:  domainops.CreateResult{Domain: d, MailErr: &cliAgentUnavailableError{err: errors.New("dial unix: refused")}},
			want: []string{"email auto-enable skipped: agent unavailable (dial unix: refused)"}},
		{name: "mail enable failure names the retry",
			res:  domainops.CreateResult{Domain: d, MailErr: errors.New("stalwart down")},
			want: []string{"email auto-enable failed (can retry with `jabali domain email-enable shop.example.com`): stalwart down"}},
		{name: "DNS autoconfig warnings pass through",
			res:  domainops.CreateResult{Domain: d, MailWarnings: []string{"autoconfig CNAME exists"}},
			want: []string{"autoconfig CNAME exists"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cliCreateWarnings(&tc.res)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || len(got) != len(tc.want) {
				t.Fatalf("warnings = %q, want %q", got, tc.want)
			}
		})
	}
}

type cliCertLister struct {
	certs []models.SharedCertificate
	err   error
}

func (l cliCertLister) ListServerWideAndOwned(context.Context, string) ([]models.SharedCertificate, error) {
	return l.certs, l.err
}

type cliFailingCertSetter struct{ cliCreateStore }

func (cliFailingCertSetter) SetSharedCertificate(context.Context, string, *string, string) error {
	return errors.New("db down")
}

// attachErr produces the module's real attach error for cert.
func attachErr(t *testing.T, cert *models.SharedCertificate) error {
	t.Helper()
	_, err := domainops.AttachCoveringSharedCert(context.Background(), domainops.SharedCertDeps{
		Certs: cliCertLister{certs: []models.SharedCertificate{*cert}}, Domains: &cliFailingCertSetter{},
	}, &models.Domain{ID: "d1", Name: "shop.example.com"})
	if !errors.Is(err, domainops.ErrSharedCertAttach) {
		t.Fatalf("setup: want an attach error, got %v", err)
	}
	return err
}

// TestCLICreateDomain_WiresTheCreateEntrypoint source-pins the store wiring of
// createDomainDirect, which the behavior tests above cannot reach.
func TestCLICreateDomain_WiresTheCreateEntrypoint(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))
	start := strings.Index(src, "func createDomainDirect(")
	if start < 0 {
		t.Fatal("expected createDomainDirect in cli_create.go")
	}
	end := strings.Index(src[start:], "\n}\n")
	fn := src[start : start+end]

	norm := strings.Index(fn, "in.Name = domainops.NormalizeDomainName(in.Name)")
	create := strings.Index(fn, "domainops.Create(ctx, deps, domainops.CreateHooks{")
	if norm < 0 || create < 0 || norm > create {
		t.Fatal("CLI create must normalize the name, then call domainops.Create")
	}
	for _, re := range []string{
		`Domains:\s+domainRepoFromDB\(\),`,
		`Users:\s+owners,`,
		`Aliases:\s+repository\.NewWebDomainAliasRepository\(sharedDB\),`,
		`Packages:\s+packageRepoFromDB\(\),`,
		`Settings:\s+serverSettingsRepoFromDB\(\),`,
		`DNSTemplates:\s+repository\.NewDNSTemplateRepository\(sharedDB\),`,
		`SharedCerts:\s+sharedCertRepoFromDB\(\),`,
		`Ports:\s+repository\.NewPortAllocationRepository\(sharedDB\),`,
		`EnableMail:\s+cliEnableMail,`,
		`\}, cliCreateInput\(in\)\)`,
		`cliCreateError\(err, in, owners\.resolved\)`,
		`cliCreateWarnings\(res\)`,
	} {
		if !regexp.MustCompile(re).MatchString(fn) {
			t.Errorf("createDomainDirect must contain %s", re)
		}
	}
	// A nil *agent.Client assigned to the interface field is a non-nil
	// interface that panics on Call; the adapter must guard the pointer.
	if !strings.Contains(fn, "sharedAgent != nil {\n\t\tdeps.Agent = sharedAgent") {
		t.Error("CLI create must assign deps.Agent only when sharedAgent is non-nil")
	}
	if !strings.Contains(src, "u, err := resolveUser(ctx, spec)") {
		t.Error("the CLI owner finder must resolve --user through resolveUser")
	}
	// The CLI's own copy of any create step must stay gone: each of these back
	// in createDomainDirect means the adapter runs a rule on its own again and
	// can drift from the REST door.
	for _, banned := range []string{
		"validateDomainName(", "AliasCollision(", "CrossTenantSuffixCollision(",
		"CheckOwnerEligible(", "CheckDomainQuota(", "CheckWebOffOptions(", "ValidateDocumentRoot(",
		"ResolveMailPosture(", "ResolveServiceMatrix(", "ReserveReverseProxyPort(", "PersistDomain(",
		"AttachCoveringSharedCert(", "domainmailops.Enable(", "models.Domain{", ".MaxDomains",
	} {
		if strings.Contains(fn, banned) {
			t.Errorf("createDomainDirect must not call %q itself; domainops.Create owns it", banned)
		}
	}
}

func readGoSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// stripLineComments blanks every "//" line comment so a source pin cannot be
// defeated by commenting the pinned call out while leaving its text intact. It
// is a deliberately simple line scanner (no block-comment or string-literal
// awareness); the pinned anchors carry no "//", so it never removes a match.
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "//"); idx >= 0 {
			lines[i] = ln[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
