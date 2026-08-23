package commands

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

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// FTP/SFTP subaccounts (GH #1053, plans/gh1053-ftp-accounts.md).
//
// A subaccount is a SECOND passwd entry sharing the owning tenant's uid/gid
// (useradd --non-unique): its own username + password + home directory, but
// every file it writes is owned by the tenant uid, so quotas, per-user FPM,
// AppArmor, and backups treat its writes exactly like the tenant's own.
//
// Its cgroup RESOURCE limits do NOT carry over, though (JAB-263): the M18
// CPU/memory/task limits live on jabali-user-<tenant>.slice, which binds only
// panel-managed SERVICES (php-fpm pool, python app, docker) via an explicit
// Slice=. Interactive transfer sessions — FTPS here, and SSH/SFTP alike — are
// never placed in that slice: there is no pam_systemd in the vsftpd stack, and
// logind's default is the standard user-<uid>.slice, not ours. FTPS resource
// use is instead bounded at the vsftpd daemon (max_clients / max_per_ip /
// local_max_rate, rendered from server_settings). Per-tenant cgroup placement
// of interactive sessions is a cross-cutting gap (SSH too), deferred to the
// session-placement epic — see docs/adr/0167 and JAB-263.
//
// Deliberate non-goals, both consequences of sharing the uid:
//   - No isolation BETWEEN a tenant and its subaccounts — the kernel cannot
//     separate two identities with one uid. The boundary this feature adds
//     is credential-scoped (revocable, per-protocol), not filesystem-scoped.
//   - home_path picks where a session STARTS (and the vsftpd chroot), not
//     what the account can ultimately reach over SFTP.
//
// Group memberships:
//   - jabali-ftp: PAM gate for vsftpd — held only while ftp_access is on.
//   - jabali-sftp: NEVER. Its M12 Match block chroots to /home/%u, and a
//     subaccount's username has no /home/<username>; the resulting failed
//     chroot would reset every connection. SFTP for subaccounts arrives via
//     per-account Match User blocks (step 3), not this group.
const ftpGroupName = "jabali-ftp"

// ftpAliasGecos is the passwd comment (GECOS) stamped on every alias the
// panel creates — the ownership marker that separates OUR aliases from
// operator-made same-uid accounts. GECOS survives everything the alias
// survives (chpasswd, usermod -L/-U, group edits).
const ftpAliasGecos = "jabali-ftp-account"

