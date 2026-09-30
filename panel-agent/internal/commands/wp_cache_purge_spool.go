package commands

// wp_cache_purge_spool.go — GH #611. Auto-purge the nginx FastCGI page cache
// when WordPress content changes. Tenant PHP is unprivileged and cannot delete
// the root-owned nginx cache files, so the jabali-cache plugin drops a small
// purge-request JSON into a shared, sticky spool dir; this root-side agent
// watcher picks it up, validates the requesting tenant actually owns the host,
// and calls the same nginx.cache.purge path (targeted by URL, GH #619).
//
// Same supervised-loop shape as StartLoginAllowlistWatcher (#598): tenant
// writes a request, a trusted root component acts on it — the plugin never
// gets panel credentials or root.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// wpPurgeSpoolDir is created by install.sh (tmpfs, mode 1777 sticky so any
// tenant can drop a request but only its owner — or root — can remove it).
// wpPurgeSpoolDir is a var (not const) so tests can point the watcher at a temp
// dir instead of the real /run path.
var wpPurgeSpoolDir = "/run/jabali-wp-purge"

// Hooks for tests: the root-owned vhost dir the ownership check reads, the
// nginx purge, and the warm started after a successful purge.
var (
	wpPurgeSitesDir  = "/etc/nginx/sites-available"
	wpPurgeRunPurge  = nginxCachePurgeHandler
	wpPurgeStartWarm = startWarmAfterPurge
)

const (
	wpPurgePollInterval = 2 * time.Second
	wpPurgeMaxFileBytes = 8 * 1024 // a purge request is tiny; ignore anything larger.
	wpPurgeMaxPaths     = 64       // cap paths per request.
	wpPurgeMaxPerTick   = 500      // bound total work per poll so a flood can't wedge the loop.
	// JAB-63: fairness + flood containment on the shared 1777 spool.
	wpPurgeMaxPerUIDTick = 50               // one tenant may consume at most this many of the per-tick budget.
	wpPurgeMaxScan       = 5000             // cap files stat'd per tick so a huge flood can't pin the loop scanning.
	wpPurgeStaleAfter    = 5 * time.Minute  // a legit purge is handled within a poll; older files are flood/failed leftovers — reap them so the spool can't fill /run.
)

type wpPurgeRequest struct {
	Host  string   `json:"host"`
	Paths []string `json:"paths"`
}

// StartWpCachePurgeWatcher launches the supervised spool watcher.
func StartWpCachePurgeWatcher(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	// Ensure the spool dir exists with sticky 1777 (tenants create requests,
	// only the owner/root removes them) — so a plain `jabali update` on an
	// existing host enables auto-purge without waiting for a full install.sh run.
	// install.sh's tmpfiles.d entry handles reboot persistence.
	_ = os.MkdirAll(wpPurgeSpoolDir, 0o777)
	_ = os.Chmod(wpPurgeSpoolDir, os.ModeSticky|0o777)
	go func() {
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			runWpPurgeTick(ctx, log)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wpPurgePollInterval):
			}
		}
	}()
}

