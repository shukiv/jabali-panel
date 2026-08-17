// os_users stage apply (GH #331 two-node drill): materialize the SOURCE
// host's tenant Linux users onto this box, add-missing-only. A promoted DR
// standby (or a disaster-restore target) starts with none of the tenant
// users — account creation is imperative at signup, not reconciled — so the
// account-data restore rsyncs into nothing and the reconciler's domain
// create loops on "unknown user" without this.
//
// Safety posture:
//   - never modifies an existing user, group, or password;
//   - a name that exists locally with a DIFFERENT uid/gid is skipped with a
//     warning (the operator must resolve that collision by hand);
//   - a wanted uid/gid already taken by another name is likewise skip+warn;
//   - password hashes are fed to `chpasswd -e` via stdin, never argv.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// osUserEntry is one parsed passwd line.
type osUserEntry struct {
	Name, Home, Shell, Gecos string
	UID, GID                 int
}

// parsePasswdLine parses `name:x:uid:gid:gecos:home:shell`.
func parsePasswdLine(line string) (*osUserEntry, error) {
	f := strings.Split(line, ":")
	if len(f) != 7 {
		return nil, fmt.Errorf("malformed passwd line (%d fields)", len(f))
	}
	uid, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, fmt.Errorf("uid %q: %w", f[2], err)
	}
	gid, err := strconv.Atoi(f[3])
	if err != nil {
		return nil, fmt.Errorf("gid %q: %w", f[3], err)
	}
	return &osUserEntry{Name: f[0], UID: uid, GID: gid, Gecos: f[4], Home: f[5], Shell: f[6]}, nil
}

// parseGroupLine parses `name:x:gid:member,member`.
func parseGroupLine(line string) (name string, gid int, members []string, err error) {
	f := strings.Split(line, ":")
	if len(f) != 4 {
		return "", 0, nil, fmt.Errorf("malformed group line (%d fields)", len(f))
	}
	gid, err = strconv.Atoi(f[2])
	if err != nil {
		return "", 0, nil, fmt.Errorf("gid %q: %w", f[2], err)
	}
	for _, m := range strings.Split(f[3], ",") {
		if m = strings.TrimSpace(m); m != "" {
			members = append(members, m)
		}
	}
	return f[0], gid, members, nil
}

// shadowHashes maps user → password hash from the bundle's shadow lines.
func shadowHashes(shadow []string) map[string]string {
	out := map[string]string{}
	for _, line := range shadow {
		f := strings.Split(line, ":")
		if len(f) >= 2 && f[0] != "" {
			out[f[0]] = f[1]
		}
	}
	return out
}