// ftpSubaccountLabelRegex validates the operator-chosen suffix. The full
// username is "<tenant>_<label>": lowercase, digits, underscore, and the
// whole thing must still fit useradd's 32-char ceiling.
var ftpSubaccountLabelRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,19}$`)

const ftpSubaccountMaxLen = 32

// minTenantUID is the floor for uids a subaccount may alias. Blocks root
// (uid 0) and system accounts outright, whatever a buggy caller sends —
// the agent never trusts the panel just because it is localhost.
const minTenantUID = 1000

// ftpTenant resolves and validates the owning tenant for a subaccount
// operation: real user, regular-uid range, home under /home.
type ftpTenant struct {
	Username string
	UID      int
	GID      int
	HomeDir  string
}

func resolveFtpTenant(tenantUsername string) (*ftpTenant, *agentwire.AgentError) {
	if !usernameRegex.MatchString(tenantUsername) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid tenant username %q", tenantUsername),
		}
	}
	u, err := user.Lookup(tenantUsername)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("tenant %q not found on host", tenantUsername),
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid < minTenantUID {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("tenant %q has uid %d — subaccounts may only alias regular tenant uids (>= %d)", tenantUsername, uid, minTenantUID),
		}
	}
	if !strings.HasPrefix(u.HomeDir, "/home/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("tenant %q home %q is outside /home — refusing", tenantUsername, u.HomeDir),
		}
	}
	return &ftpTenant{Username: tenantUsername, UID: uid, GID: gid, HomeDir: u.HomeDir}, nil
}

// validateFtpSubaccountName enforces the "<tenant>_<label>" namespace so a
// subaccount can never collide with a real user or another tenant's
// accounts, and never impersonate an unrelated name.
func validateFtpSubaccountName(tenant, sub string) *agentwire.AgentError {
	prefix := tenant + "_"
	if !strings.HasPrefix(sub, prefix) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("subaccount %q must be namespaced under the tenant (%s<label>)", sub, prefix),
		}
	}
	label := strings.TrimPrefix(sub, prefix)
	if !ftpSubaccountLabelRegex.MatchString(label) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid subaccount label %q: must match %s", label, ftpSubaccountLabelRegex.String()),
		}
	}
	if len(sub) > ftpSubaccountMaxLen {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("subaccount %q exceeds %d characters", sub, ftpSubaccountMaxLen),
		}
	}
	return nil
}

// resolveFtpHomePath validates that homePath is an absolute, clean path
// that — after resolving the symlinks of its nearest existing ancestor —
// stays inside the tenant's home. A tenant-writable tree means a symlink
// can point anywhere; resolving server-side is what stops a subaccount
// home from landing in another tenant (or /etc).
//
// JAB-251: this EvalSymlinks pass is a fast-fail / friendly-error gate
// ONLY — it is inherently TOCTOU (the tenant can swap a component after
// this returns). The authoritative escape boundary is the openat2-
// confined MkdirChownInScope in the create handler, which resolves and
// mutates through descriptors with no post-check pathname re-resolution.
func resolveFtpHomePath(homePath string, tenant *ftpTenant) (string, *agentwire.AgentError) {
	if !filepath.IsAbs(homePath) || filepath.Clean(homePath) != homePath {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("home_path %q must be absolute and clean (no ., .., trailing slash)", homePath),
		}
	}
	// The path lands in a rendered sshd directive (`internal-sftp -d`)
	// and in the passwd home field: whitespace splits the argv, quotes
	// and backslashes change tokenization, colons corrupt passwd rows.
	// Docroot paths never legitimately contain any of these.
	if strings.ContainsAny(homePath, " \t\r\n\"'\\:") {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("home_path %q must not contain whitespace, quotes, backslashes, or colons", homePath),
		}
	}
	// Resolve through the nearest existing ancestor so a not-yet-created
	// leaf directory doesn't fail validation while symlinked ancestors
	// still get caught.
	probe := homePath
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			rel, rerr := filepath.Rel(tenant.HomeDir, resolved)
			if rerr != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				return "", &agentwire.AgentError{
					Code:    agentwire.CodePermissionDenied,
					Message: fmt.Sprintf("home_path %q resolves outside the tenant home %q", homePath, tenant.HomeDir),
				}
			}
			return homePath, nil
		}
		if !os.IsNotExist(err) {
			return "", &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("resolve home_path %q: %v", probe, err),
			}
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("home_path %q has no existing ancestor", homePath),
			}
		}
		probe = parent
	}
}

// requireFtpSubaccount verifies an EXISTING passwd entry is one of ours:
// tenant-namespaced name AND aliasing exactly the tenant's uid. Every
// mutating verb below runs this before touching the account, so a caller
// can never lock, re-password, or delete an unrelated user.
func requireFtpSubaccount(tenant *ftpTenant, sub string) (*user.User, *agentwire.AgentError) {
	if aerr := validateFtpSubaccountName(tenant.Username, sub); aerr != nil {
		return nil, aerr
	}
	u, err := user.Lookup(sub)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("subaccount %q not found on host", sub),
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	// Ownership: the GECOS marker (bare or owner-encoded) is authoritative.
	// Owner-encoded aliases match by owner regardless of uid (isolated accounts
	// have their OWN uid); bare-marker legacy aliases must still share the
	// tenant uid. os/user parses User.Name as the pre-comma GECOS segment, which
	// is exactly what ftpMarkerOwner expects. Both the marker AND the tenant
	// binding must hold, so a caller can never lock/re-password/delete an
	// operator's alias or another tenant's subaccount.
	owner, marked := ftpMarkerOwner(u.Name)
	if !marked {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("passwd entry %q lacks the %q ownership marker — operator-managed account, refusing", sub, ftpAliasGecos),
		}
	}
	if owner != "" {
		if owner != tenant.Username {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodePermissionDenied,
				Message: fmt.Sprintf("passwd entry %q is owned by tenant %q, not %q — refusing", sub, owner, tenant.Username),
			}
		}
		// Owner match is authoritative, but never touch a system uid: an owned
		// subaccount is the tenant uid (owner-encoded legacy) or an isolated uid
		// in the reserved range. Blocks a mistaken owner marker on uid 0.
		if uid != tenant.UID && uid < ftpSubaccountUIDMin {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodePermissionDenied,
				Message: fmt.Sprintf("passwd entry %q has out-of-range uid %d for an owned subaccount — refusing", sub, uid),
			}
		}
	} else if uid != tenant.UID {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("passwd entry %q has uid %d, expected tenant uid %d — refusing to touch a non-subaccount user", sub, uid, tenant.UID),
		}
	}
	return u, nil
}

// ensureFtpGroup makes the jabali-ftp PAM group exist. Idempotent;
// install.sh's ftp module also creates it, but an agent older host that
// enables ftp_access before the module lands must not fail here.
func ensureFtpGroup(ctx context.Context) *agentwire.AgentError {
	if _, err := user.LookupGroup(ftpGroupName); err == nil {
		return nil
	}
	if out, err := execCommandContext(ctx, "groupadd", "--system", ftpGroupName).CombinedOutput(); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("groupadd %s: %v: %s", ftpGroupName, err, strings.TrimSpace(string(out))),
		}
	}
	return nil
}

func setFtpGroupMembership(ctx context.Context, username string, member bool) *agentwire.AgentError {
	if member {
		if aerr := ensureFtpGroup(ctx); aerr != nil {
			return aerr
		}
		if out, err := execCommandContext(ctx, "usermod", "-aG", ftpGroupName, username).CombinedOutput(); err != nil {
			return &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("usermod -aG %s %s: %v: %s", ftpGroupName, username, err, strings.TrimSpace(string(out))),
			}
		}
		return nil
	}
	isMember, err := isUserInGroup(ctx, username, ftpGroupName)
	if err != nil || !isMember {
		return nil // absent group or non-member: removal is a no-op
	}
	if out, gerr := execCommandContext(ctx, "gpasswd", "-d", username, ftpGroupName).CombinedOutput(); gerr != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("gpasswd -d %s %s: %v: %s", username, ftpGroupName, gerr, strings.TrimSpace(string(out))),
		}
	}
	return nil
}

// chpasswdStdin sets a password via chpasswd's stdin — the password must
// never appear in argv (visible in /proc) or logs.
func chpasswdStdin(ctx context.Context, username, password string) *agentwire.AgentError {
	cmd := execCommandContext(ctx, "chpasswd")
	cmd.Stdin = strings.NewReader(username + ":" + password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chpasswd for %q failed: %v: %s", username, err, strings.TrimSpace(string(out))),
		}
	}
	return nil
}

// ---- ftpaccount.create ----

type ftpAccountCreateParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	HomePath       string `json:"home_path"`
	Password       string `json:"password"`
	FTPAccess      bool   `json:"ftp_access"`
	// WebDAVAccess realizes the per-subaccount WebDAV worker at create time (GH
	// #1146) — same treatment as FTPAccess: the account is provisioned, then the
	// worker + jabali-webdav group are enabled. Rolls the whole create back on
	// failure, like every other create step.
	WebDAVAccess bool `json:"webdav_access"`
	// Isolated selects the separate-uid jailed model (GH #1145). When false
	// the legacy same-uid alias path runs. The fields below are required only
	// when Isolated is true; the panel allocates the uid + computes the split.
	Isolated   bool   `json:"isolated"`
	UID        uint32 `json:"uid"`
	QuotaMB    uint32 `json:"quota_mb"`
	QuotaMount string `json:"quota_mount"`
	JailPath   string `json:"jail_path"`
}

type ftpAccountCreateResponse struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	HomePath string `json:"home_path"`
}

// ensureTenantOwnedDir makes homePath exist and be tenant-owned, created
// through the openat2-confined scope (JAB-251) so a tenant symlink swap under
// the tenant-writable home cannot redirect the root chown. No-op when homePath
// IS the tenant home root (already provisioned, no in-scope parent to open).
// Used by both the legacy and the isolated create paths.
func ensureTenantOwnedDir(tenant *ftpTenant, homePath string) *agentwire.AgentError {
	if filepath.Clean(homePath) == filepath.Clean(tenant.HomeDir) {
		return nil
	}
	scope, serr := filesafe.NewScope(tenant.Username, tenant.Username, []string{tenant.HomeDir})
	if serr != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("home scope for %q: %v", tenant.HomeDir, serr)}
	}
	if err := scope.MkdirChownInScope(homePath, 0o755, true, tenant.UID, tenant.GID); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create+chown home %q (confined): %v", homePath, err)}
	}
	return nil
}

func ftpAccountCreateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpAccountCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateFtpSubaccountName(tenant.Username, p.Username); aerr != nil {
		return nil, aerr
	}
	homePath, aerr := resolveFtpHomePath(p.HomePath, tenant)
	if aerr != nil {
		return nil, aerr
	}
	if p.Password == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "password is required"}
	}
	if _, err := user.Lookup(p.Username); err == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: fmt.Sprintf("user %q already exists", p.Username)}
	}

	// GH #1145 isolated (separate-uid) path: create the selected dir confined,
	// then build the root-owned jail + own-uid user + ACL + per-uid quota. The
	// legacy same-uid alias path below is skipped entirely for isolated rows.
	if p.Isolated {
		if aerr := ensureTenantOwnedDir(tenant, homePath); aerr != nil {
			return nil, aerr
		}
		rollback, aerr := provisionIsolatedJail(ctx, tenant, p)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := chpasswdStdin(ctx, p.Username, p.Password); aerr != nil {
			rollback()
			return nil, aerr
		}
		if p.FTPAccess {
			if aerr := setFtpGroupMembership(ctx, p.Username, true); aerr != nil {
				rollback()
				return nil, aerr
			}
		}
		if p.WebDAVAccess {
			if aerr := enableWebdavForCreate(ctx, tenant, p.Username); aerr != nil {
				rollback()
				return nil, aerr
			}
		}
		return ftpAccountCreateResponse{Username: p.Username, UID: int(p.UID), HomePath: homePath}, nil
	}

	// Same-uid alias. --no-create-home: the directory is the tenant's
	// (created on demand below if missing); a subaccount must never own
	// tenant tree nodes. Shell is nologin ON PURPOSE: sshd's ForceCommand
	// internal-sftp runs IN-PROCESS (no shell exec — M12's chroots contain
	// no /bin and work), so SFTP is unaffected, while every path that
	// WOULD exec a shell dead-ends. With bash, an alias that has no Match
	// block yet (ftp-only account, or the create→first-sshd_sync gap)
	// would get an interactive shell whenever the operator's global
	// PasswordAuthentication toggle is on. vsftpd side: the step-4 PAM
	// file must omit pam_shells.so, which would reject nologin.
	// No --user-group and NO www-data (#430); jabali-ftp only via
	// ftp_access below.
	// --comment: the GECOS field is the panel's OWNERSHIP MARKER. The
	// "<tenant>_<label>, same uid" naming pattern alone is guessable and
	// predates this feature (operators hand-make same-uid aliases with the
	// identical useradd -o trick), and every reaper in this codebase marks
	// its property before it ever deletes (policy-rc.d marker line, DNS
	// managed_by=acme-dns01). list_all + every mutating verb require this
	// marker, so the stray sweep can never userdel an operator's own alias.
	args := []string{
		"--non-unique",
		"--uid", strconv.Itoa(tenant.UID),
		"--gid", strconv.Itoa(tenant.GID),
		"--home-dir", homePath,
		"--no-create-home",
		"--no-user-group",
		"--shell", "/usr/sbin/nologin",
		"--comment", ftpAliasGecosFor(tenant.Username),
		p.Username,
	}
	if out, err := execCommandContext(ctx, "useradd", args...).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("useradd %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))}
	}
	rollback := func() {
		// -f: the alias shares the tenant's uid, so the tenant's own
		// processes (FPM pool, cron) always count as "user in use" and a
		// plain userdel exits 8. Never --remove: home is tenant data.
		_ = execCommandContext(ctx, "userdel", "-f", p.Username).Run()
	}
	// Home must exist for vsftpd chroot + internal-sftp start dir, owned
	// by the tenant uid like every other node in their tree.
	//
	// JAB-251: the create + chown are descriptor-confined via openat2
	// (RESOLVE_BENEATH) against the tenant home, NOT run by pathname. The
	// parent home tree is tenant-writable, so a plain os.MkdirAll +
	// os.Chown by pathname could be raced — swap a validated component
	// for a symlink to /etc/sudoers.d after the check and the root chown
	// follows it. MkdirChownInScope holds the resolved parent fd across
	// mkdirat + O_NOFOLLOW re-open + Fchown, so no post-validation
	// pathname re-resolution occurs and a swapped leaf is refused (ELOOP).
	if aerr := ensureTenantOwnedDir(tenant, homePath); aerr != nil {
		rollback()
		return nil, aerr
	}
	if aerr := chpasswdStdin(ctx, p.Username, p.Password); aerr != nil {
		rollback()
		return nil, aerr
	}
	if p.FTPAccess {
		if aerr := setFtpGroupMembership(ctx, p.Username, true); aerr != nil {
			rollback()
			return nil, aerr
		}
	}
	if p.WebDAVAccess {
		if aerr := enableWebdavForCreate(ctx, tenant, p.Username); aerr != nil {
			rollback()
			return nil, aerr
		}
	}
	return ftpAccountCreateResponse{Username: p.Username, UID: tenant.UID, HomePath: homePath}, nil
}

// ---- ftpaccount.set_password ----

type ftpAccountSetPasswordParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	// Enabled is the panel-authoritative desired lock state (JAB-261).
	// chpasswd rewrites the shadow hash and DROPS the '!' lock, so a
	// disabled (usermod -L) account would silently re-enable and accept
	// FTPS/SFTP auth until the reconciler re-locked it. Pointer, not bool:
	// an older panel that omits the field must NOT default a disabled
	// account to unlocked — nil falls back to preserving the pre-change
	// lock state instead.
	Enabled *bool `json:"enabled"`
}

// lockAfterPasswordReset decides whether the account must be re-locked after
// chpasswd clears the shadow lock. The panel's is_enabled is authoritative
// when present; absent (older panel), preserve whatever lock state existed
// before the password change so a disabled account is never silently unlocked.
func lockAfterPasswordReset(enabled *bool, wasLocked bool) bool {
	if enabled != nil {
		return !*enabled
	}
	return wasLocked
}

// ftpUserLocked reports whether username's shadow password is locked. `passwd
// -S` prints "<user> <status> ..." where status is L (locked), P/PS (usable),
// or NP (no password). Field 2 is the status.
func ftpUserLocked(ctx context.Context, username string) (bool, *agentwire.AgentError) {
	out, err := execCommandContext(ctx, "passwd", "-S", username).CombinedOutput()
	if err != nil {
		return false, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("passwd -S %q: %v: %s", username, err, strings.TrimSpace(string(out)))}
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return false, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("passwd -S %q: unexpected output %q", username, strings.TrimSpace(string(out)))}
	}
	return fields[1] == "L", nil
}

func ftpAccountSetPasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpAccountSetPasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	au, aerr := requireFtpSubaccount(tenant, p.Username)
	if aerr != nil {
		return nil, aerr
	}
	if p.Password == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "password is required"}
	}
	// JAB-261: capture the pre-change lock only when the panel didn't send
	// an authoritative desired state (avoids an extra passwd -S on the hot
	// path).
	var wasLocked bool
	if p.Enabled == nil {
		locked, lerr := ftpUserLocked(ctx, p.Username)
		if lerr != nil {
			return nil, lerr
		}
		wasLocked = locked
	}
	if aerr := chpasswdStdin(ctx, p.Username, p.Password); aerr != nil {
		return nil, aerr
	}
	// Re-apply the shadow lock in the SAME verb (no reliance on the periodic
	// reconciler) so a disabled account cannot authenticate in the window.
	if lockAfterPasswordReset(p.Enabled, wasLocked) {
		if out, err := execCommandContext(ctx, "usermod", "-L", p.Username).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("re-lock disabled account %q after password reset: %v: %s", p.Username, err, strings.TrimSpace(string(out)))}
		}
		// JAB-256: a reset on a disabled account must not leave a live session
		// authenticated under the old password — kill any open sessions too.
		uid, _ := strconv.Atoi(au.Uid)
		terminateFtpSessions(ctx, p.Username, uid)
	}
	return map[string]any{"username": p.Username, "updated": true}, nil
}

