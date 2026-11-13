package webmailbranding

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const testDomainID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"

func picture(t *testing.T, w, h int, encode func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngOf(b *bytes.Buffer, img image.Image) error { return png.Encode(b, img) }

func TestText(t *testing.T) {
	if got, err := Text("app_name", "  שם החברה Mail  ", MaxAppName); err != nil || got != "שם החברה Mail" {
		t.Fatalf("Text = %q, %v; want the trimmed name", got, err)
	}
	for _, bad := range []string{"Acme\nAPP_NAME=x", "tab\there", strings.Repeat("א", MaxAppName+1)} {
		if _, err := Text("app_name", bad, MaxAppName); err == nil {
			t.Errorf("Text(%q) accepted", bad)
		}
	}
	var fe *FieldError
	if _, err := Text("company_name", "a\x00b", MaxCompanyName); !errors.As(err, &fe) || fe.Field != "company_name" {
		t.Fatalf("err = %v, want a FieldError naming company_name", err)
	}
}

func TestLink(t *testing.T) {
	for _, ok := range []string{"", "https://example.com/privacy", "http://example.com"} {
		if _, err := Link("website_url", ok); err != nil {
			t.Errorf("Link(%q) = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"/relative/path",
		"https://user:pw@example.com/",
		"https://example.com/a b",
		"ftp://example.com/",
		"https://" + strings.Repeat("a", MaxURL) + ".com",
	} {
		if _, err := Link("website_url", bad); err == nil {
			t.Errorf("Link(%q) accepted", bad)
		}
	}
}

func TestEncode(t *testing.T) {
	t.Run("png is encoded again, without what followed it", func(t *testing.T) {
		in := append(picture(t, 40, 20, pngOf), []byte("<html><script>alert(1)</script></html>")...)
		out, hash, err := Encode(bytes.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out, []byte("<script>")) {
			t.Fatal("the stored picture still carries the HTML appended to the upload")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil || format != "png" || cfg.Width != 40 || cfg.Height != 20 {
			t.Fatalf("stored picture = %s %dx%d (%v), want a 40x20 png", format, cfg.Width, cfg.Height, err)
		}
		if len(hash) != 12 {
			t.Fatalf("hash = %q, want 12 hex characters", hash)
		}
	})
	t.Run("jpeg becomes png", func(t *testing.T) {
		in := picture(t, 16, 16, func(b *bytes.Buffer, img image.Image) error { return jpeg.Encode(b, img, nil) })
		out, _, err := Encode(bytes.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		if _, format, _ := image.DecodeConfig(bytes.NewReader(out)); format != "png" {
			t.Fatalf("format = %s, want png", format)
		}
	})
	t.Run("refused", func(t *testing.T) {
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		gifPic := picture(t, 4, 4, func(b *bytes.Buffer, img image.Image) error { return gif.Encode(b, img, nil) })
		for name, c := range map[string]struct {
			in   []byte
			want error
		}{
			"svg":       {svg, ErrBadPicture},
			"gif":       {gifPic, ErrBadPicture},
			"too wide":  {picture(t, MaxSide+1, 1, pngOf), ErrBadSize},
			"too large": {bytes.Repeat([]byte{0}, MaxUploadBytes+1), ErrTooLarge},
		} {
			if _, _, err := Encode(bytes.NewReader(c.in)); !errors.Is(err, c.want) {
				t.Errorf("%s: err = %v, want %v", name, err, c.want)
			}
		}
	})
}

func TestWriteRemovePrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "webmail-branding")
	if err := Write(dir, testDomainID, LogoLight, []byte("png")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(dir, testDomainID, LogoLight))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("stored picture mode = %v (%v), want 0644", fi.Mode(), err)
	}
	if di, _ := os.Stat(filepath.Join(dir, testDomainID)); di.Mode().Perm() != 0o755 {
		t.Fatalf("domain dir mode = %v, want 0755: nginx reads it", di.Mode())
	}
	if err := Write(dir, "../etc", LogoLight, []byte("x")); err == nil {
		t.Fatal("Write accepted a domain id that is not a ULID")
	}

	gone := "01HYYYYYYYYYYYYYYYYYYYYYYY"
	if err := Write(dir, gone, Favicon, []byte("png")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "not-a-domain")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Prune(dir, map[string]bool{testDomainID: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
		t.Fatal("the pictures of a domain with no branding row stayed")
	}
	for _, kept := range []string{filepath.Join(dir, testDomainID), other} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s was removed", kept)
		}
	}
	if err := Remove(dir, testDomainID, LogoLight); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, testDomainID, LogoLight); err != nil {
		t.Fatalf("removing a missing picture: %v", err)
	}
	if err := Prune(filepath.Join(dir, "missing"), nil); err != nil {
		t.Fatalf("Prune of a missing dir: %v", err)
	}
}

