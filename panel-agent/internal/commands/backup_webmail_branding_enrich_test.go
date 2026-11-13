package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func wbHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// GH #2502: a backup carries the domain's webmail branding pictures, each
// only when it is the file the branding names by its hash, read without
// following a link.
func TestEnrichWebmailBranding(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "webmail-branding")
	orig, origCap := webmailBrandingDir, webmailBrandingBackupCap
	t.Cleanup(func() { webmailBrandingDir, webmailBrandingBackupCap = orig, origCap })
	webmailBrandingDir = dir

	const id = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	if err := os.MkdirAll(filepath.Join(dir, id), 0o755); err != nil {
		t.Fatal(err)
	}
	logo, favicon := []byte("logo-png"), []byte("favicon-png")
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, id, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("logo-light.png", logo)
	write("favicon.png", favicon)
	// A link to a file outside, whose hash the branding names: never read.
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, id, "logo-dark.png")); err != nil {
		t.Fatal(err)
	}
	// A domain directory that is a link, to one holding a picture of the
	// hash the branding names: never read either.
	const linkedID = "01HXXXXXXXXXXXXXXXXXXXXXXX"
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "logo-light.png"), logo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, linkedID)); err != nil {
		t.Fatal(err)
	}

	meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{
		{ID: id, Name: "good.org", WebmailBranding: &backup.MetadataWebmailBranding{
			AppName: "Good Mail", LogoLight: wbHash(logo), LogoDark: wbHash([]byte("secret")),
			Favicon: "000000000000",
		}},
		// The panel's bytes never reach the backup, nor a path from an id
		// that isn't one.
		{ID: "../../etc", Name: "bad.org", WebmailBranding: &backup.MetadataWebmailBranding{
			LogoLight: wbHash(logo), FaviconPNG: []byte("from the panel"),
		}},
		{ID: "01HYYYYYYYYYYYYYYYYYYYYYYY", Name: "plain.org"},
		{ID: linkedID, Name: "linked.org", WebmailBranding: &backup.MetadataWebmailBranding{LogoLight: wbHash(logo)}},
	}}
	warnings := enrichWebmailBranding(meta)

	b := meta.Domains[0].WebmailBranding
	if string(b.LogoLightPNG) != string(logo) {
		t.Fatalf("logo %q, want the file the branding names", b.LogoLightPNG)
	}
	if b.LogoDarkPNG != nil || b.FaviconPNG != nil {
		t.Fatalf("dark logo %q favicon %q: a link, and a file of another hash, must be left out", b.LogoDarkPNG, b.FaviconPNG)
	}
	bad := meta.Domains[1].WebmailBranding
	if bad.LogoLightPNG != nil || bad.FaviconPNG != nil {
		t.Fatalf("bad.org carried %q / %q", bad.LogoLightPNG, bad.FaviconPNG)
	}
	if meta.Domains[3].WebmailBranding.LogoLightPNG != nil {
		t.Fatal("linked.org's picture was read through its linked directory")
	}
	got := strings.Join(warnings, "\n")
	for _, want := range []string{
		"good.org logo-dark left out: ",
		"good.org favicon left out: it is not the picture the branding names",
		"bad.org logo-light left out: not a domain id",
		"linked.org logo-light left out: ",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("warnings:\n%s\nwant %q", got, want)
		}
	}
	if len(warnings) != 4 {
		t.Fatalf("warnings:\n%s", got)
	}

	// Past the backup's total, a picture is left out.
	webmailBrandingBackupCap = len(logo) - 1
	b.LogoDark, b.Favicon = "", ""
	warnings = enrichWebmailBranding(&backup.AccountMetadata{Domains: meta.Domains[:1]})
	if b.LogoLightPNG != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "the account's pictures are over") {
		t.Fatalf("logo %q warnings %v", b.LogoLightPNG, warnings)
	}
}