// applyOSUsersMerge reads <stageDir>/os_users.json and creates every group +
// user that does not exist locally, preserving uid/gid, shell, home path,
// and password hash. Group memberships are re-asserted additively at the
// end. Returns (applied, warnings); it never fails the whole restore.
func applyOSUsersMerge(ctx context.Context, stageDir string) ([]string, []string) {
	var applied, warnings []string
	src := filepath.Join(stageDir, "os_users.json")
	if _, err := os.Stat(src); err != nil {
		src = filepath.Join(stageDir, "stdin")
	}
	raw, err := os.ReadFile(src) // #nosec G304 — server-controlled stage dir
	if err != nil {
		return nil, []string{fmt.Sprintf("os_users: read blob: %v", err)}
	}
	var bundle osUsersBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, []string{fmt.Sprintf("os_users: parse blob: %v", err)}
	}
	hashes := shadowHashes(bundle.Shadow)

	// Groups first — users reference their primary gid.
	type wantedGroup struct {
		name    string
		members []string
	}
	var groups []wantedGroup
	for _, line := range bundle.Group {
		name, gid, members, perr := parseGroupLine(line)
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("os_users: %v", perr))
			continue
		}
		groups = append(groups, wantedGroup{name: name, members: members})
		if g, lerr := user.LookupGroup(name); lerr == nil {
			if g.Gid != strconv.Itoa(gid) {
				warnings = append(warnings,
					fmt.Sprintf("os_users: group %q exists with gid %s (source %d) — leaving as-is", name, g.Gid, gid))
			}
			continue
		}
		if g, lerr := user.LookupGroupId(strconv.Itoa(gid)); lerr == nil {
			warnings = append(warnings,
				fmt.Sprintf("os_users: gid %d already belongs to group %q — cannot create %q, skipped", gid, g.Name, name))
			continue
		}
		if out, cerr := exec.CommandContext(ctx, "groupadd", "-g", strconv.Itoa(gid), name).CombinedOutput(); cerr != nil {
			warnings = append(warnings, fmt.Sprintf("os_users: groupadd %s: %v (%s)", name, cerr, strings.TrimSpace(string(out))))
			continue
		}
		applied = append(applied, fmt.Sprintf("os_users: group %s (gid %d)", name, gid))
	}

	for _, line := range bundle.Passwd {
		e, perr := parsePasswdLine(line)
		if perr != nil {
			warnings = append(warnings, fmt.Sprintf("os_users: %v", perr))
			continue
		}
		if u, lerr := user.Lookup(e.Name); lerr == nil {
			if u.Uid != strconv.Itoa(e.UID) {
				warnings = append(warnings,
					fmt.Sprintf("os_users: user %q exists with uid %s (source %d) — restored files will carry the SOURCE uid; resolve by hand", e.Name, u.Uid, e.UID))
			}
			continue
		}
		if u, lerr := user.LookupId(strconv.Itoa(e.UID)); lerr == nil {
			warnings = append(warnings,
				fmt.Sprintf("os_users: uid %d already belongs to %q — cannot create %q, skipped", e.UID, u.Username, e.Name))
			continue
		}
		args := []string{
			"--no-create-home", // home layout is the sftp-chroot reconciler's job
			"--uid", strconv.Itoa(e.UID),
			"--home-dir", e.Home,
			"--shell", e.Shell,
			"--comment", e.Gecos,
		}
		if _, gerr := user.LookupGroupId(strconv.Itoa(e.GID)); gerr == nil {
			args = append(args, "--gid", strconv.Itoa(e.GID))
		} else {
			// Primary group missing (filtered out of the bundle or a
			// collision above) — let useradd create the usergroup.
			warnings = append(warnings,
				fmt.Sprintf("os_users: user %q source gid %d absent — using a fresh usergroup", e.Name, e.GID))
		}
		args = append(args, e.Name)
		if out, cerr := exec.CommandContext(ctx, "useradd", args...).CombinedOutput(); cerr != nil {
			warnings = append(warnings, fmt.Sprintf("os_users: useradd %s: %v (%s)", e.Name, cerr, strings.TrimSpace(string(out))))
			continue
		}
		// M12 sftp-chroot home shape: root-owned 0751 shell; the ssh-keys
		// reconciler + the account-restore rsync fill and finalize it.
		if merr := os.MkdirAll(e.Home, 0o751); merr != nil {
			warnings = append(warnings, fmt.Sprintf("os_users: mkdir %s: %v", e.Home, merr))
		}
		if h := hashes[e.Name]; h != "" && h != "*" && h != "!" {
			cmd := exec.CommandContext(ctx, "chpasswd", "-e")
			cmd.Stdin = strings.NewReader(e.Name + ":" + h + "\n")
			if out, cerr := cmd.CombinedOutput(); cerr != nil {
				warnings = append(warnings, fmt.Sprintf("os_users: chpasswd %s: %v (%s)", e.Name, cerr, strings.TrimSpace(string(out))))
			}
		}
		applied = append(applied, fmt.Sprintf("os_users: user %s (uid %d)", e.Name, e.UID))
	}

	// Memberships last, additive only, both sides must exist by now.
	for _, g := range groups {
		for _, m := range g.members {
			if _, uerr := user.Lookup(m); uerr != nil {
				continue
			}
			if _, gerr := user.LookupGroup(g.name); gerr != nil {
				continue
			}
			if out, cerr := exec.CommandContext(ctx, "usermod", "-aG", g.name, m).CombinedOutput(); cerr != nil {
				warnings = append(warnings, fmt.Sprintf("os_users: usermod -aG %s %s: %v (%s)", g.name, m, cerr, strings.TrimSpace(string(out))))
			}
		}
	}
	return applied, warnings
}