func runWpPurgeTick(ctx context.Context, log *slog.Logger) {
	entries, err := os.ReadDir(wpPurgeSpoolDir)
	if err != nil {
		return // dir absent until install.sh creates it / first request; not an error.
	}
	// JAB-63: the spool is a shared 1777 dir any tenant can write. Process it
	// with a per-UID fair share so one tenant flooding .json files can't consume
	// the whole per-tick budget and starve everyone else's post-save purge. Also
	// reject non-regular files, reap stale leftovers so a flood can't fill /run,
	// and bound the scan so a huge backlog can't pin the loop.
	perUID := map[uint64]int{}
	deferred := map[uint64]int{}
	now := time.Now()
	processed, scanned := 0, 0
	var items []*wpPurgeItem
	for _, e := range entries {
		if processed >= wpPurgeMaxPerTick || scanned >= wpPurgeMaxScan {
			break
		}
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(wpPurgeSpoolDir, e.Name())
		// Lstat (not Stat): reject symlinks/fifos/etc. explicitly instead of
		// following them, and get the owner UID + mtime for fairness/staleness.
		fi, lerr := os.Lstat(path)
		if lerr != nil {
			continue
		}
		scanned++
		if !fi.Mode().IsRegular() {
			_ = os.Remove(path) // non-regular file in the spool — never trust it.
			continue
		}
		if now.Sub(fi.ModTime()) > wpPurgeStaleAfter {
			_ = os.Remove(path) // stale: a real purge is handled within a poll.
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		uid := uint64(st.Uid)
		if perUID[uid] >= wpPurgeMaxPerUIDTick {
			deferred[uid]++ // this tenant already got its fair share this tick.
			continue
		}
		perUID[uid]++
		processed++
		if it := collectWpPurgeFile(path, log); it != nil {
			items = append(items, it)
		}
	}
	for uid, d := range deferred {
		log.WarnContext(ctx, "wp-purge: per-UID tick cap exceeded (possible flood)",
			"uid", uid, "deferred", d, "cap", wpPurgeMaxPerUIDTick)
	}
	if scanned >= wpPurgeMaxScan {
		log.WarnContext(ctx, "wp-purge: scan cap hit; spool backlog deferred to next tick", "scanned", scanned)
	}
	// JAB-93: collapse the collected files into one nginx purge per (uid, host)
	// so a bulk edit / import / WooCommerce sweep is one host purge, not one
	// per spool file.
	coalesceAndPurge(ctx, items, log)
}

// nginxVhostDocroot returns the server docroot from the host's nginx vhost
// (the first uncommented `root` directive). The vhost lives under root-owned
// /etc/nginx and is the panel's authoritative host->docroot mapping (GH #630).
func nginxVhostDocroot(vhostPath string) (string, error) {
	b, err := os.ReadFile(vhostPath)
	if err != nil {
		return "", err
	}
	m := nginxRootRE.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("no root directive in %s", vhostPath)
	}
	return strings.TrimSpace(string(m[1])), nil
}

var nginxRootRE = regexp.MustCompile(`(?m)^\s*root\s+([^;]+);`)

// nginxVhostCacheOn reports whether the host's vhost has the page cache on (a
// `fastcgi_cache <zone>;` directive, rendered only when the domain's cache is
// enabled). A purge on a cache-off vhost succeeds as a no-op, and warming it
// would only add PHP renders nginx never stores.
func nginxVhostCacheOn(vhostPath string) bool {
	b, err := os.ReadFile(vhostPath)
	if err != nil {
		return false
	}
	for _, m := range nginxFastcgiCacheRE.FindAllSubmatch(b, -1) {
		if string(m[1]) != "off" {
			return true
		}
	}
	return false
}

var nginxFastcgiCacheRE = regexp.MustCompile(`(?m)^\s*fastcgi_cache\s+([^;\s]+)\s*;`)

// wpPurgeItem is one well-formed spool request collected in a tick, before the
// per-(uid, host) coalescing pass. Ownership is confirmed later, once per group.
type wpPurgeItem struct {
	path     string
	uid      uint64
	username string
	host     string
	paths    []string
}

// collectWpPurgeFile parses one spool file into a wpPurgeItem, or removes it and
// returns nil when it is malformed (bad size, unreadable owner, bad JSON, or a
// host that fails the domain regex). A well-formed request is NOT removed here —
// coalesceAndPurge removes it after the grouped purge. A var so the fairness
// test can stub it.
var collectWpPurgeFile = func(path string, log *slog.Logger) *wpPurgeItem {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 || fi.Size() > wpPurgeMaxFileBytes {
		_ = os.Remove(path)
		return nil
	}
	// Owner uid: the request is only trusted for the tenant that wrote it (the
	// sticky spool dir preserves the writer's uid).
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		_ = os.Remove(path)
		return nil
	}
	usr, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil || usr.Username == "" {
		_ = os.Remove(path)
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		_ = os.Remove(path)
		return nil
	}
	var req wpPurgeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		_ = os.Remove(path)
		return nil
	}
	if !domainRegex.MatchString(req.Host) {
		_ = os.Remove(path)
		return nil
	}
	paths := req.Paths
	if len(paths) > wpPurgeMaxPaths {
		paths = paths[:wpPurgeMaxPaths]
	}
	return &wpPurgeItem{path: path, uid: uint64(st.Uid), username: usr.Username, host: req.Host, paths: paths}
}

