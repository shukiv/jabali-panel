package commands

// user.ping_access.apply (GH #1798) lets the tenants whose hosting package
// allows ping run it from their SSH shell.
//
// The SSH sandbox (bwrap) sets no_new_privs, so the cap_net_raw file
// capability on /usr/bin/ping is ignored there and its raw ICMP socket fails
// with EPERM. The kernel's ICMP "ping" sockets need no capability: a process
// may open one when its primary or a supplementary group is inside
// net.ipv4.ping_group_range, which also covers ICMPv6 ping sockets. The range
// is per network namespace, and the sandbox shares the host's
// (--unshare-all --share-net). A supplementary group still counts inside the
// sandbox's user namespace, where it shows as nogroup.
//
// This verb keeps:
//   - the jabali-ping system group, whose members are exactly the tenants the
//     panel sends (users whose package has egress_icmp);
//   - net.ipv4.ping_group_range set to that group's gid alone, at runtime and
//     in /etc/sysctl.d/60-jabali-ping.conf so it survives a reboot.
//
// The range is narrowed to the one group even where the distribution opens it
// to every group (Debian 13's systemd 50-default.conf sets 0 2147483647):
// ping is a per-package allowance, off by default. For enforced and learning
// users the per-user egress firewall also has to allow it (allow_ping in
// user.egress.apply). A membership change takes effect at the tenant's next
// login, because a session's supplementary groups are fixed when it starts.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const pingGroupName = "jabali-ping"

// Seams for tests.
var (
	pingSysctlConfPath = "/etc/sysctl.d/60-jabali-ping.conf"
	pingRangeProcPath  = "/proc/sys/net/ipv4/ping_group_range"
	pingTenantCheck    = pingTenantAccount
)

type pingAccessParams struct {
	// Members is a pointer so a call without the field is refused instead of
	// being read as "clear the group".
	Members *[]string `json:"members"`
}

type pingAccessResponse struct {
	GID     int      `json:"gid"`
	Range   string   `json:"range"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Refused maps each name that was not added to the reason.
	Refused map[string]string `json:"refused,omitempty"`
}

func userPingAccessApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pingAccessParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if p.Members == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "members is required (an empty list clears the group)"}
	}

	resp := &pingAccessResponse{Refused: map[string]string{}}
	want := map[string]bool{}
	for _, name := range *p.Members {
		if !usernameRegex.MatchString(name) {
			resp.Refused[name] = "invalid username"
			continue
		}
		if err := pingTenantCheck(name); err != nil {
			resp.Refused[name] = err.Error()
			continue
		}
		want[name] = true
	}

	gid, current, aerr := ensurePingGroup(ctx)
	if aerr != nil {
		return nil, aerr
	}
	resp.GID = gid

	have := map[string]bool{}
	for _, m := range current {
		have[m] = true
	}
	for _, name := range sortedKeys(want) {
		if have[name] {
			continue
		}
		if out, err := execCommandContext(ctx, "usermod", "-aG", pingGroupName, name).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("add %q to %s: %v: %s", name, pingGroupName, err, strings.TrimSpace(string(out)))}
		}
		resp.Added = append(resp.Added, name)
	}
	for _, name := range sortedKeys(have) {
		if want[name] {
			continue
		}
		if out, err := execCommandContext(ctx, "gpasswd", "-d", name, pingGroupName).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove %q from %s: %v: %s", name, pingGroupName, err, strings.TrimSpace(string(out)))}
		}
		resp.Removed = append(resp.Removed, name)
	}

	rng, aerr := ensurePingGroupRange(gid)
	if aerr != nil {
		return nil, aerr
	}
	resp.Range = rng
	if len(resp.Refused) == 0 {
		resp.Refused = nil
	}
	return resp, nil
}

// pingTenantAccount accepts only an existing regular account (uid >= 1000):
// whatever the panel sends, root and system accounts never join the group.
func pingTenantAccount(name string) error {
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("no such user")
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid < minTenantUID {
		return fmt.Errorf("uid %s is not a tenant uid (>= %d)", u.Uid, minTenantUID)
	}
	return nil
}

// ensurePingGroup returns the group's gid and current members, creating the
// group when it is missing.
func ensurePingGroup(ctx context.Context) (int, []string, *agentwire.AgentError) {
	line, ok := pingGroupEntry(ctx)
	if !ok {
		if out, err := execCommandContext(ctx, "groupadd", "--system", pingGroupName).CombinedOutput(); err != nil {
			return 0, nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("groupadd %s: %v: %s", pingGroupName, err, strings.TrimSpace(string(out)))}
		}
		if line, ok = pingGroupEntry(ctx); !ok {
			return 0, nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: pingGroupName + " missing after groupadd"}
		}
	}
	name, gid, members, err := parseGroupLine(line)
	if err != nil || name != pingGroupName {
		return 0, nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("unreadable %s entry %q", pingGroupName, line)}
	}
	if gid <= 0 {
		return 0, nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("%s has gid %d; refusing to open ping sockets to it", pingGroupName, gid)}
	}
	return gid, members, nil
}

func pingGroupEntry(ctx context.Context) (string, bool) {
	var out bytes.Buffer
	cmd := execCommandContext(ctx, "getent", "group", pingGroupName)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", false
	}
	line := strings.TrimSpace(out.String())
	return line, line != ""
}

// ensurePingGroupRange sets net.ipv4.ping_group_range to "gid gid" in the
// sysctl.d file and at runtime, writing only what differs.
func ensurePingGroupRange(gid int) (string, *agentwire.AgentError) {
	rng := fmt.Sprintf("%d %d", gid, gid)
	conf := "# Managed by jabali (GH #1798). Do NOT hand-edit.\n" +
		"# Only members of " + pingGroupName + " (tenants whose hosting package allows\n" +
		"# ping) may open ICMP ping sockets.\n" +
		"net.ipv4.ping_group_range = " + rng + "\n"
	if cur, err := os.ReadFile(pingSysctlConfPath); err != nil || string(cur) != conf {
		if err := writeFileAtomically(pingSysctlConfPath, []byte(conf), 0o644); err != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write %s: %v", pingSysctlConfPath, err)}
		}
	}
	cur, err := os.ReadFile(pingRangeProcPath)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", pingRangeProcPath, err)}
	}
	if strings.Join(strings.Fields(string(cur)), " ") != rng {
		if err := os.WriteFile(pingRangeProcPath, []byte(rng+"\n"), 0o644); err != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("set %s: %v", pingRangeProcPath, err)}
		}
	}
	return rng, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func init() {
	Default.Register("user.ping_access.apply", userPingAccessApplyHandler)
}
