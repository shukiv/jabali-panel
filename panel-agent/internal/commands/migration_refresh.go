package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// migration_refresh.go — GH #646. Re-migration/refresh writers. The refresh is
// DESTRUCTIVE (overwrites a live migrated account), so Wave B (backup) is a hard
// precondition enforced by the runner: no refresh writer runs until
// migration.refresh_backup has produced a durable dest snapshot + DB dump.

var (
	refreshDBNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	refreshStampRE  = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)
	refreshOSUserRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	refreshTableRE  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// jabaliManagedRefreshExcludes are files the file mirror must NEVER overwrite —
// they carry the DEST's jabali integration (DB creds, Redis/PHP drop-ins). The
// source's copies would break the dest (invariant 2, GH #646).
var jabaliManagedRefreshExcludes = []string{
	"wp-config.php",
	"wp-content/object-cache.php",
	"wp-content/advanced-cache.php",
	".user.ini",
	"wp-content/mu-plugins/jabali-sso-*.php",
}

// Test seams: the account lookup, where homes live, and the tree chown.
var (
	lookupRefreshUser = user.Lookup
	refreshHomeRoot   = "/home"
	refreshChownTree  = chownTreeRecursive
)

// refreshTenant is the dest account of a refresh: a hosting user (uid in the
// tenant range, home <refreshHomeRoot>/<os_user>). Every dest path a refresh
// writes is resolved inside that home, and every source path inside the
// migration staging roots, the same way migration.import_home does.
type refreshTenant struct {
	name     string
	uid, gid int
	home     string // <refreshHomeRoot>/<name>
	anchor   string // home with a linked /home resolved, for the fd walks
	scope    *filesafe.Scope
}

func resolveRefreshTenant(osUser string) (*refreshTenant, error) {
	if !refreshOSUserRE.MatchString(osUser) {
		return nil, errors.New("invalid os_user")
	}
	u, err := lookupRefreshUser(osUser)
	if err != nil {
		return nil, fmt.Errorf("os_user %q not found", osUser)
	}
	uid, uerr := strconv.Atoi(u.Uid)
	gid, gerr := strconv.Atoi(u.Gid)
	if uerr != nil || gerr != nil || uid < minTenantUID || uid >= 60000 {
		return nil, fmt.Errorf("os_user %q is not a hosting user", osUser)
	}
	home := refreshHomeRoot + "/" + osUser
	if u.HomeDir != home {
		return nil, fmt.Errorf("os_user %q home is %q, not %s", osUser, u.HomeDir, home)
	}
	anchor := home
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		anchor = resolved
	}
	roots := []string{home}
	if anchor != home {
		roots = append(roots, anchor)
	}
	scope, err := filesafe.NewScope(osUser, osUser, roots)
	if err != nil {
		return nil, fmt.Errorf("home scope: %w", err)
	}
	return &refreshTenant{name: osUser, uid: uid, gid: gid, home: home, anchor: anchor, scope: scope}, nil
}

// below resolves p, which must be strictly inside the tenant's home, links
// and all. It returns the resolved path.
func (t *refreshTenant) below(p, what string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", fmt.Errorf("%s must be an absolute, clean path", what)
	}
	resolved, err := t.scope.Resolve(p)
	if err != nil {
		return "", fmt.Errorf("%s must be inside %s: %v", what, t.home, err)
	}
	if resolved == t.home || resolved == t.anchor {
		return "", fmt.Errorf("%s must be below %s, not the home itself", what, t.home)
	}
	return resolved, nil
}

// dirBelow is below for a path that must be an existing directory.
func (t *refreshTenant) dirBelow(p, what string) (string, error) {
	resolved, err := t.below(p, what)
	if err != nil {
		return "", err
	}
	if fi, sErr := os.Lstat(resolved); sErr != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s does not exist", what)
	}
	return resolved, nil
}

// fdDir is a working directory for a child process: the directory f holds
// open. The child changes into it before it runs, so the path f was opened
// by is never looked up again.
func fdDir(f *os.File) string {
	return "/proc/self/fd/" + strconv.Itoa(int(f.Fd()))
}