// ---- ftpaccount.set_access ----

type ftpAccountSetAccessParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	FTPAccess      bool   `json:"ftp_access"`
	// WebDAVAccess gates the per-subaccount WebDAV worker + jabali-webdav group
	// (GH #1146). Pointer, not bool: nil = leave the worker unchanged. A plain
	// bool would default an older panel's omitted field to false and tear the
	// worker down on every unrelated set_access call — the same scar as
	// set_password.Enabled (JAB-261). Effective only while Enabled is true.
	WebDAVAccess *bool `json:"webdav_access"`
	// Enabled locks/unlocks the account password (usermod -L/-U): a
	// disabled subaccount fails auth on every protocol but keeps its
	// row, memberships, and files.
	Enabled bool `json:"enabled"`
}

func ftpAccountSetAccessHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpAccountSetAccessParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	au, aerr := requireFtpSubaccount(tenant, p.Username)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := setFtpGroupMembership(ctx, p.Username, p.FTPAccess); aerr != nil {
		return nil, aerr
	}
	lockFlag := "-U"
	if !p.Enabled {
		lockFlag = "-L"
	}
	if out, err := execCommandContext(ctx, "usermod", lockFlag, p.Username).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod %s %q: %v: %s", lockFlag, p.Username, err, strings.TrimSpace(string(out)))}
	}
	// JAB-256: the lock blocks re-auth but not an already-open session — kill
	// live sessions so a disable takes effect at once.
	if !p.Enabled {
		uid, _ := strconv.Atoi(au.Uid)
		terminateFtpSessions(ctx, p.Username, uid)
	}
	// GH #1146: WebDAV worker lifecycle. Only touch it when the panel sent the
	// field (older panels omit it → nil → leave as-is). Desired-running requires
	// both webdav_access AND the account enabled — a disabled account serves no
	// protocol.
	resp := map[string]any{"username": p.Username, "ftp_access": p.FTPAccess, "enabled": p.Enabled}
	if p.WebDAVAccess != nil {
		if *p.WebDAVAccess && p.Enabled {
			if aerr := enableWebdavWorker(ctx, tenant, au); aerr != nil {
				return nil, aerr
			}
		} else {
			if aerr := disableWebdavWorker(ctx, p.Username); aerr != nil {
				return nil, aerr
			}
		}
		resp["webdav_access"] = *p.WebDAVAccess
	}
	return resp, nil
}