func TestAgentDomains(t *testing.T) {
	row := func(name string, applies bool, b models.DomainWebmailBranding) repository.WebmailBrandingRow {
		return repository.WebmailBrandingRow{DomainWebmailBranding: b, DomainName: name, Applies: applies}
	}
	got := AgentDomains([]repository.WebmailBrandingRow{
		row("Customer-A.com", true, models.DomainWebmailBranding{DomainID: testDomainID, AppName: "A Mail", LogoLight: "aaaaaaaaaaaa", Favicon: "ffffffffffff"}),
		row("b.com", false, models.DomainWebmailBranding{DomainID: "01HYYYYYYYYYYYYYYYYYYYYYYY", AppName: "B Mail"}),
		row("c.com", true, models.DomainWebmailBranding{DomainID: "01HXXXXXXXXXXXXXXXXXXXXXXX"}),
		// The agent refuses the whole list over one host it doesn't take, so a
		// name stored outside the FQDN shape (an old import) is left out.
		row("old_import.com", true, models.DomainWebmailBranding{DomainID: "01HWWWWWWWWWWWWWWWWWWWWWWW", AppName: "D Mail"}),
		// ...as is one the agent would refuse for any other value, and a
		// host seen already (the agent refuses a list with a host twice).
		row("e.com", true, models.DomainWebmailBranding{DomainID: "01HVVVVVVVVVVVVVVVVVVVVVVV", AppName: "E\nAPP_NAME=x"}),
		row("customer-a.com", true, models.DomainWebmailBranding{DomainID: "01HTTTTTTTTTTTTTTTTTTTTTTT", AppName: "Again"}),
	})
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want only the one that applies and sets something", got)
	}
	d := got[0]
	if d.Host != "mail.customer-a.com" || d.AppName != "A Mail" {
		t.Fatalf("entry = %+v", d)
	}
	wantLight := "/jabali-webmail-branding/" + testDomainID + "/logo-light.png?v=aaaaaaaaaaaa"
	if d.LogoLightURL != wantLight || d.LogoDarkURL != wantLight {
		t.Fatalf("logos = %q / %q, want the light logo for both", d.LogoLightURL, d.LogoDarkURL)
	}
	if d.FaviconURL != "/jabali-webmail-branding/"+testDomainID+"/favicon.png?v=ffffffffffff" {
		t.Fatalf("favicon = %q", d.FaviconURL)
	}
	if got := AgentDomains(nil); got == nil || len(got) != 0 {
		t.Fatalf("AgentDomains(nil) = %#v, want an empty list (sent as [], not null)", got)
	}
}

// A backup's picture is one the panel stored: a PNG encoded again, which can
// be larger than an upload may be. EncodeStored takes it up to its own cap,
// and checks it as Encode checks an upload.
func TestEncodeStored(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, 400, 400))
	rnd.Read(img.Pix)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	big := b.Bytes()
	if len(big) <= MaxUploadBytes {
		t.Fatalf("test picture is %d bytes, want one over the upload cap", len(big))
	}
	if _, _, err := Encode(bytes.NewReader(big)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Encode = %v, want ErrTooLarge", err)
	}
	out, hash, err := EncodeStored(big, 4<<20)
	if err != nil || len(hash) != 12 {
		t.Fatalf("EncodeStored = %d bytes, %q, %v", len(out), hash, err)
	}
	if again, h2, err := EncodeStored(out, 4<<20); err != nil || h2 != hash || !bytes.Equal(again, out) {
		t.Fatalf("a stored picture encoded again changed: %q vs %q, %v", h2, hash, err)
	}
	if _, _, err := EncodeStored(big, len(big)-1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
	if _, _, err := EncodeStored([]byte("<svg/>"), 4<<20); !errors.Is(err, ErrBadPicture) {
		t.Fatalf("not a picture: %v", err)
	}
	if _, _, err := EncodeStored(picture(t, MaxSide+1, 1, pngOf), 4<<20); !errors.Is(err, ErrBadSize) {
		t.Fatalf("too wide: %v", err)
	}
}

// Run as root (a restore by `jabali backup account-restore`), Write gives
// the directories and the picture the owner of dir's parent, the panel's
// user; run as anyone else it gives nothing away.
func TestWriteAsRootGivesThePanelItsPictures(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "webmail-branding")
	origEUID, origChown := geteuid, fchown
	t.Cleanup(func() { geteuid, fchown = origEUID, origChown })
	var got []string
	fchown = func(fd, uid, gid int) error {
		if uid != os.Getuid() || gid != os.Getgid() {
			t.Errorf("chown to %d:%d, want the parent's owner %d:%d", uid, gid, os.Getuid(), os.Getgid())
		}
		p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.TrimPrefix(p, dir))
		return nil
	}

	geteuid = func() int { return 1000 }
	if err := Write(dir, testDomainID, LogoLight, []byte("png")); err != nil || len(got) != 0 {
		t.Fatalf("not root: chowned %v, %v", got, err)
	}
	geteuid = func() int { return 0 }
	if err := Write(dir, testDomainID, Favicon, []byte("png")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "" || got[1] != "/"+testDomainID || !strings.HasPrefix(got[2], "/"+testDomainID+"/.favicon.") {
		t.Fatalf("chowned %q, want dir, the domain's dir and the new picture", got)
	}
}

// A link in place of dir or of the domain's directory makes Write and Remove
// fail: run as root, they would otherwise write or delete wherever the
// panel's user pointed it.
func TestWriteRemoveNeverFollowALink(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "logo-light.png"), []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "webmail-branding")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, testDomainID)); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(elsewhere, linkedDir); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, id string }{{dir, testDomainID}, {linkedDir, testDomainID}} {
		if err := Write(c.dir, c.id, Favicon, []byte("png")); err == nil {
			t.Fatalf("Write through a link in %s succeeded", c.dir)
		}
		if err := Remove(c.dir, c.id, LogoLight); err == nil {
			t.Fatalf("Remove through a link in %s succeeded", c.dir)
		}
	}
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) != 1 || entries[0].Name() != "logo-light.png" {
		t.Fatalf("the linked directory holds %v, want only its own file", entries)
	}
	if fi, _ := os.Stat(elsewhere); fi.Mode().Perm() != 0o700 {
		t.Fatalf("the linked directory's mode became %v", fi.Mode().Perm())
	}
}