// openDirAt opens the directory name in dirfd without following a link.
func openDirAt(dirfd int, name string) (*os.File, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// refreshStaged resolves a staged source path inside the migration staging
// roots, links and all.
func refreshStaged(p, what string) (string, *filesafe.Scope, error) {
	scope, err := filesafe.NewScope("refresh", "migration", migrationStagingRoots)
	if err != nil {
		return "", nil, fmt.Errorf("staging scope: %w", err)
	}
	resolved, err := scope.Resolve(p)
	if err != nil {
		return "", nil, fmt.Errorf("%s must be inside %s: %v", what, strings.Join(migrationStagingRoots, " or "), err)
	}
	return resolved, scope, nil
}

type refreshBackupParams struct {
	Docroot string `json:"docroot"`
	OSUser  string `json:"os_user"`
	DBName  string `json:"db_name"`
	Stamp   string `json:"stamp"`
}

// migration.refresh_backup (Wave B) — MANDATORY pre-overwrite backup: a hardlink
// snapshot of the dest docroot + a mysqldump of the dest DB. Non-destructive; a
// failure aborts the refresh with the dest untouched.
func migrationRefreshBackupHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p refreshBackupParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	if !refreshDBNameRE.MatchString(p.DBName) {
		return nil, csInvalidArg("invalid db_name")
	}
	if !refreshStampRE.MatchString(p.Stamp) {
		return nil, csInvalidArg("stamp must be YYYYMMDD-HHMMSS")
	}
	t, err := resolveRefreshTenant(p.OSUser)
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	docroot, err := t.dirBelow(p.Docroot, "docroot")
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	// The snapshot and the dump go next to the docroot, in a folder the
	// tenant writes. Hold that folder open (resolved inside the home) and do
	// everything relative to it, so a link swapped into the path since the
	// check is never followed:
	//   - the snapshot folder is made here, root-only: mkdirat fails on
	//     anything already at its name, a link included, and the tenant can't
	//     put anything inside it;
	//   - the dump is created new (O_EXCL, O_NOFOLLOW);
	//   - cp copies the held docroot into the held snapshot folder.
	parent, err := t.scope.OpenDirInScope(filepath.Dir(docroot))
	if err != nil {
		return nil, csInvalidArg("docroot's folder: " + err.Error())
	}
	defer parent.Close()
	pfd := int(parent.Fd())
	base := filepath.Base(docroot)
	src, err := openDirAt(pfd, base)
	if err != nil {
		return nil, csInvalidArg("docroot: " + err.Error())
	}
	defer src.Close()
	snapName := base + ".pre-refresh-" + p.Stamp
	snapshot := filepath.Join(filepath.Dir(docroot), snapName)
	if mErr := unix.Mkdirat(pfd, snapName, 0o700); mErr != nil {
		if errors.Is(mErr, unix.EEXIST) {
			return nil, csInvalidArg("snapshot already exists: " + snapshot)
		}
		return nil, csInternal("create snapshot folder", mErr)
	}
	snap, err := openDirAt(pfd, snapName)
	if err != nil {
		_ = unix.Unlinkat(pfd, snapName, unix.AT_REMOVEDIR)
		return nil, csInternal("open snapshot folder", err)
	}
	defer snap.Close()
	dumpName := "pre-refresh-" + p.Stamp + ".sql"
	dumpPath := filepath.Join(filepath.Dir(docroot), dumpName)
	fd, fErr := unix.Openat(pfd, dumpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if fErr != nil {
		_ = unix.Unlinkat(pfd, snapName, unix.AT_REMOVEDIR)
		if errors.Is(fErr, unix.EEXIST) {
			return nil, csInvalidArg("db dump already exists: " + dumpPath)
		}
		return nil, csInternal("create dump", fErr)
	}
	f := os.NewFile(uintptr(fd), dumpPath)
	defer f.Close()
	// The held docroot is the child's fd 3.
	cp := execCommandContext(ctx, "cp", "-al", "--", "/proc/self/fd/3/.", ".")
	cp.ExtraFiles = []*os.File{src}
	cp.Dir = fdDir(snap)
	// Undo on failure, with its own context: ctx may be what just ran out.
	undo := func() {
		_ = unix.Unlinkat(pfd, dumpName, 0)
		_ = t.scope.RemoveAllInScope(context.Background(), snapshot)
	}
	if out, cErr := cp.CombinedOutput(); cErr != nil {
		undo()
		return nil, csInternal("docroot snapshot", fmt.Errorf("%v: %s", cErr, string(out)))
	}
	dump := execCommandContext(ctx, "mysqldump", "--single-transaction", "--quick", "--lock-tables=0", "--", p.DBName)
	dump.Stdout = f
	var dumpErr strings.Builder
	dump.Stderr = &dumpErr
	if dErr := dump.Run(); dErr != nil {
		undo()
		return nil, csInternal("mysqldump", fmt.Errorf("%v: %s", dErr, dumpErr.String()))
	}
	return map[string]any{"ok": true, "snapshot": snapshot, "db_dump": dumpPath}, nil
}

type refreshFilesParams struct {
	Source  string `json:"source"`
	Docroot string `json:"docroot"`
	OSUser  string `json:"os_user"`
}

// migration.refresh_files (Wave C) — DESTRUCTIVE mirror of the staged source
// onto the dest docroot with --delete, EXCLUDING jabali-managed files so the
// dest DB creds + Redis/PHP integration survive. Re-applies <user>:www-data.
func migrationRefreshFilesHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p refreshFilesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	t, err := resolveRefreshTenant(p.OSUser)
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	docroot, err := t.dirBelow(p.Docroot, "dest docroot")
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	src, _, err := refreshStaged(p.Source, "source")
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	if fi, sErr := os.Lstat(src); sErr != nil || !fi.IsDir() {
		return nil, csInvalidArg("source dir does not exist")
	}
	args := []string{"-rlptD", "--delete"}
	for _, ex := range jabaliManagedRefreshExcludes {
		args = append(args, "--exclude="+ex)
	}
	// rsync runs in the docroot it was handed, held open from inside the
	// home, so a link swapped into the path since the check is never followed.
	dst, err := t.scope.OpenDirInScope(docroot)
	if err != nil {
		return nil, csInvalidArg("dest docroot: " + err.Error())
	}
	defer dst.Close()
	args = append(args, src+"/", "./")
	rs := execCommandContext(ctx, "rsync", args...)
	rs.Dir = fdDir(dst)
	if out, rErr := rs.CombinedOutput(); rErr != nil {
		return nil, csInternal("rsync mirror", fmt.Errorf("%v: %s", rErr, string(out)))
	}
	// <user>:www-data, by file descriptor from the home (never `chown -R`,
	// which follows a link out of the tree). Without a www-data group the
	// user's own group, as migration.import_home does.
	gid := t.gid
	if g, gErr := user.LookupGroup("www-data"); gErr == nil {
		if n, aErr := strconv.Atoi(g.Gid); aErr == nil {
			gid = n
		}
	}
	if cErr := refreshChownTree(t.anchor, docroot, t.uid, gid); cErr != nil {
		return nil, csInternal("chown docroot", cErr)
	}
	return map[string]any{"ok": true}, nil
}

