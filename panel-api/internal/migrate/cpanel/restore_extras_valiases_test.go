package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A current cPanel keeps an account's forwarders and catch-alls in
// /etc/valiases/<domain>, which pkgacct ships as va/<domain>. The importer read
// only <homedir>/etc/<domain>/aliases, a file such a server does not have, and
// the --skiphomedir SSH pull ships no homedir at all — so every forwarder and
// catch-all of the account was dropped without a line in the summary.

// catchallDomainRepo finds every domain and records catch-all writes.
type catchallDomainRepo struct {
	bindDomainRepo
	catchalls map[string]string // domainID -> target
}

func (r *catchallDomainRepo) UpdateCatchallTarget(_ context.Context, id string, target *string) error {
	if r.catchalls == nil {
		r.catchalls = map[string]string{}
	}
	if target != nil {
		r.catchalls[id] = *target
	}
	return nil
}

// writeVa writes va/<domain> inside the cpmove tree, the way pkgacct ships it.
func writeVa(t *testing.T, parsed *ParsedTarball, domain, body string) {
	t.Helper()
	dir := filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "va")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir va: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain), []byte(body), 0o644); err != nil {
		t.Fatalf("write va: %v", err)
	}
}

func runExtrasWithForwarders(t *testing.T, parsed *ParsedTarball, dom *catchallDomainRepo, fwdRepo *fwdForwarderRepo, preserve bool) *ExtrasResult {
	t.Helper()
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales", DomainID: "dom-example.com"}
	res, err := ImportExtras(
		context.Background(),
		dom,
		&fwdMailboxRepo{mb: mb},
		fwdRepo,
		nil, nil, nil,
		&fwdConvergeAgent{},
		parsed,
		"user-123", "someuser",
		preserve,
	)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	return res
}

