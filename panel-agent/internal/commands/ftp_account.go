package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// FTP/SFTP subaccounts (GH #1053, plans/gh1053-ftp-accounts.md).
//
// A subaccount is a SECOND passwd entry sharing the owning tenant's uid/gid
// (useradd --non-unique): its own username + password + home directory, but
// every file it writes is owned by the tenant uid, so quotas, per-user FPM,
// AppArmor, and backups treat its writes exactly like the tenant's own.
// Sessions also land in the tenant's user-<uid>.slice, so M18 resource
// limits apply unchanged.
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
	if uid != tenant.UID {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("passwd entry %q has uid %d, expected tenant uid %d — refusing to touch a non-subaccount user", sub, uid, tenant.UID),
		}
	}
	// Ownership marker: name+uid alone can match an operator's hand-made
	// alias — those are never ours to re-password, lock, or delete.
	if u.Name != ftpAliasGecos {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("passwd entry %q lacks the %q ownership marker — operator-managed account, refusing", sub, ftpAliasGecos),
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
	if out, err := exec.CommandContext(ctx, "groupadd", "--system", ftpGroupName).CombinedOutput(); err != nil {
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
		if out, err := exec.CommandContext(ctx, "usermod", "-aG", ftpGroupName, username).CombinedOutput(); err != nil {
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
	if out, gerr := exec.CommandContext(ctx, "gpasswd", "-d", username, ftpGroupName).CombinedOutput(); gerr != nil {
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
	cmd := exec.CommandContext(ctx, "chpasswd")
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
}

type ftpAccountCreateResponse struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	HomePath string `json:"home_path"`
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
		"--comment", ftpAliasGecos,
		p.Username,
	}
	if out, err := exec.CommandContext(ctx, "useradd", args...).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("useradd %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))}
	}
	rollback := func() {
		// -f: the alias shares the tenant's uid, so the tenant's own
		// processes (FPM pool, cron) always count as "user in use" and a
		// plain userdel exits 8. Never --remove: home is tenant data.
		_ = exec.CommandContext(ctx, "userdel", "-f", p.Username).Run()
	}
	// Home must exist for vsftpd chroot + internal-sftp start dir. Owned
	// by the tenant uid like every other node in their tree.
	if err := os.MkdirAll(homePath, 0o755); err != nil {
		rollback()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create home %q: %v", homePath, err)}
	}
	if err := os.Chown(homePath, tenant.UID, tenant.GID); err != nil {
		rollback()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown home %q: %v", homePath, err)}
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
	return ftpAccountCreateResponse{Username: p.Username, UID: tenant.UID, HomePath: homePath}, nil
}

// ---- ftpaccount.set_password ----

type ftpAccountSetPasswordParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	Password       string `json:"password"`
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
	if _, aerr := requireFtpSubaccount(tenant, p.Username); aerr != nil {
		return nil, aerr
	}
	if p.Password == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "password is required"}
	}
	if aerr := chpasswdStdin(ctx, p.Username, p.Password); aerr != nil {
		return nil, aerr
	}
	return map[string]any{"username": p.Username, "updated": true}, nil
}

// ---- ftpaccount.set_access ----

type ftpAccountSetAccessParams struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	FTPAccess      bool   `json:"ftp_access"`
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
	if _, aerr := requireFtpSubaccount(tenant, p.Username); aerr != nil {
		return nil, aerr
	}
	if aerr := setFtpGroupMembership(ctx, p.Username, p.FTPAccess); aerr != nil {
		return nil, aerr
	}
	lockFlag := "-U"
	if !p.Enabled {
		lockFlag = "-L"
	}
	if out, err := exec.CommandContext(ctx, "usermod", lockFlag, p.Username).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod %s %q: %v: %s", lockFlag, p.Username, err, strings.TrimSpace(string(out)))}
	}
	return map[string]any{"username": p.Username, "ftp_access": p.FTPAccess, "enabled": p.Enabled}, nil
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
	if _, aerr := requireFtpSubaccount(tenant, p.Username); aerr != nil {
		// Idempotent delete: a passwd entry that is already gone is success.
		if aerr.Code == agentwire.CodeNotFound {
			return map[string]any{"username": p.Username, "deleted": false, "absent": true}, nil
		}
		return nil, aerr
	}
	_ = setFtpGroupMembership(ctx, p.Username, false)
	// -f is REQUIRED, not defensive: the alias shares the tenant's uid, so
	// the tenant's always-running processes (per-user FPM master, cron)
	// make plain userdel fail with "user is currently used by process"
	// (exit 8) — proven live on jabalitests. -f overrides only the in-use
	// check; home removal stays off (NEVER --remove: the home dir is a
	// live tenant directory, usually a docroot).
	if out, err := exec.CommandContext(ctx, "userdel", "-f", p.Username).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("userdel %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))}
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
		if uid, _ := strconv.Atoi(parts[2]); uid != tenant.UID {
			continue // same prefix but not our alias — not ours to report
		}
		if gecosName(parts[4]) != ftpAliasGecos {
			continue // operator-made alias — invisible to the panel
		}
		name := parts[0]
		isFtp, _ := isUserInGroup(ctx, name, ftpGroupName)
		locked := false
		if out, perr := exec.CommandContext(ctx, "passwd", "-S", name).Output(); perr == nil {
			fields := strings.Fields(string(out))
			locked = len(fields) >= 2 && fields[1] == "L"
		}
		entries = append(entries, ftpAccountListEntry{
			Username:  name,
			HomePath:  parts[5],
			FTPAccess: isFtp,
			Locked:    locked,
		})
	}
	return map[string]any{"accounts": entries}, nil
}

// ---- ftpaccount.list_all ----

type ftpAccountListAllEntry struct {
	TenantUsername string `json:"tenant_username"`
	Username       string `json:"username"`
	HomePath       string `json:"home_path"`
	FTPAccess      bool   `json:"ftp_access"`
	Locked         bool   `json:"locked"`
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
		i := strings.LastIndex(name, "_")
		if i <= 0 {
			continue
		}
		// Walk every "_" split point: tenant names may themselves contain
		// underscores (user_01k…), so "a_b_c" must try tenants "a_b" and "a".
		for j := i; j > 0; j = strings.LastIndex(name[:j], "_") {
			tenant := name[:j]
			te, ok := byName[tenant]
			if !ok || te.uid < minTenantUID || te.uid != byName[name].uid || tenant == name {
				continue
			}
			if gecosName(byName[name].gecos) != ftpAliasGecos {
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
		if out, perr := exec.CommandContext(ctx, "passwd", "-S", entries[i].Username).Output(); perr == nil {
			fields := strings.Fields(string(out))
			locked = len(fields) >= 2 && fields[1] == "L"
		}
		entries[i].Locked = locked
		entries[i].FTPAccess, _ = isUserInGroup(ctx, entries[i].Username, ftpGroupName)
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
}
