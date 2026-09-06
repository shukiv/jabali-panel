package cronvalidate

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/google/shlex"
)

// HTTP-trigger validation (GH #400 Part B).
//
// A large class of cPanel crons are `curl`/`wget` pinging a self-domain
// URL (WordPress wp-cron, app keep-alives). These genuinely need the web
// layer (query strings, DOCUMENT_ROOT-relative requires) so they can't be
// rewritten to `php`, yet jabali's wp/php-only allowlist refuses them and
// they sit dead.
//
// ValidateHTTPTrigger is a SEPARATE, equally-closed allowlist for that one
// pattern. It NEVER widens the wp/php validator. Two gates:
//
//   - Static (this file): binary curl/wget; exactly one http(s) URL whose
//     host is one of THIS account's own domains; ports 80/443 only; a tiny
//     benign-flag allowlist; no userinfo / file:// / query is fine.
//   - Runtime (`jabali cron http-trigger`): resolves the host and rejects
//     any non-routable IP (loopback / link-local / RFC1918 / ULA / metadata),
//     then pins the validated IP via `curl --resolve` so DNS can't rebind
//     between the check and the connect.
//
// Security note (honest framing for the ADR): this does NOT close an SSRF
// hole. A tenant `php` cron already has unrestricted outbound HTTP today
// (allow_url_fopen=On, empty disable_functions). The runtime guard is
// defense-in-depth + least-surprise — it keeps the curl/wget surface no
// wider than necessary, not a new security boundary.

// jabaliBinPath is the absolute path to the jabali CLI on the host
// (symlink to jabali-panel, installed 0755). The http-trigger Command's
// Argv invokes `<jabaliBinPath> cron http-trigger <url>` so the same Argv
// works whether emitted as a systemd ExecStart (cron.apply) or passed to
// `systemd-run -- …` (cron.run_now).
const jabaliBinPath = "/usr/local/bin/jabali"

// HTTP-trigger error codes (one per rejected case so callers + tests can
// assert the exact failure).
const (
	ErrCodeHTTPNotCurlWget   = "http_not_curl_wget"
	ErrCodeHTTPNoURL         = "http_no_url"
	ErrCodeHTTPMultipleURLs  = "http_multiple_urls"
	ErrCodeHTTPBadURL        = "http_bad_url"
	ErrCodeHTTPBadScheme     = "http_bad_scheme"
	ErrCodeHTTPUserinfo      = "http_userinfo"
	ErrCodeHTTPForeignHost   = "http_foreign_host"
	ErrCodeHTTPBadPort       = "http_bad_port"
	ErrCodeHTTPDangerousFlag = "http_dangerous_flag"
	ErrCodeHTTPUnexpectedArg = "http_unexpected_arg"
)

// benignHTTPFlags is the closed allowlist of curl/wget flags tolerated on
// an http-trigger cron. Everything else (writes a file, uploads, sets a
// proxy, follows redirects, sends data, runs arbitrary wgetrc) is rejected.
// The flags are not actually forwarded — the runtime wrapper always issues
// a fixed hardened `curl … -o /dev/null` GET — so this allowlist only gates
// what the operator's stored command is ALLOWED to look like.
var benignHTTPFlags = map[string]bool{
	"-s": true, "--silent": true,
	"-f": true, "--fail": true,
	"-q":           true, // wget quiet
	"--spider":     true, // wget HEAD-ish; runtime still does GET (advisor: HEAD risks not triggering the job)
	"-nv":          true, // wget no-verbose
	"--no-verbose": true,
}

