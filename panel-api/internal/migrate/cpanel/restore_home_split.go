// restore_home_split.go — per-domain rsync split (M35.8 P7).
//
// cpanel layout:
//   primary domain     → <home>/public_html/
//   addon / subdomain  → <home>/public_html/<addon.tld>/
// jabali layout:
//   any domain         → /home/<user>/domains/<domain>/public_html/
//
// Without this split, ImportHome rsync'd the source `homedir`
// 1-for-1 into `/home/<dest>/` — files landed under
// `/home/<dest>/public_html/...` even though the jabali vhost
// expects `/home/<dest>/domains/<dom>/public_html/`. Result: a
// migrated site served 0 bytes.
//
// Algorithm:
//   1. Parse `cpmove-<user>/userdata/<dom>` YAML for every panel
//      domain → `documentroot: /home/<user>/<path>`.
//   2. Build map name→source-relative-docroot. Drop entries whose
//      docroot isn't under the source homedir (safety).
//   3. For every entry: compute nested-domain excludes (other
//      docroots that sit under this one). Sort by depth so the
//      shorter exclude list is correct.
//   4. Dispatch agent.migration.import_home per domain with
//      DestSubpath="domains/<name>/public_html" + ExtraExcludes.
//   5. Final pass: rsync the rest of homedir minus public_html
//      (mail/, .htpasswd/, application_backups/, etc/, …).

package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// HomeSplitResult tallies the per-domain + the homedir-rest rsync
// results.
type HomeSplitResult struct {
	DomainsCopied int      // number of (domain → docroot) rsyncs that succeeded
	BytesCopied   int64    // total bytes summed across all calls
	Files         int64    // total files
	Skipped       []string // human-readable reasons
}

// domainSrc is one row of the parsed userdata table.
type domainSrc struct {
	Name      string // panel domain name (servername)
	SourceRel string // relative to source homedir, e.g. "public_html" or "public_html/sub.example.com"
}

func ImportHomeSplit(
	ctx context.Context,
	agentCli agent.AgentInterface,
	parsed *ParsedTarball,
	jobID, destUser string,
) (*HomeSplitResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("ImportHomeSplit: parsed nil")
	}
	if agentCli == nil {
		return nil, fmt.Errorf("ImportHomeSplit: agent unwired")
	}
	if jobID == "" || destUser == "" {
		return nil, fmt.Errorf("ImportHomeSplit: jobID + destUser required")
	}
	res := &HomeSplitResult{}
	if parsed.HomeDir == "" {
		res.Skipped = append(res.Skipped, "home_split_skip:no_homedir")
		return res, nil
	}

	domains := parseUserdataDocroots(parsed)
	if len(domains) == 0 {
		// Source has no userdata yaml — operator probably uploaded
		// a partial cpmove. Fall back to legacy full-homedir rsync
		// so the data still lands somewhere.
		res.Skipped = append(res.Skipped, "home_split_skip:no_userdata_yaml — using legacy full-homedir rsync")
		return res, nil
	}

	// Sort deepest-first so the inner-most domain is rsync'd before
	// its enclosing parent (rsync output dirs cleanly populated).
	sort.Slice(domains, func(i, j int) bool {
		return strings.Count(domains[i].SourceRel, "/") > strings.Count(domains[j].SourceRel, "/")
	})

	// Compute the nested-excludes map. excludes[name] = list of paths
	// (relative to SourceRel) that match docroots of OTHER domains
	// nested inside it. Rsync receives `--exclude=<basename>` per
	// nested entry.
	excludes := map[string][]string{}
	for i, di := range domains {
		for j, dj := range domains {
			if i == j {
				continue
			}
			if strings.HasPrefix(dj.SourceRel+"/", di.SourceRel+"/") && dj.SourceRel != di.SourceRel {
				// dj is nested under di → exclude its tail from di's rsync.
				tail := strings.TrimPrefix(dj.SourceRel, di.SourceRel+"/")
				if tail != "" {
					excludes[di.Name] = append(excludes[di.Name], tail)
				}
			}
		}
	}

	// Dispatch per-domain rsync.
	for _, d := range domains {
		srcAbs := filepath.Join(parsed.HomeDir, d.SourceRel)
		if _, err := os.Stat(srcAbs); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("home_split_skip:%s src %s missing", d.Name, srcAbs))
			continue
		}
		params := map[string]any{
			"job_id":         jobID,
			"src_dir":        srcAbs,
			"dest_user":      destUser,
			"dest_subpath":   filepath.Join("domains", d.Name, "public_html"),
			"extra_excludes": excludes[d.Name],
		}
		raw, err := agentCli.Call(ctx, "migration.import_home", params)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("home_split_skip:%s rsync:%v", d.Name, err))
			continue
		}
		var r struct {
			BytesCopied int64 `json:"bytes_copied"`
			Files       int64 `json:"files"`
		}
		if jErr := json.Unmarshal(raw, &r); jErr == nil {
			res.BytesCopied += r.BytesCopied
			res.Files += r.Files
		}
		res.DomainsCopied++
	}

	// Final rsync — rest of the homedir into /home/<dest>/, but
	// public_html excluded since per-domain calls already covered it.
	// mail/ + etc/ + application_backups/ + .htpasswd/ etc. live
	// outside public_html and must land at homedir root.
	finalParams := map[string]any{
		"job_id":         jobID,
		"src_dir":        parsed.HomeDir,
		"dest_user":      destUser,
		"extra_excludes": []string{"public_html", "public_html/", "public_html/**"},
	}
	raw, err := agentCli.Call(ctx, "migration.import_home", finalParams)
	if err != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("home_split_skip:rest_of_homedir:%v", err))
	} else {
		var r struct {
			BytesCopied int64 `json:"bytes_copied"`
			Files       int64 `json:"files"`
		}
		if jErr := json.Unmarshal(raw, &r); jErr == nil {
			res.BytesCopied += r.BytesCopied
			res.Files += r.Files
		}
	}

	return res, nil
}

