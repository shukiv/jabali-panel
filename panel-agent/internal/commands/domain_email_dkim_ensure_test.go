package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// sigSummary lists domainID's signatures as sorted "type/selector" pairs.
func sigSummary(reg *fakeDkim2Registry, domainID string) string {
	var out []string
	for _, s := range reg.sigs {
		if s["domainId"] == domainID {
			out = append(out, s["@type"].(string)+"/"+s["selector"].(string))
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// sigOfType returns domainID's signature of type typ, or nil.
func sigOfType(reg *fakeDkim2Registry, domainID, typ string) map[string]any {
	for _, s := range reg.sigs {
		if s["domainId"] == domainID && s["@type"] == typ {
			return s
		}
	}
	return nil
}

func wireEnsureServer(t *testing.T, reg *fakeDkim2Registry, domainIDs ...string) {
	t.Helper()
	routes := reg.routes()
	routes["x:Domain/query"] = jmapHandlerReturning(jmapQueryResult{IDs: domainIDs, Total: uint64(len(domainIDs))})
	srv := newJMAPServer(t, routes)
	t.Cleanup(srv.Close)
	wireJMAP(t, srv)
}

func callDkimEnsure(t *testing.T, domain string) (dkimEnsureResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(dkimEnsureParams{DomainName: domain})
	out, err := dkimEnsureHandler(context.Background(), raw)
	if err != nil {
		return dkimEnsureResponse{}, err
	}
	return out.(dkimEnsureResponse), nil
}

// A domain with no signature gets both; the RSA signature carries the key
// that was written to disk, and the published TXT is that key's.
func TestDkimEnsure_NoSignatures_CreatesBoth(t *testing.T) {
	dir := wireDKIMDir(t)
	reg := &fakeDkim2Registry{}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resp.Created, ","); got != "Dkim1Ed25519Sha256,Dkim1RsaSha256" {
		t.Errorf("created = %q", got)
	}
	if got := sigSummary(reg, "dom1"); got != "Dkim1Ed25519Sha256/jabali Dkim1RsaSha256/jabalirsa" {
		t.Fatalf("signatures = %q", got)
	}

	rsaPath := filepath.Join(dir, "example.com.rsa.key")
	pemKey, err := dkim.LoadRSA(rsaPath)
	if err != nil {
		t.Fatalf("RSA key file: %v", err)
	}
	if fi, _ := os.Stat(rsaPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("RSA key mode = %v, want 0600", fi.Mode().Perm())
	}
	pk, _ := sigOfType(reg, "dom1", "Dkim1RsaSha256")["privateKey"].(map[string]any)
	if pk["secret"] != string(pemKey) {
		t.Errorf("the RSA signature's key is not the key on disk: %v", pk)
	}
	want, _ := dkim.PublicRSADKIMTxtFromPEM(pemKey)
	if resp.RSAPublicKey != string(want) || resp.RSASelector != "jabalirsa" {
		t.Errorf("RSA TXT = %q, want %q", resp.RSAPublicKey, want)
	}
	if !strings.HasPrefix(resp.Ed25519PublicKey, "v=DKIM1; k=ed25519; p=") || resp.Ed25519Selector != "jabali" {
		t.Errorf("Ed25519 = %q %q", resp.Ed25519Selector, resp.Ed25519PublicKey)
	}
}

// A pre-#2475 domain has the Ed25519 signature only: it gets the RSA one,
// and its Ed25519 key is neither regenerated nor re-pushed.
func TestDkimEnsure_Ed25519Only_AddsRSAAndKeepsEd25519(t *testing.T) {
	dir := wireDKIMDir(t)
	edPath := filepath.Join(dir, "example.com.key")
	edTXT, err := ensureDKIMKey(edPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(edPath)
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		"old": {"id": "old", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali"},
	}}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resp.Created, ","); got != "Dkim1RsaSha256" {
		t.Errorf("created = %q, want the RSA signature only", got)
	}
	if _, ok := reg.sigs["old"]; !ok || len(reg.sigs) != 2 {
		t.Fatalf("signatures = %v", reg.sigs)
	}
	if after, _ := os.ReadFile(edPath); string(after) != string(before) {
		t.Error("the Ed25519 key file changed under an existing signature")
	}
	if resp.Ed25519PublicKey != edTXT {
		t.Errorf("Ed25519 TXT = %q, want the key file's %q", resp.Ed25519PublicKey, edTXT)
	}
}

// Both signatures present: nothing is created, an RSA key file that exists
// is read, not replaced, and a missing one is not generated (a new key
// would be published while the signature keeps signing with the old one).
func TestDkimEnsure_BothPresent_CreatesNothing(t *testing.T) {
	dir := wireDKIMDir(t)
	k, err := dkim.GenerateRSA()
	if err != nil {
		t.Fatal(err)
	}
	rsaPath := filepath.Join(dir, "example.com.rsa.key")
	if err := dkim.WritePrivate(rsaPath, k.PrivatePEM); err != nil {
		t.Fatal(err)
	}
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		"a": {"id": "a", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali"},
		"b": {"id": "b", "@type": "Dkim1RsaSha256", "domainId": "dom1", "selector": "jabalirsa"},
		// Another domain's signatures don't count.
		"c": {"id": "c", "@type": "Dkim1RsaSha256", "domainId": "dom2", "selector": "jabalirsa"},
	}}
	wireEnsureServer(t, reg, "dom1")

	resp, err := callDkimEnsure(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Created) != 0 || reg.nextID != 0 {
		t.Fatalf("created %v on a domain with both signatures", resp.Created)
	}
	if resp.RSAPublicKey != string(k.PublicDKIMTxt) {
		t.Errorf("RSA TXT = %q, want the key file's", resp.RSAPublicKey)
	}
	// The Ed25519 key file is missing: reported unknown, not generated.
	if resp.Ed25519PublicKey != "" {
		t.Errorf("Ed25519 TXT = %q, want \"\" for a signature whose key file is missing", resp.Ed25519PublicKey)
	}
	if _, err := os.Stat(filepath.Join(dir, "example.com.key")); !os.IsNotExist(err) {
		t.Error("an Ed25519 key was generated under an existing signature")
	}
}

func TestDkimEnsure_UnknownDomainIsNotFound(t *testing.T) {
	wireDKIMDir(t)
	wireEnsureServer(t, &fakeDkim2Registry{})
	_, err := callDkimEnsure(t, "example.com")
	requireAgentErrorCode(t, err, agentwire.CodeNotFound)
}

func TestDkimEnsure_RejectsBadDomainName(t *testing.T) {
	wireDKIMDir(t)
	wireEnsureServer(t, &fakeDkim2Registry{}, "dom1")
	for _, name := range []string{"", "../etc", "a b.com", "Example.COM/x"} {
		_, err := callDkimEnsure(t, name)
		requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	}
}
