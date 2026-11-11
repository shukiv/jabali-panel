package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// rotateFixture is a mail domain as #2475 left it: Ed25519 and RSA key files,
// and Stalwart signatures that sign with those keys (DKIM2 on as well).
type rotateFixture struct {
	dir, edPath, rsaPath string
	edFile, rsaFile      []byte // the key files' bytes before the rotation
	edTXT, rsaTXT        string
	reg                  *fakeDkim2Registry
	systemctl            *systemctlCapture
}

func newRotateFixture(t *testing.T) *rotateFixture {
	t.Helper()
	f := &rotateFixture{dir: wireDKIMDir(t)}
	f.edPath = filepath.Join(f.dir, "example.com.key")
	f.rsaPath = filepath.Join(f.dir, "example.com.rsa.key")
	var err error
	if f.edTXT, err = ensureDKIMKey(f.edPath); err != nil {
		t.Fatal(err)
	}
	var rsaPEM []byte
	if rsaPEM, f.rsaTXT, err = ensureDKIMRSAKey(f.rsaPath); err != nil {
		t.Fatal(err)
	}
	f.edFile, _ = os.ReadFile(f.edPath)
	f.rsaFile, _ = os.ReadFile(f.rsaPath)
	seed, _ := dkim.LoadEd25519(f.edPath)
	edPEM, _ := dkim.SeedToPKCS8PEM(seed)
	key := func(pem []byte) map[string]any { return map[string]any{"@type": "Text", "secret": string(pem)} }
	f.reg = &fakeDkim2Registry{sigs: map[string]map[string]any{
		"ed1":  {"id": "ed1", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali", "privateKey": key(edPEM)},
		"rsa1": {"id": "rsa1", "@type": "Dkim1RsaSha256", "domainId": "dom1", "selector": "jabalirsa", "privateKey": key(rsaPEM)},
		"d2":   {"id": "d2", "@type": "Dkim2Ed25519Sha256", "domainId": "dom1", "selector": "jabali", "privateKey": key(edPEM)},
		// Another domain's signature must not be touched.
		"other": {"id": "other", "@type": "Dkim1Ed25519Sha256", "domainId": "dom2", "selector": "jabali", "privateKey": key([]byte("other-key"))},
	}}
	f.systemctl = &systemctlCapture{}
	wireSystemctl(t, f.systemctl)
	wireEnsureServer(t, f.reg, "dom1")
	return f
}

// secret is the private key Stalwart holds for signature id.
func (f *rotateFixture) secret(id string) string {
	pk, _ := f.reg.sigs[id]["privateKey"].(map[string]any)
	s, _ := pk["secret"].(string)
	return s
}

// edPEMOnDisk is the Ed25519 key file's key in the form Stalwart gets it.
func edPEMOnDisk(t *testing.T, path string) string {
	t.Helper()
	seed, err := dkim.LoadEd25519(path)
	if err != nil {
		t.Fatal(err)
	}
	pemKey, _ := dkim.SeedToPKCS8PEM(seed)
	return string(pemKey)
}

// callDKIMRotate returns the response as the panel sees it on the wire.
func callDKIMRotate(t *testing.T, domain string) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"domain_name": domain})
	out, err := domainEmailDKIMRotateHandler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func requireNoStagedKeys(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".new") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("staged key file left behind: %s", e.Name())
		}
	}
}