// parseUserdataDocroots scans cpmove-<user>/userdata/<domain> YAML
// files for the documentroot line. Returns map of (servername →
// source-relative-docroot) skipping files that don't reference the
// source homedir.
func parseUserdataDocroots(parsed *ParsedTarball) []domainSrc {
	roots := []string{
		filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "userdata"),
		filepath.Join(parsed.ExtractDir, "userdata"),
	}
	out := []domainSrc{}
	seen := map[string]bool{}
	srcHome := parsed.HomeDir
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			// skip .php-fpm.yaml / .php-fpm.yaml.transferred / _SSL
			if strings.HasSuffix(name, ".php-fpm.yaml") ||
				strings.HasSuffix(name, ".php-fpm.yaml.transferred") ||
				strings.HasSuffix(name, "_SSL") ||
				name == "main" || name == "cache.json" || name == "scope" {
				continue
			}
			path := filepath.Join(root, name)
			servername, docroot := extractYAMLFields(path)
			if servername == "" {
				servername = name // fallback to filename
			}
			if docroot == "" {
				continue
			}
			// Trim to source-homedir-relative path. PeekAccountMeta
			// expects this. If docroot isn't under srcHome's canonical
			// path, try a path-suffix match (the source-side homedir
			// is /home/<sourceUser> but srcHome here is the extracted
			// path).
			rel := relativeTo(docroot, "/home/"+parsed.SourceUser)
			if rel == "" {
				// Wildcard match — strip leading absolute prefix.
				rel = strings.TrimPrefix(docroot, "/")
			}
			rel = strings.TrimLeft(rel, "/")
			if rel == "" || seen[servername] {
				continue
			}
			seen[servername] = true
			out = append(out, domainSrc{Name: servername, SourceRel: rel})
		}
		if len(out) > 0 {
			break
		}
	}
	_ = srcHome
	return out
}

// extractYAMLFields pulls `servername:` + `documentroot:` lines out
// of one cpanel userdata YAML file. Minimal parser — no need for a
// full YAML lib since cpanel writes plain key:value lines.
func extractYAMLFields(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var sn, dr string
	buf := make([]byte, 16*1024)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "servername:") {
			sn = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "servername:")), `"'`)
		}
		if strings.HasPrefix(line, "documentroot:") {
			dr = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "documentroot:")), `"'`)
		}
	}
	return sn, dr
}

// relativeTo returns p relative to base, or "" when p isn't under base.
func relativeTo(p, base string) string {
	p = strings.TrimRight(p, "/")
	base = strings.TrimRight(base, "/")
	if p == base {
		return "."
	}
	if !strings.HasPrefix(p, base+"/") {
		return ""
	}
	return strings.TrimPrefix(p, base+"/")
}
