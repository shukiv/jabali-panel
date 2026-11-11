// addon_domains.go — cPanel addon domains and the subdomains that back them.
//
// cPanel serves an addon domain through a subdomain of the account's main
// domain: shop.example is backed by shop.main.example. The account's
// per-site userdata file is named after that subdomain, and its servername
// is the subdomain too:
//
//	userdata/shop.main.example   servername: shop.main.example
//	                             documentroot: /home/acct/shop.example
//
// The pair is written down in one place only, <userdata>/main:
//
//	addon_domains:
//	  shop.example: shop.main.example
//
// Every import pass that keyed a site by its userdata file or servername
// therefore filed an addon site under its backing subdomain: the files were
// copied into domains/<sub>/public_html, the addon's own docroot stayed empty,
// and the addon ran the box default PHP instead of its source version.
// SiteDomain is the one place that maps a servername to the domain the site is
// served as on Jabali.

package cpanel

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
)

// AddonDomainsBySubdomain returns backing subdomain → addon domain for the
// account, read from `addon_domains:` in <userdata>/main.
//
// Both names are lower-cased and must be valid domain names. An entry is kept
// only when the addon is a domain this import creates (a source zone file or a
// parsed web domain) and the subdomain is not one, so a stale or tampered map
// cannot move a site's files into a docroot the import does not own, or away
// from a domain it does create. A subdomain or addon named by more than one
// entry is ambiguous and dropped. Every dropped entry comes back as a warning
// for the run summary. A missing main file or `addon_domains: {}` yields an
// empty map and no warnings.
func AddonDomainsBySubdomain(parsed *ParsedTarball) (map[string]string, []string) {
	out := map[string]string{}
	if parsed == nil {
		return out, nil
	}
	var pairs [][2]string
	for _, root := range userdataRoots(parsed) {
		if pairs = parseYAMLStringMap(filepath.Join(root, "main"), "addon_domains"); len(pairs) > 0 {
			break
		}
	}
	if len(pairs) == 0 {
		return out, nil
	}

	planned := plannedDomainNames(parsed)
	var warnings []string
	drop := func(addon, sub, why string) {
		warnings = append(warnings, fmt.Sprintf(
			"addon_domain_skip:%s→%s: %s — the site stays under the subdomain", addon, sub, why))
	}

	subCount := map[string]int{}
	addonCount := map[string]int{}
	type entry struct{ addon, sub string }
	var valid []entry
	for _, p := range pairs {
		addon := domainops.NormalizeDomainName(p[0])
		sub := domainops.NormalizeDomainName(p[1])
		if err := domainops.ValidateDomainName(addon); err != nil {
			drop(addon, sub, "addon is not a valid domain name")
			continue
		}
		if err := domainops.ValidateDomainName(sub); err != nil {
			drop(addon, sub, "subdomain is not a valid domain name")
			continue
		}
		subCount[sub]++
		addonCount[addon]++
		valid = append(valid, entry{addon, sub})
	}
	for _, e := range valid {
		switch {
		case subCount[e.sub] > 1 || addonCount[e.addon] > 1:
			drop(e.addon, e.sub, "named by more than one addon_domains entry")
		case !planned[e.addon]:
			drop(e.addon, e.sub, "addon is not a domain this import creates (no source zone or web domain)")
		case planned[e.sub]:
			drop(e.addon, e.sub, "subdomain is itself a domain this import creates")
		default:
			out[e.sub] = e.addon
		}
	}
	sort.Strings(warnings)
	return out, warnings
}

// SiteDomain returns the domain a cPanel site is served as on Jabali: the
// addon domain when servername backs one, else servername unchanged.
func SiteDomain(addons map[string]string, servername string) string {
	if addon, ok := addons[domainops.NormalizeDomainName(servername)]; ok {
		return addon
	}
	return servername
}

// plannedDomainNames is the set of domain names ImportDomains creates rows
// for: the source zone files' basenames plus the parsed web domains,
// lower-cased. Keep in step with ImportDomains' create list.
func plannedDomainNames(parsed *ParsedTarball) map[string]bool {
	out := map[string]bool{}
	for _, z := range parsed.ZoneFiles {
		if n := domainops.NormalizeDomainName(strings.TrimSuffix(filepath.Base(z), ".db")); n != "" {
			out[n] = true
		}
	}
	for _, n := range parsed.DomainNames {
		if n = domainops.NormalizeDomainName(n); n != "" {
			out[n] = true
		}
	}
	return out
}

// SiteDocroot is one cPanel site's source document root, keyed by the domain
// the site is served as on Jabali.
type SiteDocroot struct {
	Domain  string // the addon domain for an addon site, else the userdata servername
	DocRoot string // absolute path on the source, as the userdata file records it
}

// UserdataSiteDocroots lists every site's source document root from the
// account's per-site userdata files, for the remote (SSH pull) home copy.
// It reads the first userdata dir that yields any site; a file without a
// documentroot or servername is skipped, and a file with no servername line
// is keyed by its file name. Addon sites are keyed by their addon domain (see
// SiteDomain), so their files land in the addon's own docroot.
func UserdataSiteDocroots(parsed *ParsedTarball) []SiteDocroot {
	if parsed == nil {
		return nil
	}
	addons, _ := AddonDomainsBySubdomain(parsed)
	var out []SiteDocroot
	for _, root := range userdataRoots(parsed) {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".php-fpm.yaml") ||
				strings.HasSuffix(name, ".php-fpm.yaml.transferred") ||
				strings.HasSuffix(name, "_SSL") ||
				name == "main" || name == "cache.json" || name == "scope" {
				continue
			}
			body, rerr := os.ReadFile(filepath.Join(root, name))
			if rerr != nil {
				continue
			}
			servername := name
			var docroot string
			for _, ln := range strings.Split(string(body), "\n") {
				ln = strings.TrimSpace(ln)
				if strings.HasPrefix(ln, "documentroot:") {
					docroot = strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "documentroot:")), `'"`)
				}
				if strings.HasPrefix(ln, "servername:") {
					servername = strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "servername:")), `'"`)
				}
			}
			if docroot == "" || servername == "" {
				continue
			}
			out = append(out, SiteDocroot{Domain: SiteDomain(addons, servername), DocRoot: docroot})
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

// parseYAMLStringMap pulls a flat `key:` block mapping out of a cPanel
// manifest, in file order:
//
//	addon_domains:
//	  one.example: one.main.example
//	  two.example: two.main.example
//
// Same rules as parseYAMLStringList: a value on the key's own line
// (`addon_domains: {}`) is not a block and yields nothing, and the block ends
// at the first line that is not indented. Quotes around either side are
// trimmed; lines without a `: ` separator are ignored.
func parseYAMLStringMap(path, key string) [][2]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out [][2]string
	inBlock := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, key+":") &&
				strings.TrimSpace(strings.TrimPrefix(trimmed, key+":")) == "" {
				inBlock = true
			}
			continue
		}
		if trimmed == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			break // a new top-level key ends the mapping
		}
		k, v, ok := strings.Cut(trimmed, ": ")
		if !ok {
			continue
		}
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}
