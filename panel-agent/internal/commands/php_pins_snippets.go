package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GH #1701: the per-domain PHP values (PHP_VALUE, PHP_ADMIN_VALUE and the
// domain's environment variables) reach PHP-FPM only from a location that
// sends them, and a value sent once sticks on the reused worker for the next
// request that sends none. The vhost's own PHP locations send them; the PHP
// locations of the per-install app snippets in /etc/nginx/jabali/<domain>/
// (Drupal/Joomla/OpenEMR/InvoiceShelf/Flarum subdir installs, the osTicket
// PATH_INFO handler) did not. A request there ran with whatever the previous
// request on that worker left behind: a sibling domain's display_errors=On,
// or its narrowed open_basedir, which breaks the app.
//
// So writeVhost keeps a php-pins.params file beside the snippets with the same
// fastcgi_param lines as the vhost's PHP locations, and every snippet PHP
// location includes it. The include is a glob, so nginx accepts it before the
// file exists (a fresh install before the next vhost write). Snippets written
// before this change get the include added by writeVhost.

const phpPinsFileName = "php-pins.params"

// phpPinsInclude is the include line a snippet's PHP location carries.
func phpPinsInclude(domain string) string {
	return "include " + filepath.Join(nginxJabaliDir, domain, "php-pins*.params") + ";"
}

// renderPHPPinParams renders the per-domain PHP fastcgi_param lines of a
// vhost, exactly as its PHP locations carry them. "" for a non-PHP vhost.
func renderPHPPinParams(vd vhostData) string {
	if !vd.HasPHP {
		return ""
	}
	var b strings.Builder
	if vd.PHPValueParam != "" {
		b.WriteString("fastcgi_param PHP_VALUE \"" + vd.PHPValueParam + "\";\n")
	}
	if vd.PHPAdminValueParam != "" {
		b.WriteString("fastcgi_param PHP_ADMIN_VALUE \"" + vd.PHPAdminValueParam + "\";\n")
	}
	if vd.EnvParams != "" {
		b.WriteString(vd.EnvParams + "\n")
	}
	return b.String()
}

// pinFileChange is one file writeVhost writes (or removes) beside the
// snippets, with what was there before so a failed nginx -t can put it back.
type pinFileChange struct {
	path    string
	want    []byte
	remove  bool
	prev    []byte
	existed bool
}

// GH #329: a domain bound to a per-version pool passes PHP to that pool's own
// socket, but every app snippet generator writes the owner's default pool
// socket, so an app's PHP ran on the default pool (its PHP version, its
// settings). The vhost is the source of truth: a snippet's fastcgi_pass to one
// of the owner's pools follows the vhost's socket, when the snippet is written
// at install and on every vhost write after (so a later PHP version change
// carries over). A pass anywhere else (another account's pool, the distro pool,
// a TCP backend) is left alone, and nothing moves unless the vhost's socket is
// one of the owner's own pools: a domain's PHP never reaches another account.
var snippetFastCGIPassRE = regexp.MustCompile(`(?m)^(\s*fastcgi_pass\s+unix:)(\S+?)(;)`)

// ownerPoolSocket reports whether socket is one of owner's jabali pools: the
// default /run/php/jabali-<owner>/fpm.sock or a per-version
// /run/php/jabali-<owner>-php<X.Y>/fpm.sock.
func ownerPoolSocket(socket, owner string) bool {
	m := fpmSocketRE.FindStringSubmatch(socket)
	if m == nil || owner == "" {
		return false
	}
	if m[1] == owner {
		return true
	}
	ver, ok := strings.CutPrefix(m[1], owner+"-php")
	if !ok {
		return false
	}
	_, _, ok = splitPHPVersion(ver)
	return ok
}

// withVhostFPMSocket points every fastcgi_pass of a snippet that targets one
// of owner's pools at socket, the vhost's. (nil, false) when socket is not one
// of owner's pools or every such pass already targets it.
func withVhostFPMSocket(body []byte, owner, socket string) ([]byte, bool) {
	if !ownerPoolSocket(socket, owner) {
		return nil, false
	}
	changed := false
	out := snippetFastCGIPassRE.ReplaceAllFunc(body, func(line []byte) []byte {
		m := snippetFastCGIPassRE.FindSubmatch(line)
		if cur := string(m[2]); cur == socket || !ownerPoolSocket(cur, owner) {
			return line
		}
		changed = true
		return []byte(string(m[1]) + socket + string(m[3]))
	})
	if !changed {
		return nil, false
	}
	return out, true
}