// ---- ftpaccount.delete ----

type ftpAccountDeleteParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
}

func ftpAccountDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpAccountDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := requireFtpSubaccount(tenant, p.Username)
	if aerr != nil {
		// Idempotent delete: a passwd entry that is already gone is success.
		// But a PRIOR delete may have failed AFTER userdel and BEFORE jail
		// teardown, leaving an orphaned jail + live bind mount of tenant data
		// that nothing else sweeps (the reaper keys on passwd, the reconciler
		// on DB rows — both gone). Tear the jail down here too; idempotent and
		// ENOENT-safe, so it no-ops for a legacy account that never had one.
		if aerr.Code == agentwire.CodeNotFound {
			// GH #1146: also tear down any orphaned WebDAV worker/drop-in/group
			// from a partial prior delete. Idempotent + safe on absent.
			if derr := disableWebdavWorker(ctx, p.Username); derr != nil {
				return nil, derr
			}
			if terr := teardownIsolatedJail(ctx, ftpJailPathFor(tenant, p.Username)); terr != nil {
				return nil, terr
			}
			return map[string]any{"username": p.Username, "deleted": false, "absent": true}, nil
		}
		return nil, aerr
	}
	// Isolated accounts own a uid in the reserved range and a root-owned jail;
	// remember whether to tear it down after userdel. Legacy same-uid aliases
	// share the tenant uid and have no jail.
	delUID, _ := strconv.Atoi(u.Uid)
	isolated := delUID >= ftpSubaccountUIDMin
	_ = setFtpGroupMembership(ctx, p.Username, false)
	// GH #1146: stop + disable the WebDAV worker and remove its drop-in/group
	// BEFORE userdel, so nothing references the about-to-be-removed uid.
	if derr := disableWebdavWorker(ctx, p.Username); derr != nil {
		return nil, derr
	}
	// JAB-256: kill live sessions BEFORE userdel so the credential's open
	// connections drop with the account, not linger past it.
	terminateFtpSessions(ctx, p.Username, delUID)
	// -f is REQUIRED, not defensive: the alias shares the tenant's uid, so
	// the tenant's always-running processes (per-user FPM master, cron)
	// make plain userdel fail with "user is currently used by process"
	// (exit 8) — proven live on jabalitests. -f overrides only the in-use
	// check; home removal stays off (NEVER --remove: the home dir is a
	// live tenant directory, usually a docroot).
	if out, err := execCommandContext(ctx, "userdel", "-f", p.Username).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("userdel %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))}
	}
	// Isolated account: unmount + remove its jail AFTER the user is gone. The
	// teardown never touches the bind-mount source (tenant data); it is
	// idempotent and safe on a legacy path that has no jail.
	if isolated {
		if terr := teardownIsolatedJail(ctx, ftpJailPathFor(tenant, p.Username)); terr != nil {
			return nil, terr
		}
	}
	return map[string]any{"username": p.Username, "deleted": true}, nil
}

