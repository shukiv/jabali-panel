// Package wordpressssh discovers a WordPress install on an SSH-reachable source
// (GH #647). Unlike the cPanel/DA/Hestia importers it does NOT implement the
// account-shaped migrate.Discoverer interface — a single WP site has no account
// picker and AccountManifest (mailboxes/dns/cron/…) is a bad fit. It exposes a
// bespoke WordPressFacts + a Connect that reuses the proven, tested primitives:
// migrate.DialTCP (rebind-safe SSRF) and migrate.PinningHostKeyCallback.
//
// SECURITY: every path interpolated into a remote command is shellQuote'd, so a
// hostile source_path / discovered dir can never break out of the command.
package wordpressssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

const defaultCmdTimeout = 30 * time.Second

// WordPressFacts is what discovery yields — enough to drive the pull + import +
// UI domain-confirm without the account-panel machinery.
type WordPressFacts struct {
	Root        string `json:"root"`         // absolute WP root (contains wp-config.php)
	Home        string `json:"home"`         // WP `home` option (empty if WP-CLI absent)
	SiteURL     string `json:"siteurl"`      // WP `siteurl` option
	TablePrefix string `json:"table_prefix"` // from wp-config.php
	WPVersion   string `json:"wp_version,omitempty"`
	PHPVersion  string `json:"php_version,omitempty"`
	BytesTotal  int64  `json:"bytes_total"` // du -sb of the root
	Multisite   bool   `json:"multisite"`
	WPCLI       bool   `json:"wp_cli"` // whether WP-CLI was available (else home/siteurl are best-effort)
}

// Session wraps an authenticated SSH client. Caller owns Close.
type Session struct{ client *ssh.Client }

func (s *Session) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Connect dials the source (SSRF + rebind-safe via migrate.DialTCP), verifies
// the host key (migrate.PinningHostKeyCallback), and authenticates. Mirrors
// cpanel.Connect minus the principal probe.
func Connect(ctx context.Context, host string, port int, user string, secret migrate.SecretRef, allowPrivate bool) (*Session, error) {
	if port == 0 {
		port = 22
	}
	auth, err := loadSecret(secret.Path)
	if err != nil {
		return nil, fmt.Errorf("wordpressssh.Connect: load secret: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: migrate.PinningHostKeyCallback(secret.ExpectedHostKey),
		Timeout:         15 * time.Second,
	}
	conn, err := migrate.DialTCP(ctx, host, port, allowPrivate, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("wordpressssh.Connect: tcp dial: %w", err)
	}
	addr := host + ":" + strconv.Itoa(port)
	c, ch, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		// Auth rejected — most often a Cloudways/managed host that disables
		// password auth for the master user and requires a key. Catch both
		// "attempted methods [none]" and "[none password]".
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "no supported methods remain") {
			return nil, fmt.Errorf("the source rejected the credentials. If this is Cloudways or a managed host, it usually disables password login for the master user — use an SSH PRIVATE KEY instead of a password. Also verify the SSH user and that the key is authorized on the source. (%w)", err)
		}
		return nil, fmt.Errorf("wordpressssh.Connect: ssh handshake: %w", err)
	}
	return &Session{client: ssh.NewClient(c, ch, reqs)}, nil
}

// run executes one command over a fresh SSH session, bounded by timeout.
func (s *Session) run(ctx context.Context, timeout time.Duration, cmd string) ([]byte, error) {
	if timeout == 0 {
		timeout = defaultCmdTimeout
	}
	subctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh new session: %w", err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err := <-done:
		if err != nil {
			return stdout.Bytes(), fmt.Errorf("ssh exec %q: %w (stderr=%q)", cmd, err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), nil
	case <-subctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("ssh exec %q: timed out after %s", cmd, timeout)
	}
}

// shellQuote single-quotes a value so it is a single, literal shell argument —
// the only safe way to interpolate an untrusted path into a remote command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// autoDetectPatterns are FIXED, trusted glob patterns expanded on the source
// when the operator gives no path. Because they contain no user input they are
// safe to pass unquoted to `ls -d` (we WANT the remote shell to glob them);
// the RESULTING dirs are shellQuote'd before any further use.
// Cloudways apps live under /home/master/applications/*/public_html.
var autoDetectPatterns = []string{
	"~/public_html",
	"/home/*/public_html",
	"/home/master/applications/*/public_html", // Cloudways
}

