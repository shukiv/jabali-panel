package commands

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpbasedir"
)

// GH #1701 Slice 3: per-domain open_basedir and allow_url_fopen.
//
// Both are PHP_INI_SYSTEM, so PHP_VALUE cannot set them; they go through
// fastcgi_param PHP_ADMIN_VALUE, which FPM applies before the script runs and
// which replaces the pool's value for that request, tighter or looser
// (box-drilled on PHP 8.4). Like PHP_VALUE, the value sticks on the reused
// worker: the next request that sends none inherits it. One pool serves all of
// a user's domains on a PHP version, so a sibling would run with this domain's
// jail (or without its allow_url_fopen) at random. So both are PINNED on every
// PHP vhost:
//
//   - open_basedir: the domain's own value, expanded and checked again
//     (internal/phpbasedir), else the open_basedir in the pool's own file. A
//     domain value that fails the check falls back to the pool's value. When
//     the pool file cannot be read nothing is pinned for a domain without its
//     own value (today's behaviour) rather than a guess.
//   - allow_url_fopen: the domain's own value, else the box php.ini baseline
//     (a pool cannot override it: it is in neither admin allowlist), unless
//     php_flags_inherit_unknown says to pin nothing from the baseline.
//
// Only the same user's sites share a pool, so a missed pin can never reach
// another tenant.

// phpAdminValuePins returns the "directive=value" lines of the vhost's
// PHP_ADMIN_VALUE. A non-PHP vhost gets none.
func phpAdminValuePins(ctx context.Context, p *domainCreateParams) []string {
	if !p.HasPHP {
		return nil
	}
	var out []string
	if ob := phpOpenBasedirPin(p); ob != "" {
		out = append(out, "open_basedir="+ob)
	}
	if v, ok := phpAllowURLFopenPin(ctx, p); ok {
		out = append(out, "allow_url_fopen="+v)
	}
	return out
}

func phpOpenBasedirPin(p *domainCreateParams) string {
	if p.PHPOpenBasedir != "" {
		v, err := phpbasedir.Expand(p.PHPOpenBasedir, p.Username, p.DocRoot)
		if err == nil {
			return v
		}
		log.Printf("domain.create %s: not pinning the domain's open_basedir (%v); pinning the pool's", p.Domain, err)
	}
	v, err := poolOpenBasedirForVhost(p)
	if err != nil {
		log.Printf("domain.create %s: pool open_basedir unavailable, not pinning open_basedir: %v", p.Domain, err)
		return ""
	}
	return v
}

func phpAllowURLFopenPin(ctx context.Context, p *domainCreateParams) (string, bool) {
	if p.PHPAllowURLFopen != nil {
		return phpIniBoolValue(*p.PHPAllowURLFopen), true
	}
	// Same rule as every inherited pin: when the panel could not read the
	// pool's overrides, nothing is pinned from the baseline this pass.
	if p.PHPFlagsInheritUnknown {
		return "", false
	}
	raw, ok := phpValueBaseline(ctx, p.PHPVersion)["allow_url_fopen"]
	if !ok {
		return "", false
	}
	return phpIniBoolValue(phpIniBool(raw)), true
}

func phpIniBoolValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// poolOpenBasedirForVhost reads the open_basedir of the pool behind the
// vhost's FPM socket: /run/php/jabali-<slug>/fpm.sock is served by
// /etc/php/<version>/fpm/pool.d/jabali-<slug>.conf. A pool apply removes the
// slug's conf under every other version, so exactly one file is expected.
func poolOpenBasedirForVhost(p *domainCreateParams) (string, error) {
	slug := p.Username
	if p.FPMSocket != "" {
		s, ok := strings.CutPrefix(p.FPMSocket, "/run/php/jabali-")
		if ok {
			s, ok = strings.CutSuffix(s, "/fpm.sock")
		}
		if !ok {
			return "", fmt.Errorf("fpm socket %q is not a jabali pool socket", p.FPMSocket)
		}
		slug = s
	}
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") ||
		(slug != p.Username && !strings.HasPrefix(slug, p.Username+"-php")) {
		return "", fmt.Errorf("pool slug %q does not belong to user %q", slug, p.Username)
	}
	matches, _ := filepath.Glob(filepath.Join(poolConfDirGlob, "jabali-"+slug+".conf"))
	if len(matches) != 1 {
		return "", fmt.Errorf("want one pool file for %s, found %d", slug, len(matches))
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return "", err
	}
	v, present := poolOpenBasedir(string(b))
	if !present {
		return "", fmt.Errorf("%s sets no open_basedir", matches[0])
	}
	if !phpbasedir.SafePinValue(v) {
		return "", fmt.Errorf("%s open_basedir %q is not a plain path list", matches[0], v)
	}
	return v, nil
}

// buildPHPAdminValueParam joins the PHP_ADMIN_VALUE lines; "" (no line
// rendered) for a non-PHP vhost or no pins.
func buildPHPAdminValueParam(hasPHP bool, lines []string) string {
	if !hasPHP {
		return ""
	}
	return strings.Join(lines, "\n")
}