func hasSkip(res *ExtrasResult, prefix string) bool {
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// The SSH-pull shape: no homedir, only va/. The forwarder is imported; the
// cPanel default ":fail:" catch-all is not written as a catch-all target.
func TestImportExtras_ReadsPkgacctValiases(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": "ea-php81"})
	writeVa(t, parsed, "example.com", "sales@example.com: fwd@out.org\n*: :fail: No Such User Here\n")

	dom := &catchallDomainRepo{}
	fwdRepo := &fwdForwarderRepo{}
	res := runExtrasWithForwarders(t, parsed, dom, fwdRepo, true)

	if len(fwdRepo.rows) != 1 || fwdRepo.rows[0].Target != "fwd@out.org" {
		t.Fatalf("forwarders = %+v, want one to fwd@out.org (read from va/example.com)", fwdRepo.rows)
	}
	if res.ForwardersCreated != 1 {
		t.Errorf("ForwardersCreated = %d, want 1", res.ForwardersCreated)
	}
	if len(dom.catchalls) != 0 {
		t.Errorf("catch-alls written = %v, want none (\":fail:\" is not an address)", dom.catchalls)
	}
	if !hasSkip(res, "catchall_skip:source_rejects_unknown:example.com") {
		t.Errorf("skipped = %v, want a catchall_skip:source_rejects_unknown line", res.Skipped)
	}
}

// A real address in va/ becomes the domain's catch-all (preserve on); the
// account-inbox form is reported, never written.
func TestImportExtras_ValiasesCatchAll(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": "", "example.org": ""})
	writeVa(t, parsed, "example.com", "*: box@example.net\n")
	writeVa(t, parsed, "example.org", "*: someuser\n")

	dom := &catchallDomainRepo{}
	res := runExtrasWithForwarders(t, parsed, dom, &fwdForwarderRepo{}, true)

	if got := dom.catchalls["dom-example.com"]; got != "box@example.net" {
		t.Errorf("example.com catch-all = %q, want box@example.net", got)
	}
	if _, set := dom.catchalls["dom-example.org"]; set {
		t.Errorf("example.org catch-all written (%q); the account-inbox form has no address", dom.catchalls["dom-example.org"])
	}
	if res.CatchallsSet != 1 {
		t.Errorf("CatchallsSet = %d, want 1", res.CatchallsSet)
	}
	if !hasSkip(res, "catchall_skip:account_inbox:example.org") {
		t.Errorf("skipped = %v, want a catchall_skip:account_inbox line", res.Skipped)
	}
}

// va/ is cPanel's own file; the homedir layout is read only for a domain va/
// does not cover.
func TestImportExtras_ValiasesWinOverHomedirAliases(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": ""})
	writeVa(t, parsed, "example.com", "sales: from-va@out.org\n")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "etc", "example.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "etc", "example.com", "aliases"), []byte("sales: from-home@out.org\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed.HomeDir = home

	fwdRepo := &fwdForwarderRepo{}
	runExtrasWithForwarders(t, parsed, &catchallDomainRepo{}, fwdRepo, true)

	if len(fwdRepo.rows) != 1 || fwdRepo.rows[0].Target != "from-va@out.org" {
		t.Fatalf("forwarders = %+v, want only from-va@out.org", fwdRepo.rows)
	}
}

// A symlink or a non-domain name in va/ is not read.
func TestCpanelAliasFiles_OnlyRegularDomainFiles(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": ""})
	writeVa(t, parsed, "example.com", "sales: a@out.org\n")
	va := filepath.Join(parsed.ExtractDir, "cpmove-someuser", "va")
	if err := os.Symlink("/etc/passwd", filepath.Join(va, "example.net")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(va, ".hidden"), []byte("x: y@z.org\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := cpanelAliasFiles(parsed)
	if len(got) != 1 || got["example.com"] == "" {
		t.Fatalf("alias files = %v, want only example.com", got)
	}
}

// otherOwnerDomainRepo: every domain belongs to another tenant.
type otherOwnerDomainRepo struct{ catchallDomainRepo }

func (r *otherOwnerDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	return &models.Domain{ID: "dom-" + name, Name: name, UserID: "someone-else"}, nil
}

// The domain names and lines in va/ come from the source. A tampered file
// naming another tenant's domain must not set its catch-all or add forwarders
// on its mailboxes — that would hand the source that tenant's mail.
func TestImportExtras_ValiasesRefuseAnotherTenantsDomain(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": ""})
	writeVa(t, parsed, "example.com", "sales: fwd@out.org\n*: box@out.org\n")

	dom := &otherOwnerDomainRepo{}
	fwdRepo := &fwdForwarderRepo{}
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales", DomainID: "dom-example.com"}
	res, err := ImportExtras(context.Background(), dom, &fwdMailboxRepo{mb: mb}, fwdRepo,
		nil, nil, nil, &fwdConvergeAgent{}, parsed, "user-123", "someuser", true)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if len(dom.catchalls) != 0 || len(fwdRepo.rows) != 0 {
		t.Fatalf("catch-alls=%v forwarders=%+v, want none on another tenant's domain", dom.catchalls, fwdRepo.rows)
	}
	if !hasSkip(res, "aliases_refused:example.com") {
		t.Errorf("skipped = %v, want an aliases_refused line", res.Skipped)
	}
}

// A forwarder line whose address resolves to a mailbox on some other domain
// row is refused, even when the alias domain itself is the account's.
func TestImportExtras_ValiasesForwarderMailboxMustBeOnTheDomain(t *testing.T) {
	parsed := writeUserdata(t, map[string]string{"example.com": ""})
	writeVa(t, parsed, "example.com", "sales: fwd@out.org\n")

	fwdRepo := &fwdForwarderRepo{}
	mb := &models.Mailbox{ID: "mb-sales", EmailCached: "sales@example.com", LocalPart: "sales", DomainID: "dom-of-someone-else"}
	res, err := ImportExtras(context.Background(), &catchallDomainRepo{}, &fwdMailboxRepo{mb: mb}, fwdRepo,
		nil, nil, nil, &fwdConvergeAgent{}, parsed, "user-123", "someuser", true)
	if err != nil {
		t.Fatalf("ImportExtras: %v", err)
	}
	if len(fwdRepo.rows) != 0 {
		t.Fatalf("forwarders = %+v, want none", fwdRepo.rows)
	}
	if !hasSkip(res, "forwarder_refused:sales@example.com") {
		t.Errorf("skipped = %v, want a forwarder_refused line", res.Skipped)
	}
}

func TestCatchAllAddress(t *testing.T) {
	for _, tc := range []struct{ in, addr, skip string }{
		{"box@example.net", "box@example.net", ""},
		{"  box@example.net ", "box@example.net", ""},
		{":fail: No Such User Here", "", "source_rejects_unknown"},
		{":blackhole:", "", "source_discards_unknown"},
		{"|/usr/local/bin/handler", "", "pipe"},
		{"someuser", "", "account_inbox"},
		{"a b@example.net", "", "invalid"},
		{"a@b@example.net", "", "invalid"},
		{"box@", "", "invalid"},
		{"@example.net", "", "invalid"},
		{"box@localhost", "", "invalid"},
		{"box@example.net, other@example.net", "", "invalid"},
	} {
		addr, skip := catchAllAddress(tc.in)
		if addr != tc.addr || skip != tc.skip {
			t.Errorf("catchAllAddress(%q) = (%q, %q), want (%q, %q)", tc.in, addr, skip, tc.addr, tc.skip)
		}
	}
}