// DiscoverWordPress locates the WP root (verifying it holds wp-config.php),
// then reads home/siteurl/prefix/size/multisite. hint is the operator-supplied
// path ("" → auto-detect). WP-CLI is preferred; without it, home/siteurl stay
// empty (the UI asks the operator to confirm the domain) but prefix/size still
// resolve.
func DiscoverWordPress(ctx context.Context, s *Session, hint string) (*WordPressFacts, error) {
	root, err := s.locateRoot(ctx, hint)
	if err != nil {
		return nil, err
	}
	f := &WordPressFacts{Root: root}

	// Table prefix from wp-config.php. Matches BOTH quote styles
	// ($table_prefix = 'wp_'; and "wp_") with flexible spacing; prefix chars
	// are [A-Za-z0-9_]. An empty result is a HARD error — the import's table
	// rewrite keys off the prefix, so a wrong/empty prefix silently corrupts.
	if out, err := s.run(ctx, defaultCmdTimeout,
		"grep -oE \"table_prefix[[:space:]]*=[[:space:]]*['\\\"][A-Za-z0-9_]+['\\\"]\" "+shellQuote(root+"/wp-config.php")+
			" | head -1 | grep -oE \"['\\\"][A-Za-z0-9_]+['\\\"]\" | tr -d \"'\\\"\""); err == nil {
		f.TablePrefix = strings.TrimSpace(string(out))
	}
	if f.TablePrefix == "" {
		return nil, fmt.Errorf("wordpressssh: could not read table_prefix from %s/wp-config.php (unexpected format)", root)
	}

	// WP-CLI present AND sees a real install? Only then trust its option output.
	// A bare `command -v wp` is not enough — a broken install would leak stdout
	// warnings into Home/SiteURL. Gate on error-checked `core is-installed`, and
	// the wp() helper returns "" on any error (never trusts garbage stdout).
	if _, err := s.run(ctx, defaultCmdTimeout, "command -v wp >/dev/null 2>&1 && echo yes"); err == nil {
		wpRun := func(args string) (string, error) {
			out, err := s.run(ctx, defaultCmdTimeout, "wp --allow-root --path="+shellQuote(root)+" --skip-plugins --skip-themes "+args+" 2>/dev/null")
			return strings.TrimSpace(string(out)), err
		}
		if _, err := wpRun("core is-installed"); err == nil {
			f.WPCLI = true
			if v, e := wpRun("option get home"); e == nil {
				f.Home = v
			}
			if v, e := wpRun("option get siteurl"); e == nil {
				f.SiteURL = v
			}
			if v, e := wpRun("core version"); e == nil {
				f.WPVersion = v
			}
			if _, e := wpRun("core is-installed --network"); e == nil {
				f.Multisite = true
			}
		}
	}

	// Size (best-effort; du in bytes).
	if out, err := s.run(ctx, 60*time.Second, "du -sb "+shellQuote(root)+" 2>/dev/null | cut -f1"); err == nil {
		if n, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil {
			f.BytesTotal = n
		}
	}
	// PHP version (best-effort).
	if out, err := s.run(ctx, defaultCmdTimeout, "php -r 'echo PHP_VERSION;' 2>/dev/null"); err == nil {
		f.PHPVersion = strings.TrimSpace(string(out))
	}
	return f, nil
}

// hasWPConfig tests, safely, whether dir contains wp-config.php.
func (s *Session) hasWPConfig(ctx context.Context, dir string) bool {
	_, err := s.run(ctx, defaultCmdTimeout, "test -f "+shellQuote(dir+"/wp-config.php")+" && echo ok")
	return err == nil
}