type refreshDBParams struct {
	DBName  string `json:"db_name"`
	SQLPath string `json:"sql_path"`
}

// migration.refresh_db (Wave C) — DESTRUCTIVE: drop every table in the dest DB
// then reimport the staged source dump INTO THE SAME dest DB (same name+prefix)
// so the preserved dest wp-config stays valid.
func migrationRefreshDBHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p refreshDBParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	if !refreshDBNameRE.MatchString(p.DBName) {
		return nil, csInvalidArg("invalid db_name")
	}
	sql, stage, err := refreshStaged(p.SQLPath, "sql_path")
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	f, fErr := stage.OpenInScope(sql, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if fErr != nil {
		return nil, csInvalidArg("sql dump not found")
	}
	defer f.Close()
	if fi, sErr := f.Stat(); sErr != nil || !fi.Mode().IsRegular() {
		return nil, csInvalidArg("sql dump not found")
	}
	tblOut, tErr := execCommandContext(ctx, "mysql", "-N", "-e", "SHOW TABLES", "--", p.DBName).Output()
	if tErr != nil {
		return nil, csInternal("list dest tables", tErr)
	}
	var drop strings.Builder
	drop.WriteString("SET FOREIGN_KEY_CHECKS=0;\n")
	for _, t := range strings.Fields(string(tblOut)) {
		if refreshTableRE.MatchString(t) {
			drop.WriteString("DROP TABLE IF EXISTS `" + t + "`;\n")
		}
	}
	drop.WriteString("SET FOREIGN_KEY_CHECKS=1;\n")
	dropCmd := execCommandContext(ctx, "mysql", "--", p.DBName)
	dropCmd.Stdin = strings.NewReader(drop.String())
	if out, dErr := dropCmd.CombinedOutput(); dErr != nil {
		return nil, csInternal("drop dest tables", fmt.Errorf("%v: %s", dErr, string(out)))
	}
	imp := execCommandContext(ctx, "mysql", "--", p.DBName)
	imp.Stdin = f
	if out, iErr := imp.CombinedOutput(); iErr != nil {
		return nil, csInternal("reimport dump", fmt.Errorf("%v: %s", iErr, string(out)))
	}
	return map[string]any{"ok": true}, nil
}