// ValidateHTTPTrigger validates a constrained curl/wget self-domain HTTP
// trigger. ownedDomains is this account's domain names (any case); the URL
// host must be one of them. On success returns a Command with
// Kind==KindHTTPTrigger and Argv invoking the rebind-safe wrapper.
func ValidateHTTPTrigger(raw string, ownedDomains []string) (*Command, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, &ValidationError{Code: ErrCodeEmpty, Detail: "command cannot be empty"}
	}
	if len(raw) > 1024 {
		return nil, &ValidationError{Code: ErrCodeTooLong, Detail: fmt.Sprintf("command exceeds 1024 bytes (%d bytes)", len(raw))}
	}
	// Reject ALL control characters (newline/CR/NUL/…) outright, including
	// inside quotes — same gate as ValidateCommand. A newline that survives
	// into a single-quoted ExecStart token breaks out of the unit line and
	// injects attacker-controlled systemd directives. The URL token is
	// emitted single-quoted, so this check must run on the curl path too.
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x20 && c != '\t' {
			return nil, &ValidationError{
				Code:   ErrCodeMetacharReject,
				Detail: "command contains control characters (newline/CR/NUL/etc.) which are not allowed",
			}
		}
	}

	argv, err := shlex.Split(raw)
	if err != nil || len(argv) == 0 {
		return nil, &ValidationError{Code: ErrCodeMetacharReject, Detail: fmt.Sprintf("failed to parse command: %v", err)}
	}

	bin := filepath.Base(argv[0])
	if bin != "curl" && bin != "wget" {
		return nil, &ValidationError{Code: ErrCodeHTTPNotCurlWget, Detail: fmt.Sprintf("first token must be curl or wget, got %q", argv[0])}
	}

	var rawURL string
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://"):
			if rawURL != "" {
				return nil, &ValidationError{Code: ErrCodeHTTPMultipleURLs, Detail: "only one URL is allowed"}
			}
			rawURL = a
		case a == "-o" || a == "-O" || a == "--output-document":
			// Output flag — only the /dev/null discard form is allowed.
			if i+1 >= len(argv) || argv[i+1] != "/dev/null" {
				return nil, &ValidationError{Code: ErrCodeHTTPDangerousFlag, Detail: "output flag " + a + " is only allowed as " + a + " /dev/null"}
			}
			i++ // consume /dev/null
		case strings.HasPrefix(a, "-"):
			if !benignHTTPFlags[a] {
				return nil, &ValidationError{Code: ErrCodeHTTPDangerousFlag, Detail: "flag not allowed: " + a}
			}
		default:
			return nil, &ValidationError{Code: ErrCodeHTTPUnexpectedArg, Detail: "unexpected argument: " + a}
		}
	}
	if rawURL == "" {
		return nil, &ValidationError{Code: ErrCodeHTTPNoURL, Detail: "no http(s) URL found"}
	}

	u, perr := url.Parse(rawURL)
	if perr != nil {
		return nil, &ValidationError{Code: ErrCodeHTTPBadURL, Detail: "URL parse failed: " + perr.Error()}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, &ValidationError{Code: ErrCodeHTTPBadScheme, Detail: "scheme must be http or https, got " + u.Scheme}
	}
	if u.User != nil {
		return nil, &ValidationError{Code: ErrCodeHTTPUserinfo, Detail: "URL must not contain userinfo (user:pass@)"}
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return nil, &ValidationError{Code: ErrCodeHTTPBadURL, Detail: "URL has no host"}
	}
	if !hostInOwned(host, ownedDomains) {
		return nil, &ValidationError{Code: ErrCodeHTTPForeignHost, Detail: "host " + host + " is not one of this account's domains"}
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return nil, &ValidationError{Code: ErrCodeHTTPBadPort, Detail: "only ports 80 and 443 are allowed, got " + p}
	}

	return &Command{
		Kind: KindHTTPTrigger,
		URL:  rawURL,
		Argv: []string{jabaliBinPath, "cron", "http-trigger", rawURL},
	}, nil
}

// normalizeHost lowercases and strips a single trailing dot (FQDN root).
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hostInOwned reports whether host (already normalized) equals one of the
// account's own domain names. Exact match only — no subdomain widening; a
// cron for a subdomain must target a domain the account actually owns.
func hostInOwned(host string, ownedDomains []string) bool {
	for _, d := range ownedDomains {
		if normalizeHost(d) == host {
			return true
		}
	}
	return false
}

// IsBlockedIP reports whether ip must NOT be reached by an http-trigger
// cron. Blocks the full non-routable set: nil, unspecified (0.0.0.0 / ::),
// loopback (127/8, ::1), link-local unicast (169.254/16 incl. the
// 169.254.169.254 cloud-metadata endpoint, fe80::/10), link-local /
// general multicast, and RFC1918 + ULA (net.IP.IsPrivate covers
// 10/8, 172.16/12, 192.168/16, fc00::/7).
//
// Note: deliberately NOT IsGlobalUnicast() as the allow-test —
// IsGlobalUnicast returns true for RFC1918 addresses.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate()
}

// isHTTPTriggerBinary reports whether raw's first shell token is curl/wget.
// Used by ValidateAny to route; a parse failure routes to ValidateCommand,
// which returns the canonical metachar/parse error.
func isHTTPTriggerBinary(raw string) bool {
	argv, err := shlex.Split(raw)
	if err != nil || len(argv) == 0 {
		return false
	}
	b := filepath.Base(argv[0])
	return b == "curl" || b == "wget"
}

// ValidateAny is the single command-validation entry point for callers
// whose command may be either a wp/php/python/node exec OR a curl/wget
// http-trigger. It dispatches on the first token: curl/wget ->
// ValidateHTTPTrigger (gated on ownedDomains), everything else ->
// ValidateCommand (gated on ownedDocroots for wp/php, plus ownedHome for
// python/node scripts).
func ValidateAny(raw string, ownedDocroots, ownedDomains []string, ownedHome string) (*Command, error) {
	if isHTTPTriggerBinary(raw) {
		return ValidateHTTPTrigger(raw, ownedDomains)
	}
	return ValidateCommand(raw, ownedDocroots, ownedHome)
}