// mergeGroupPaths merges the path lists of one (uid, host) group. A whole-domain
// purge (empty paths) anywhere in the group collapses it to a single whole-domain
// purge; otherwise the paths are de-duplicated and capped. Pure — unit tested.
func mergeGroupPaths(grp []*wpPurgeItem) (outPaths []string, wholeDomain bool) {
	seen := map[string]struct{}{}
	merged := []string{}
	for _, it := range grp {
		if len(it.paths) == 0 {
			return []string{}, true // whole-domain wins.
		}
		for _, pth := range it.paths {
			if _, dup := seen[pth]; dup {
				continue
			}
			seen[pth] = struct{}{}
			merged = append(merged, pth)
		}
	}
	if len(merged) > wpPurgeMaxPaths {
		merged = merged[:wpPurgeMaxPaths]
	}
	return merged, false
}

// coalesceAndPurge groups the collected requests by (uid, host), validates host
// ownership ONCE per group against the root-owned nginx vhost docroot, merges the
// path lists, and issues a single nginx cache purge per group. Every consumed
// file is removed. All files in a group share uid+host, so a single ownership
// check (GH #630 semantics) covers the whole group. (JAB-93)
func coalesceAndPurge(ctx context.Context, items []*wpPurgeItem, log *slog.Logger) {
	type groupKey struct {
		uid  uint64
		host string
	}
	groups := map[groupKey][]*wpPurgeItem{}
	order := []groupKey{}
	for _, it := range items {
		k := groupKey{it.uid, it.host}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], it)
	}
	for _, k := range order {
		grp := groups[k]
		removeAll := func() {
			for _, it := range grp {
				_ = os.Remove(it.path)
			}
		}
		username := grp[0].username

		// OWNERSHIP (GH #630): validate against PANEL STATE — the root-owned
		// nginx vhost docroot uid — not a tenant-controllable /home path. A
		// tenant cannot forge /etc/nginx/sites-available/<host>.conf, so the
		// vhost's own root directive is the authoritative docroot; the requester
		// must own it.
		realDocroot, drerr := nginxVhostDocroot(filepath.Join(wpPurgeSitesDir, k.host+".conf"))
		if drerr != nil || realDocroot == "" {
			log.Warn("wp-purge: no nginx vhost for host, ignored", "user", username, "host", k.host, "files", len(grp))
			removeAll()
			continue
		}
		dst, derr := os.Stat(realDocroot)
		if derr != nil || !dst.IsDir() {
			log.Warn("wp-purge: vhost docroot missing, ignored", "host", k.host)
			removeAll()
			continue
		}
		if dstat, ok := dst.Sys().(*syscall.Stat_t); !ok || uint64(dstat.Uid) != k.uid {
			log.Warn("wp-purge: requester does not own the vhost docroot, ignored", "user", username, "host", k.host)
			removeAll()
			continue
		}

		outPaths, wholeDomain := mergeGroupPaths(grp)
		body, _ := json.Marshal(map[string]any{"domain": k.host, "paths": outPaths})
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, perr := wpPurgeRunPurge(pctx, body)
		cancel()
		if perr != nil {
			log.Warn("wp-purge: nginx.cache.purge failed", "host", k.host, "err", perr)
			removeAll()
			continue
		}
		log.Info("wp-purge: purged nginx cache (coalesced)", "user", username, "host", k.host,
			"files_coalesced", len(grp), "paths_merged", len(outPaths), "whole_domain", wholeDomain)
		removeAll()
		// Refill what was just purged, so the next visitor gets a HIT instead of
		// paying a full PHP render (a low-traffic site otherwise serves a cold
		// page after every edit). Ownership was checked above. A vhost with the
		// cache off stores nothing, so it is not warmed.
		if nginxVhostCacheOn(filepath.Join(wpPurgeSitesDir, k.host+".conf")) {
			wpPurgeStartWarm(ctx, k.host, outPaths, wholeDomain, log)
		}
	}
}