// Stalwart signs with the keys in its registry, not the files. A rotation
// that changed only the files published keys nothing signed with: every
// signature of the domain must carry the new key, and the published TXT
// must be that key's.
func TestDKIMRotate_StalwartSignsWithTheNewKeys(t *testing.T) {
	f := newRotateFixture(t)

	resp, err := callDKIMRotate(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}

	newEd := edPEMOnDisk(t, f.edPath)
	if readFile(t, f.edPath) == string(f.edFile) {
		t.Fatal("the Ed25519 key file did not change")
	}
	if f.secret("ed1") != newEd {
		t.Error("Stalwart's Ed25519 signature still signs with the old key")
	}
	if f.secret("d2") != newEd {
		t.Error("Stalwart's DKIM2 signature still signs with the old key")
	}
	newRSA, err := dkim.LoadRSA(f.rsaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(newRSA) == string(f.rsaFile) {
		t.Fatal("the RSA key file did not change")
	}
	if f.secret("rsa1") != string(newRSA) {
		t.Error("Stalwart's RSA signature still signs with the old key")
	}
	if f.secret("other") != "other-key" {
		t.Error("another domain's signature was changed")
	}

	seed, _ := dkim.LoadEd25519(f.edPath)
	wantEd, _ := dkim.PublicDKIMTxtFromSeed(seed)
	wantRSA, _ := dkim.PublicRSADKIMTxtFromPEM(newRSA)
	for field, want := range map[string]string{
		"old_dkim_public_key":     f.edTXT,
		"new_dkim_public_key":     string(wantEd),
		"old_dkim_rsa_public_key": f.rsaTXT,
		"new_dkim_rsa_public_key": string(wantRSA),
		"old_key_backup_path":     f.edPath + ".old",
	} {
		if resp[field] != want {
			t.Errorf("%s = %v, want %q", field, resp[field], want)
		}
	}
	if w, ok := resp["warnings"]; ok {
		t.Errorf("warnings = %v, want none", w)
	}

	if readFile(t, f.edPath+".old") != string(f.edFile) {
		t.Error("example.com.key.old is not the previous Ed25519 key")
	}
	if readFile(t, f.rsaPath+".old") != string(f.rsaFile) {
		t.Error("example.com.rsa.key.old is not the previous RSA key")
	}
	requireNoStagedKeys(t, f.dir)
	if len(f.systemctl.calls) != 0 {
		t.Errorf("systemctl calls = %v, want none (a registry update applies without a reload)", f.systemctl.calls)
	}
}

// Stalwart refusing the Ed25519 key fails the rotation with nothing
// changed: no key file, no other signature, no RSA rotation.
func TestDKIMRotate_Ed25519Refused_ChangesNothing(t *testing.T) {
	f := newRotateFixture(t)
	f.reg.refuseUpdate = map[string]bool{"ed1": true}
	before := map[string]string{"ed1": f.secret("ed1"), "rsa1": f.secret("rsa1"), "d2": f.secret("d2")}

	_, err := callDKIMRotate(t, "example.com")
	requireAgentErrorCode(t, err, agentwire.CodeInternal)

	for id, s := range before {
		if f.secret(id) != s {
			t.Errorf("signature %s changed", id)
		}
	}
	if readFile(t, f.edPath) != string(f.edFile) || readFile(t, f.rsaPath) != string(f.rsaFile) {
		t.Error("a key file changed")
	}
	for _, p := range []string{f.edPath + ".old", f.rsaPath + ".old"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s written by a failed rotation", filepath.Base(p))
		}
	}
	requireNoStagedKeys(t, f.dir)
}

