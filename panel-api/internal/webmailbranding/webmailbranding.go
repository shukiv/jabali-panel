// Package webmailbranding holds what panel-api's API and reconciler share
// about domains' webmail branding (GH #2502): where the pictures live and the
// URLs webmail loads them from, the rules a field must pass, and the entries
// sent to the agent's webmail.branding.apply.
//
// Bulwark brands each web address it is opened on (its DOMAIN_BRANDING,
// matched on the browser's host), and each mail domain's is mail.<domain>.
// The pictures are kept by panel-api and served by nginx on every webmail
// vhost under URLPrefix, not from Bulwark's own public directory, which a
// webmail upgrade replaces.
package webmailbranding

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // decoder for image.Decode
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Dir holds each domain's pictures, one directory per domain id.
const Dir = "/var/lib/jabali-panel/webmail-branding"

// URLPrefix is where every webmail vhost serves Dir (the mail vhost
// template's location of the same name).
const URLPrefix = "/jabali-webmail-branding/"

// Asset is one of a domain's branding pictures.
type Asset string

const (
	LogoLight Asset = "logo-light"
	LogoDark  Asset = "logo-dark"
	Favicon   Asset = "favicon"
)

// ParseAsset maps a URL segment to an Asset.
func ParseAsset(s string) (Asset, bool) {
	switch a := Asset(s); a {
	case LogoLight, LogoDark, Favicon:
		return a, true
	}
	return "", false
}

const (
	// MaxUploadBytes caps an uploaded picture, as for the panel's own logo.
	MaxUploadBytes = 512 * 1024
	// MaxSide caps a picture's width and height in pixels. Webmail shows a
	// logo at most 64 pixels high; a larger one only costs the decode.
	MaxSide = 1024

	MaxAppName     = 64
	MaxCompanyName = 128
	MaxURL         = 512
)

var (
	ErrTooLarge    = errors.New("webmailbranding: picture too large")
	ErrBadPicture  = errors.New("webmailbranding: not a PNG or JPEG picture")
	ErrBadSize     = errors.New("webmailbranding: picture dimensions out of range")
	errUnsafeChars = errors.New("contains control characters")
)

// FieldError names the field a value was refused for.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

// Text checks a name field: trimmed, at most max characters, no control
// characters (webmail shows it as text, and it is written to the webmail's
// environment file).
func Text(field, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", &FieldError{field, errors.New("is not valid UTF-8")}
	}
	if utf8.RuneCountInString(s) > max {
		return "", &FieldError{field, fmt.Errorf("is longer than %d characters", max)}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", &FieldError{field, errUnsafeChars}
		}
	}
	return s, nil
}

// Link checks a link field: empty, or an http or https URL with a host and
// no user name or password in it, at most MaxURL characters. Anything else
// (javascript:, data:, a path) is refused: webmail puts it in a link on the
// sign-in page.
func Link(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > MaxURL {
		return "", &FieldError{field, fmt.Errorf("is longer than %d characters", MaxURL)}
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", &FieldError{field, errors.New("contains spaces or control characters")}
		}
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", &FieldError{field, errors.New("must be an http:// or https:// address")}
	}
	return s, nil
}

// Encode reads an uploaded picture, PNG or JPEG, at most MaxUploadBytes and
// MaxSide by MaxSide pixels, and returns it encoded again as PNG with its
// short hash. Encoding it again keeps only the pixels: whatever else the
// upload carried (text chunks, a file that is also an HTML page) is gone,
// and every stored picture is a PNG.
func Encode(r io.Reader) ([]byte, string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) > MaxUploadBytes {
		return nil, "", ErrTooLarge
	}
	return encode(raw)
}

// EncodeStored checks a picture a backup carries (GH #2502) as Encode checks
// an upload, but against max bytes: the panel stores a picture encoded again
// as PNG, which can be larger than the upload it came from. It returns the
// picture encoded again with its short hash.
func EncodeStored(raw []byte, max int) ([]byte, string, error) {
	if len(raw) > max {
		return nil, "", ErrTooLarge
	}
	return encode(raw)
}

func encode(raw []byte) ([]byte, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, "", ErrBadPicture
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxSide || cfg.Height > MaxSide {
		return nil, "", ErrBadSize
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", ErrBadPicture
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, img); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(out.Bytes())
	return out.Bytes(), hex.EncodeToString(sum[:])[:12], nil
}

// domainIDRe is a ULID, the only directory name under Dir.
var domainIDRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Path is where the domain's picture is stored under dir.
func Path(dir, domainID string, a Asset) string {
	return filepath.Join(dir, domainID, string(a)+".png")
}