// ---- ftpaccount.list ----

type ftpAccountListParams struct {
	TenantUsername string `json:"tenant_username"`
}

type ftpAccountListEntry struct {
	Username  string `json:"username"`
	HomePath  string `json:"home_path"`
	FTPAccess bool   `json:"ftp_access"`
	Locked    bool   `json:"locked"`
	// WebdavEnabled reflects whether the WebDAV worker is provisioned (GH #1146),
	// so the per-tenant reconcile pass can heal a failed enable/disable. This is
	// the list the reconciler actually diffs (reconcileFtpTenant), so it MUST
	// carry the field — not just list_all.
	WebdavEnabled bool `json:"webdav_enabled"`
}

// ftpAccountListHandler reports the host's view of a tenant's subaccounts —
// the reconciler diffs this against the DB (DB is truth; extra host entries
// get removed, missing ones recreated).
func ftpAccountListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpAccountListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read passwd: %v", err)}
	}
	prefix := tenant.Username + "_"
	entries := []ftpAccountListEntry{}
	for _, line := range strings.Split(string(passwd), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 6 || !strings.HasPrefix(parts[0], prefix) {
			continue
		}
		uid, _ := strconv.Atoi(parts[2])
		if !ftpAliasOwnedBy(parts[4], uid, tenant) {
			continue // same prefix but not our alias (or an operator's) — skip
		}
		name := parts[0]
		isFtp, _ := isUserInGroup(ctx, name, ftpGroupName)
		locked := false
		if out, perr := execCommandContext(ctx, "passwd", "-S", name).Output(); perr == nil {
			fields := strings.Fields(string(out))
			locked = len(fields) >= 2 && fields[1] == "L"
		}
		entries = append(entries, ftpAccountListEntry{
			Username:      name,
			HomePath:      parts[5],
			FTPAccess:     isFtp,
			Locked:        locked,
			WebdavEnabled: webdavWorkerEnabled(name),
		})
	}
	return map[string]any{"accounts": entries}, nil
}