const (
	// wpWarmMaxConcurrent bounds warms running at once across the box, so a
	// burst of edits on many sites can't stack PHP renders.
	wpWarmMaxConcurrent = 2
	// wpWarmWholeDomainCooldown: the plugin purges the whole domain on
	// site-wide changes, comments included, so re-warm a whole domain at most
	// this often per host. A targeted warm (home + the edited post) is cheap
	// and not held back.
	wpWarmWholeDomainCooldown = 10 * time.Minute
	wpWarmTimeout             = 5 * time.Minute
)

var (
	wpWarmMu        sync.Mutex
	wpWarmInFlight  = map[string]bool{}
	wpWarmLastWhole = map[string]time.Time{}
	wpWarmSlots     = make(chan struct{}, wpWarmMaxConcurrent)
	// wpWarmRun runs one warm; a var so tests can observe it.
	wpWarmRun = func(ctx context.Context, params map[string]any) (any, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		return nginxCacheWarmupHandler(ctx, raw)
	}
)

// startWarmAfterPurge warms a host's page cache in the background after a WP
// purge: exactly the purged paths, or homepage + sitemap for a whole-domain
// purge. At most one warm per host runs at a time (a purge arriving mid-warm is
// not queued: its pages fill on the next visit), and a whole-domain warm runs at
// most once per wpWarmWholeDomainCooldown per host. Best effort: it never
// delays or fails the purge.
func startWarmAfterPurge(ctx context.Context, host string, paths []string, wholeDomain bool, log *slog.Logger) {
	now := time.Now()
	wpWarmMu.Lock()
	if wpWarmInFlight[host] {
		wpWarmMu.Unlock()
		log.Debug("wp-purge: warm already running for host, skipped", "host", host)
		return
	}
	if wholeDomain {
		if last, ok := wpWarmLastWhole[host]; ok && now.Sub(last) < wpWarmWholeDomainCooldown {
			wpWarmMu.Unlock()
			log.Debug("wp-purge: whole-domain warm inside cooldown, skipped", "host", host)
			return
		}
		wpWarmLastWhole[host] = now
	}
	wpWarmInFlight[host] = true
	wpWarmMu.Unlock()

	params := map[string]any{"host": host}
	if wholeDomain {
		params["max_urls"] = cacheWarmupDefaultMax
	} else {
		params["paths"] = append([]string(nil), paths...)
	}
	go func() {
		defer func() {
			wpWarmMu.Lock()
			delete(wpWarmInFlight, host)
			wpWarmMu.Unlock()
		}()
		select {
		case wpWarmSlots <- struct{}{}:
			defer func() { <-wpWarmSlots }()
		case <-ctx.Done():
			return
		}
		wctx, cancel := context.WithTimeout(ctx, wpWarmTimeout)
		defer cancel()
		res, err := wpWarmRun(wctx, params)
		if err != nil {
			log.Warn("wp-purge: cache warm failed", "host", host, "err", err)
			return
		}
		if m, ok := res.(map[string]any); ok {
			log.Info("wp-purge: warmed nginx cache", "host", host, "whole_domain", wholeDomain,
				"warmed", m["warmed"], "failed", m["failed"], "circuit_broken", m["circuit_broken"])
		}
	}()
}