// Write stores the domain's picture under dir, readable by nginx.
//
// dir and its parent belong to the panel's user, and a restore run as root
// (`jabali backup account-restore`) writes here too, so Write works from
// directory handles and never through a link: a link in place of dir or of
// the domain's directory makes it fail rather than write elsewhere. Run as
// root, it gives the directories and the picture the owner of dir's parent,
// /var/lib/jabali-panel, which install.sh gives the panel's user, so the
// panel can change and remove them later.
func Write(dir, domainID string, a Asset, data []byte) error {
	if !domainIDRe.MatchString(domainID) {
		return fmt.Errorf("webmailbranding: bad domain id %q", domainID)
	}
	pfd, owner, err := openParent(dir)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	dfd, err := openDirAt(pfd, filepath.Base(dir), true, owner)
	if err != nil {
		return fmt.Errorf("webmailbranding: %s: %w", dir, err)
	}
	defer unix.Close(dfd)
	ddfd, err := openDirAt(dfd, domainID, true, owner)
	if err != nil {
		return fmt.Errorf("webmailbranding: %s: %w", filepath.Join(dir, domainID), err)
	}
	defer unix.Close(ddfd)

	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := "." + string(a) + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	fd, err := unix.Openat(ddfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	renamed := false
	defer func() {
		if !renamed {
			_ = unix.Unlinkat(ddfd, tmp, 0)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if owner != nil {
		if err := fchown(int(f.Fd()), owner.uid, owner.gid); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(ddfd, tmp, ddfd, string(a)+".png"); err != nil {
		return err
	}
	renamed = true
	return nil
}

// geteuid and fchown are swapped by tests.
var (
	geteuid = os.Geteuid
	fchown  = unix.Fchown
)

type fileOwner struct{ uid, gid int }

// openParent opens dir's parent and, run as root, returns its owner.
func openParent(dir string) (int, *fileOwner, error) {
	fd, err := unix.Open(filepath.Dir(dir), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	if geteuid() != 0 {
		return fd, nil, nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, nil, err
	}
	return fd, &fileOwner{int(st.Uid), int(st.Gid)}, nil
}

// openDirAt opens the directory name under dirfd, never through a link,
// making it first when mk. It leaves it 0755, which nginx needs to reach
// the pictures (the umask can narrow a new one), and gives it to owner
// unless that is nil.
func openDirAt(dirfd int, name string, mk bool, owner *fileOwner) (int, error) {
	if mk {
		if err := unix.Mkdirat(dirfd, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if !mk {
		return fd, nil
	}
	if owner != nil {
		if err := fchown(fd, owner.uid, owner.gid); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	if err := unix.Fchmod(fd, 0o755); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// Remove deletes the domain's picture under dir. A missing one is not an
// error. Like Write it never goes through a link.
func Remove(dir, domainID string, a Asset) error {
	if !domainIDRe.MatchString(domainID) {
		return fmt.Errorf("webmailbranding: bad domain id %q", domainID)
	}
	pfd, _, err := openParent(dir)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	fd := pfd
	for _, name := range []string{filepath.Base(dir), domainID} {
		next, err := openDirAt(fd, name, false, nil)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("webmailbranding: %s: %w", name, err)
		}
		defer unix.Close(next)
		fd = next
	}
	if err := unix.Unlinkat(fd, string(a)+".png", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// RemoveAll deletes all of the domain's pictures under dir. It goes by path,
// so only the panel's own process calls it, never a restore run as root.
func RemoveAll(dir, domainID string) error {
	if !domainIDRe.MatchString(domainID) {
		return fmt.Errorf("webmailbranding: bad domain id %q", domainID)
	}
	return os.RemoveAll(filepath.Join(dir, domainID))
}

// Prune deletes the pictures of every domain under dir that keep doesn't
// hold: a deleted domain's branding row goes with it, its pictures stay
// until here. It touches only directories named like a domain id. Like
// RemoveAll, only the panel's own process calls it.
func Prune(dir string, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !domainIDRe.MatchString(e.Name()) || keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PictureURL is where webmail loads the domain's picture, on whichever
// webmail host it is open: the hash makes a changed picture a new URL.
func PictureURL(domainID string, a Asset, hash string) string {
	if hash == "" {
		return ""
	}
	return URLPrefix + domainID + "/" + string(a) + ".png?v=" + hash
}

// AgentDomain is one entry of webmail.branding.apply's domains.
type AgentDomain = agentwire.WebmailBrandingDomain

// AgentDomains turns the branding rows into the agent's entries: one per
// row that applies and sets anything, for its web address mail.<domain>.
// A domain with only a light logo shows it in dark mode too, rather than
// the server's dark logo.
//
// The agent refuses the whole list over one entry it doesn't take, which
// would freeze every domain's branding, so an entry that fails the agent's
// own check (a name an old import stored outside the FQDN shape, say) or
// repeats a host is left out.
func AgentDomains(rows []repository.WebmailBrandingRow) []AgentDomain {
	out := []AgentDomain{}
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		if !r.Applies || r.Empty() {
			continue
		}
		light := PictureURL(r.DomainID, LogoLight, r.LogoLight)
		dark := PictureURL(r.DomainID, LogoDark, r.LogoDark)
		if dark == "" {
			dark = light
		}
		d := AgentDomain{
			Host:         "mail." + strings.ToLower(r.DomainName),
			AppName:      r.AppName,
			CompanyName:  r.CompanyName,
			WebsiteURL:   r.WebsiteURL,
			PrivacyURL:   r.PrivacyURL,
			ImprintURL:   r.ImprintURL,
			LogoLightURL: light,
			LogoDarkURL:  dark,
			FaviconURL:   PictureURL(r.DomainID, Favicon, r.Favicon),
		}
		if seen[d.Host] || d.Validate() != nil {
			continue
		}
		seen[d.Host] = true
		out = append(out, d)
	}
	return out
}