// The RSA key is rotated after the Ed25519 one. Stalwart refusing it keeps
// the RSA key as it was (file, signature and published TXT), and the
// rotation reports the Ed25519 key with a warning.
func TestDKIMRotate_RSARefused_KeepsRSAAndWarns(t *testing.T) {
	f := newRotateFixture(t)
	f.reg.refuseUpdate = map[string]bool{"rsa1": true}
	rsaBefore := f.secret("rsa1")

	resp, err := callDKIMRotate(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if f.secret("ed1") != edPEMOnDisk(t, f.edPath) || readFile(t, f.edPath) == string(f.edFile) {
		t.Error("the Ed25519 key was not rotated")
	}
	if f.secret("rsa1") != rsaBefore || readFile(t, f.rsaPath) != string(f.rsaFile) {
		t.Error("the RSA key changed although Stalwart refused it")
	}
	if _, err := os.Stat(f.rsaPath + ".old"); err == nil {
		t.Error("example.com.rsa.key.old written for an RSA key that was not rotated")
	}
	if v, ok := resp["new_dkim_rsa_public_key"]; ok {
		t.Errorf("new_dkim_rsa_public_key = %v, want it absent", v)
	}
	w, _ := json.Marshal(resp["warnings"])
	if !strings.Contains(string(w), "RSA") {
		t.Errorf("warnings = %s, want one about the RSA key", w)
	}
	requireNoStagedKeys(t, f.dir)
}

// DKIM2 shares the Ed25519 key. Stalwart refusing it there still rotates
// the DKIM1 signatures, with a warning naming DKIM2.
func TestDKIMRotate_DKIM2Refused_Warns(t *testing.T) {
	f := newRotateFixture(t)
	f.reg.refuseUpdate = map[string]bool{"d2": true}

	resp, err := callDKIMRotate(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if f.secret("ed1") != edPEMOnDisk(t, f.edPath) {
		t.Error("the Ed25519 signature was not rotated")
	}
	w, _ := json.Marshal(resp["warnings"])
	if !strings.Contains(string(w), "DKIM2") {
		t.Errorf("warnings = %s, want one about DKIM2", w)
	}
}

// A domain without an RSA signature or key (not reconciled since #2475)
// gets them from the rotation: there is no old RSA key to report.
func TestDKIMRotate_NoRSAYet_CreatesIt(t *testing.T) {
	f := newRotateFixture(t)
	delete(f.reg.sigs, "rsa1")
	delete(f.reg.sigs, "d2")
	if err := os.Remove(f.rsaPath); err != nil {
		t.Fatal(err)
	}

	resp, err := callDKIMRotate(t, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := sigSummary(f.reg, "dom1"); got != "Dkim1Ed25519Sha256/jabali Dkim1RsaSha256/jabalirsa" {
		t.Fatalf("signatures = %q", got)
	}
	newRSA, err := dkim.LoadRSA(f.rsaPath)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := sigOfType(f.reg, "dom1", "Dkim1RsaSha256")["privateKey"].(map[string]any)
	if pk["secret"] != string(newRSA) {
		t.Error("the created RSA signature's key is not the key on disk")
	}
	want, _ := dkim.PublicRSADKIMTxtFromPEM(newRSA)
	if resp["new_dkim_rsa_public_key"] != string(want) {
		t.Errorf("new_dkim_rsa_public_key = %v", resp["new_dkim_rsa_public_key"])
	}
	if v, ok := resp["old_dkim_rsa_public_key"]; ok {
		t.Errorf("old_dkim_rsa_public_key = %v, want it absent", v)
	}
	if _, err := os.Stat(f.rsaPath + ".old"); err == nil {
		t.Error("example.com.rsa.key.old written with no previous RSA key")
	}
}

// Rotation applies only to a domain whose mail is set up: Stalwart must know
// it and its Ed25519 key must exist. Otherwise nothing is generated.
func TestDKIMRotate_Preconditions(t *testing.T) {
	t.Run("no key file", func(t *testing.T) {
		f := newRotateFixture(t)
		if err := os.Remove(f.edPath); err != nil {
			t.Fatal(err)
		}
		_, err := callDKIMRotate(t, "example.com")
		requireAgentErrorCode(t, err, agentwire.CodeFailedPrecondition)
		if _, err := os.Stat(f.edPath); err == nil {
			t.Error("a key was generated")
		}
	})
	t.Run("unknown to Stalwart", func(t *testing.T) {
		f := newRotateFixture(t)
		wireEnsureServer(t, f.reg) // x:Domain/query finds nothing
		_, err := callDKIMRotate(t, "example.com")
		requireAgentErrorCode(t, err, agentwire.CodeFailedPrecondition)
		if readFile(t, f.edPath) != string(f.edFile) {
			t.Error("the key file changed")
		}
	})
	t.Run("bad names", func(t *testing.T) {
		for _, name := range []string{"", "../etc", "a b.com", "-x.com"} {
			_, err := callDKIMRotate(t, name)
			requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
		}
	})
}