// locateRoot returns the WP root. A user hint is treated as a LITERAL path —
// shellQuote'd and NEVER glob-expanded (a hint with `*`/`;`/`~` must not reach
// an unquoted shell). With no hint, only the FIXED autoDetectPatterns are
// globbed on the source. Either way the hard gate is wp-config.php presence.
func (s *Session) locateRoot(ctx context.Context, hint string) (string, error) {
	if h := strings.TrimSpace(hint); h != "" {
		if s.hasWPConfig(ctx, h) {
			return h, nil
		}
		return "", fmt.Errorf("wordpressssh: no wp-config.php at the supplied path %q", h)
	}
	for _, pat := range autoDetectPatterns { // fixed literals — safe to glob unquoted
		out, err := s.run(ctx, defaultCmdTimeout, "ls -d "+pat+" 2>/dev/null")
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if d := strings.TrimSpace(l); d != "" && s.hasWPConfig(ctx, d) {
				return d, nil
			}
		}
	}
	return "", fmt.Errorf("wordpressssh: no wp-config.php found under %v", autoDetectPatterns)
}

// loadSecret reads the per-job credential file (SSH_PASSWORD / SSH_PRIVATE_KEY /
// SSH_PRIVATE_KEY_B64) into ssh.AuthMethods, zeroing the raw bytes after. Mirrors
// the cpanel read-once-then-zero pattern.
func loadSecret(path string) ([]ssh.AuthMethod, error) {
	if path == "" {
		return nil, errors.New("secret path empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	var auths []ssh.AuthMethod
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "SSH_PASSWORD":
			auths = append(auths, migrate.SSHPasswordAuthMethods(v)...)
		case "SSH_PRIVATE_KEY":
			signer, perr := ssh.ParsePrivateKey([]byte(v))
			if perr != nil {
				return nil, fmt.Errorf("parse SSH_PRIVATE_KEY: %w", perr)
			}
			auths = append(auths, ssh.PublicKeys(signer))
		case "SSH_PRIVATE_KEY_B64":
			pem, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if derr != nil {
				return nil, fmt.Errorf("base64-decode SSH_PRIVATE_KEY_B64: %w", derr)
			}
			signer, perr := ssh.ParsePrivateKey(pem)
			zero(pem)
			if perr != nil {
				return nil, fmt.Errorf("parse SSH_PRIVATE_KEY_B64: %w", perr)
			}
			auths = append(auths, ssh.PublicKeys(signer))
		}
	}
	if len(auths) == 0 {
		return nil, errors.New("no SSH_PASSWORD or SSH_PRIVATE_KEY in secret file")
	}
	return auths, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// WordPressInstall is one WP install found by a source scan.
type WordPressInstall struct {
	Root    string `json:"root"`
	SiteURL string `json:"siteurl,omitempty"`
}

// ScanWordPress finds WordPress installs on the source under the usual roots
// (/var/www, /home — incl. Cloudways /home/master/applications) by locating
// wp-config.php, then best-effort reads each site's URL. Bounded depth + count
// so a huge filesystem can't stall the scan. GH #647.
func ScanWordPress(ctx context.Context, s *Session) ([]WordPressInstall, error) {
	// -not -path '*/wp-content/*' skips nested wp-config copies inside plugins.
	out, err := s.run(ctx, 90*time.Second,
		"find /var/www /home -maxdepth 7 -name wp-config.php -not -path '*/wp-content/*' 2>/dev/null | head -100")
	if err != nil {
		return nil, fmt.Errorf("scan find: %w", err)
	}
	var installs []WordPressInstall
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasSuffix(line, "/wp-config.php") {
			continue
		}
		root := strings.TrimSuffix(line, "/wp-config.php")
		if seen[root] {
			continue
		}
		seen[root] = true
		inst := WordPressInstall{Root: root}
		// Best-effort siteurl (WP-CLI as root; short timeout each).
		if out, err := s.run(ctx, 15*time.Second,
			"wp --allow-root --path="+shellQuote(root)+" --skip-plugins --skip-themes option get siteurl 2>/dev/null"); err == nil {
			inst.SiteURL = strings.TrimSpace(string(out))
		}
		installs = append(installs, inst)
	}
	return installs, nil
}