type refreshReconcileParams struct {
	OSUser      string `json:"os_user"`
	InstallPath string `json:"install_path"`
	Domain      string `json:"domain"`
	OldURL      string `json:"old_url,omitempty"`
	NewURL      string `json:"new_url,omitempty"`
}

// migration.refresh_reconcile (Wave D) — post-refresh: fix site URLs if they
// changed, flush caches, reload the per-user FPM master (opcache), and purge the
// nginx page cache for the domain. The dest wp-config was excluded from the file
// mirror, so the jabali cache CONSTANTS are intact — no re-stamp needed (the
// drop-ins read constants, not the reimported DB option).
func migrationRefreshReconcileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p refreshReconcileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, csInvalidArg("bad params")
	}
	t, err := resolveRefreshTenant(p.OSUser)
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	if _, err := t.below(p.InstallPath, "install_path"); err != nil {
		return nil, csInvalidArg(err.Error())
	}
	warnings := []string{}
	// 1. Site-URL rewrite when the domain/path changed. SECURITY: OldURL/NewURL
	// are tenant/operator-supplied; validate them as real http(s) URLs and reject
	// any leading "-" so they can't smuggle a wp-cli flag — that leading-"-"
	// rejection IS the argument-injection defense. They are then passed as the
	// trailing <old> <new> positionals with the flags FIRST and NO "--"
	// separator: wp-cli's search-replace (2.12) treats a "--" token alongside
	// --all-tables as a table filter, so the replacement URL was parsed as a
	// non-existent table name and the rewrite always failed ("Couldn't find any
	// tables matching: <new_url>"). Dropping "--" is safe because a value that
	// could be mistaken for a flag is already rejected above.
	if p.OldURL != "" && p.NewURL != "" && p.OldURL != p.NewURL {
		for _, v := range []string{p.OldURL, p.NewURL} {
			if strings.HasPrefix(v, "-") {
				return nil, csInvalidArg("url must not start with '-'")
			}
			u, uErr := url.Parse(v)
			if uErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, csInvalidArg("old_url/new_url must be valid http(s) URLs")
			}
		}
		if err := runWPAsTenant(ctx, p.OSUser, p.InstallPath, "search-replace", "--all-tables", "--skip-columns=guid", p.OldURL, p.NewURL); err != nil {
			warnings = append(warnings, "search-replace: "+err.Error())
		}
	}
	// 2. Flush WP object cache.
	_ = runWPAsTenant(ctx, p.OSUser, p.InstallPath, "cache", "flush")
	// 3. Reload the per-user FPM master (drop stale opcache).
	_ = execCommandContext(ctx, "systemctl", "reload", "jabali-fpm@"+p.OSUser+".service").Run()
	// 4. Purge the nginx page cache for the domain (best-effort; targeted unlink).
	if refreshDomainRE.MatchString(strings.ToLower(p.Domain)) {
		cacheDir := "/var/cache/nginx/jabali"
		if _, err := os.Stat(cacheDir); err == nil {
			// Coarse purge: the targeted nginx.cache.purge verb exists; here we
			// just note it. A full purge happens on the next domain reconcile.
			warnings = append(warnings, "page cache purge deferred to reconcile/nginx.cache.purge")
		}
	}
	return map[string]any{"ok": true, "warnings": warnings}, nil
}

var refreshDomainRE = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)

func init() {
	Default.Register("migration.refresh_backup", migrationRefreshBackupHandler)
	Default.Register("migration.refresh_reconcile", migrationRefreshReconcileHandler)
	Default.Register("migration.refresh_files", migrationRefreshFilesHandler)
	Default.Register("migration.refresh_db", migrationRefreshDBHandler)
}