// ftpTenantAliasNames returns the tenant's panel-owned FTP alias
// usernames (namespaced + same-uid + GECOS-marked), read from /etc/passwd.
func ftpTenantAliasNames(tenant *ftpTenant) ([]string, *agentwire.AgentError) {
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read passwd: %v", err)}
	}
	return ftpOwnedAliasNames(string(passwd), tenant), nil
}

// ftpOwnedAliasNames is the pure filter behind ftpTenantAliasNames (and thus
// the JAB-254 suspension lock path). Owner-aware: includes isolated
// (separate-uid) aliases — which no longer share the tenant uid — while staying
// collision-proof for prefix-colliding tenants, so tenant suspension covers
// every owned alias regardless of isolation mode.
func ftpOwnedAliasNames(passwd string, tenant *ftpTenant) []string {
	prefix := tenant.Username + "_"
	var names []string
	for _, line := range strings.Split(passwd, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 6 || !strings.HasPrefix(parts[0], prefix) {
			continue
		}
		uid, _ := strconv.Atoi(parts[2])
		if !ftpAliasOwnedBy(parts[4], uid, tenant) {
			continue
		}
		names = append(names, parts[0])
	}
	return names
}

// ftpAccountLockTenantHandler locks EVERY panel-owned FTP/SFTP alias of a
// tenant immediately: usermod -L (blocks password auth for both SFTP and
// FTPS) + drop from jabali-ftp (closes the vsftpd PAM gate). Called
// synchronously from the tenant-suspension cascade (JAB-254) so a
// suspended owner's delegated credentials stop working at once, not on
// the next reconcile tick. Idempotent; unsuspension is handled by the
// reconciler restoring each alias to its row's desired state.
func ftpAccountLockTenantHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		TenantUsername string `json:"tenant_username"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	tenant, aerr := resolveFtpTenant(p.TenantUsername)
	if aerr != nil {
		return nil, aerr
	}
	names, aerr := ftpTenantAliasNames(tenant)
	if aerr != nil {
		return nil, aerr
	}
	locked := 0
	for _, name := range names {
		_ = setFtpGroupMembership(ctx, name, false)
		if out, err := execCommandContext(ctx, "usermod", "-L", name).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -L %q: %v: %s", name, err, strings.TrimSpace(string(out)))}
		}
		// JAB-256: a suspended owner's delegated sessions must drop at once,
		// not linger until the connection closes.
		if u, lerr := user.Lookup(name); lerr == nil {
			uid, _ := strconv.Atoi(u.Uid)
			terminateFtpSessions(ctx, name, uid)
		}
		locked++
	}
	return map[string]any{"locked": locked}, nil
}

// ---- ftpaccount.list_all ----

type ftpAccountListAllEntry struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	HomePath       string `json:"home_path"`
	FTPAccess      bool   `json:"ftp_access"`
	Locked         bool   `json:"locked"`
	// WebdavEnabled reflects whether the WebDAV worker is provisioned (GH #1146),
	// so the reconciler can heal a failed enable/disable. Drop-in-dir presence.
	WebdavEnabled bool `json:"webdav_enabled"`
}

// ftpAccountListAllHandler reports EVERY subaccount alias on the host in
// one call: passwd entries named "<other-entry>_<label>" that alias that
// other entry's uid (>= minTenantUID). The reconciler diffs this against
// the whole ftp_accounts table, so a stray alias is caught even when its
// tenant has zero remaining rows — a rowless alias is a working credential
// with no panel handle, which must never survive (proven live on
// jabalitests: a manually-deleted row left a login-able alias behind).
// gecosName returns the "name" segment of a raw passwd GECOS field (the
// part before the first comma) — matching how os/user parses User.Name.
func gecosName(gecos string) string {
	if i := strings.IndexByte(gecos, ','); i >= 0 {
		return gecos[:i]
	}
	return gecos
}

// classifyFtpAliases is the pure half of list_all: given raw /etc/passwd
// content it returns the panel-owned aliases (name pattern + shared uid +
// GECOS marker) and the count of pattern-matching entries that LACK the
// marker — operator property the sweep must never touch, surfaced so the
// reconciler can WARN instead of silently ignoring.
func classifyFtpAliases(passwd string) (owned []ftpAccountListAllEntry, skippedUnmarked int) {
	type pwEntry struct {
		uid   int
		home  string
		gecos string
	}
	byName := map[string]pwEntry{}
	var order []string
	for _, line := range strings.Split(passwd, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) < 6 {
			continue
		}
		uid, _ := strconv.Atoi(parts[2])
		byName[parts[0]] = pwEntry{uid: uid, home: parts[5], gecos: parts[4]}
		order = append(order, parts[0])
	}
	owned = []ftpAccountListAllEntry{}
	for _, name := range order {
		owner, marked := ftpMarkerOwner(byName[name].gecos)
		if marked && owner != "" {
			// Owner-encoded: ownership is explicit — no uid/shape inference
			// (isolated aliases do NOT share the tenant uid). Report it even if
			// the owner tenant is gone; a rowless alias is exactly what the
			// reaper must surface. A SYSTEM uid with an owner marker is never
			// ours (mirrors the legacy walk's minTenantUID guard) — ignore it.
			// Otherwise require the namespaced-username prefix to match the
			// encoded owner: a mismatch is a corrupted/forged marker (only root
			// writes passwd), never attributed to a tenant.
			if byName[name].uid < minTenantUID {
				continue
			}
			if strings.HasPrefix(name, owner+"_") {
				owned = append(owned, ftpAccountListAllEntry{
					TenantUsername: owner,
					Username:       name,
					HomePath:       byName[name].home,
				})
			} else {
				// Owner-encoded marker whose username is NOT namespaced under
				// the encoded owner. Not attacker-reachable (only root writes
				// passwd), but a PANEL stamping bug here would hide a live alias
				// from the reaper — the one thing the reaper must never do — so
				// surface it loudly instead of silently dropping the credential.
				slog.Warn("ftp reaper: owner-encoded alias with mismatched username prefix — possible stamping bug",
					"username", name, "encoded_owner", owner)
			}
			continue
		}
		// Legacy path (bare marker or no marker): needs the <tenant>_<label>
		// shape + a same-uid tenant entry to identify the owner. Walk every "_"
		// split point — tenant names may themselves contain underscores
		// (user_01k…), so "a_b_c" must try tenants "a_b" and "a".
		i := strings.LastIndex(name, "_")
		if i <= 0 {
			continue
		}
		for j := i; j > 0; j = strings.LastIndex(name[:j], "_") {
			tenant := name[:j]
			te, ok := byName[tenant]
			if !ok || te.uid < minTenantUID || te.uid != byName[name].uid || tenant == name {
				continue
			}
			if !marked {
				// Same shape, no marker: an operator's hand-made alias.
				skippedUnmarked++
				break
			}
			owned = append(owned, ftpAccountListAllEntry{
				TenantUsername: tenant,
				Username:       name,
				HomePath:       byName[name].home,
			})
			break
		}
	}
	return owned, skippedUnmarked
}

func ftpAccountListAllHandler(ctx context.Context, params json.RawMessage) (any, error) {
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read passwd: %v", err)}
	}
	entries, skipped := classifyFtpAliases(string(passwd))
	for i := range entries {
		locked := false
		if out, perr := execCommandContext(ctx, "passwd", "-S", entries[i].Username).Output(); perr == nil {
			fields := strings.Fields(string(out))
			locked = len(fields) >= 2 && fields[1] == "L"
		}
		entries[i].Locked = locked
		entries[i].FTPAccess, _ = isUserInGroup(ctx, entries[i].Username, ftpGroupName)
		entries[i].WebdavEnabled = webdavWorkerEnabled(entries[i].Username)
	}
	return map[string]any{"accounts": entries, "skipped_unmarked": skipped}, nil
}

func init() {
	Default.Register("ftpaccount.create", ftpAccountCreateHandler)
	Default.Register("ftpaccount.set_password", ftpAccountSetPasswordHandler)
	Default.Register("ftpaccount.set_access", ftpAccountSetAccessHandler)
	Default.Register("ftpaccount.delete", ftpAccountDeleteHandler)
	Default.Register("ftpaccount.list", ftpAccountListHandler)
	Default.Register("ftpaccount.list_all", ftpAccountListAllHandler)
	Default.Register("ftpaccount.lock_tenant", ftpAccountLockTenantHandler)
	Default.Register("ftpaccount.ensure_jail", ftpEnsureJailHandler)
	Default.Register("ftpaccount.isolation_status", ftpIsolationStatusHandler)
}