// vhostPoolSocket is the socket the domain's vhost passes PHP to; "" when the
// vhost cannot be read or runs no PHP.
func vhostPoolSocket(domain string) string {
	if !domainRegex.MatchString(domain) {
		return ""
	}
	vhost, err := os.ReadFile(filepath.Join(nginxSitesDir, domain+".conf"))
	if err != nil {
		return ""
	}
	if m := vhostFastCGIPassRE.FindSubmatch(vhost); m != nil {
		return string(m[1])
	}
	return ""
}

// planPHPPinFiles returns the changes that bring dir (/etc/nginx/jabali/<domain>)
// in line: the pins file holds params (removed when the vhost has no PHP), every
// snippet PHP location includes it, and every snippet passes to socket, the
// vhost's pool ("" for a vhost without PHP: no snippet socket moves). owner is
// the domain's account. Nothing when all is already so.
func planPHPPinFiles(dir, domain, owner, params, socket string) []pinFileChange {
	var out []pinFileChange
	pinsPath := filepath.Join(dir, phpPinsFileName)
	prev, err := os.ReadFile(pinsPath)
	existed := err == nil
	if params != "" {
		want := []byte("# Auto-generated by jabali-agent. Do not edit.\n" +
			"# " + domain + "'s per-domain PHP values (GH #1701), included by the PHP\n" +
			"# locations of the app snippets in this directory.\n" + params)
		if !existed || !bytes.Equal(prev, want) {
			out = append(out, pinFileChange{path: pinsPath, want: want, prev: prev, existed: existed})
		}
	} else if existed {
		out = append(out, pinFileChange{path: pinsPath, remove: true, prev: prev, existed: true})
	}

	snippets, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
	sort.Strings(snippets)
	for _, p := range snippets {
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		want := body
		if moved, ok := withVhostFPMSocket(want, owner, socket); ok {
			want = moved
		}
		if patched, ok := withPHPPinsInclude(want, domain); ok {
			want = patched
		}
		if !bytes.Equal(want, body) {
			out = append(out, pinFileChange{path: p, want: want, prev: body, existed: true})
		}
	}
	return out
}

// withPHPPinsInclude adds the pins include after every `include
// fastcgi_params;` of a snippet that passes to PHP-FPM. A snippet that
// already includes the pins, or sets its own PHP_VALUE / PHP_ADMIN_VALUE (an
// operator's hand-written one), is left alone.
func withPHPPinsInclude(body []byte, domain string) ([]byte, bool) {
	s := string(body)
	if !strings.Contains(s, "fastcgi_pass") || strings.Contains(s, "php-pins") ||
		strings.Contains(s, "PHP_VALUE") || strings.Contains(s, "PHP_ADMIN_VALUE") {
		return nil, false
	}
	var b strings.Builder
	changed := false
	for _, l := range strings.SplitAfter(s, "\n") {
		b.WriteString(l)
		if strings.TrimSpace(l) != "include fastcgi_params;" {
			continue
		}
		if !strings.HasSuffix(l, "\n") {
			b.WriteString("\n")
		}
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		b.WriteString(indent + phpPinsInclude(domain) + "\n")
		changed = true
	}
	if !changed {
		return nil, false
	}
	return []byte(b.String()), true
}

// snippetWithPHPPins is an app snippet as written at install by owner's
// domain: passing to the vhost's pool, with the pins include in its PHP
// locations, so it is right from the first request. Without a readable PHP
// vhost the socket stays as written; the next vhost write settles it.
func snippetWithPHPPins(body []byte, domain, owner string) []byte {
	if moved, ok := withVhostFPMSocket(body, owner, vhostPoolSocket(domain)); ok {
		body = moved
	}
	if patched, ok := withPHPPinsInclude(body, domain); ok {
		return patched
	}
	return body
}

// applyPinFileChanges writes the changes atomically, creating the directory
// for a new pins file. On a failure it puts back what it already changed.
func applyPinFileChanges(changes []pinFileChange) error {
	for i, c := range changes {
		var err error
		if c.remove {
			err = os.Remove(c.path)
		} else if err = os.MkdirAll(filepath.Dir(c.path), 0o755); err == nil {
			err = writeFileAtomic(c.path, c.want, 0o644)
		}
		if err != nil {
			revertPinFileChanges(changes[:i])
			return fmt.Errorf("php pins %s: %w", c.path, err)
		}
	}
	return nil
}

// revertPinFileChanges restores what each change replaced, or removes a file
// it created.
func revertPinFileChanges(changes []pinFileChange) {
	for _, c := range changes {
		if c.existed {
			_ = writeFileAtomic(c.path, c.prev, 0o644)
		} else {
			_ = os.Remove(c.path)
		}
	}
}
