package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// pingFake swaps the exec seam, the tenant check and the two sysctl paths.
// getent answers with *groupLine (exit 2 when it is empty, as getent does for
// a missing group); groupadd creates the group with gid 977; every other
// command is recorded and succeeds.
type pingFake struct {
	groupLine string
	execs     []string
	confPath  string
	procPath  string
}

func newPingFake(t *testing.T, groupLine string, tenants map[string]error) *pingFake {
	t.Helper()
	f := &pingFake{groupLine: groupLine}
	dir := t.TempDir()
	f.confPath = filepath.Join(dir, "60-jabali-ping.conf")
	f.procPath = filepath.Join(dir, "ping_group_range")
	if err := os.WriteFile(f.procPath, []byte("1\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevExec, prevTenant := execCommandContext, pingTenantCheck
	prevConf, prevProc := pingSysctlConfPath, pingRangeProcPath
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "getent":
			if f.groupLine == "" {
				return exec.CommandContext(ctx, "sh", "-c", "exit 2")
			}
			return exec.CommandContext(ctx, "printf", "%s\n", f.groupLine)
		case "groupadd":
			f.execs = append(f.execs, name+" "+strings.Join(args, " "))
			f.groupLine = pingGroupName + ":x:977:"
			return exec.CommandContext(ctx, "true")
		}
		f.execs = append(f.execs, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "true")
	}
	pingTenantCheck = func(name string) error {
		if err, ok := tenants[name]; ok {
			return err
		}
		return errors.New("no such user")
	}
	pingSysctlConfPath, pingRangeProcPath = f.confPath, f.procPath
	t.Cleanup(func() {
		execCommandContext, pingTenantCheck = prevExec, prevTenant
		pingSysctlConfPath, pingRangeProcPath = prevConf, prevProc
	})
	return f
}

func applyPing(t *testing.T, params string) (*pingAccessResponse, error) {
	t.Helper()
	out, err := userPingAccessApplyHandler(context.Background(), json.RawMessage(params))
	if err != nil {
		return nil, err
	}
	return out.(*pingAccessResponse), nil
}

// The group ends up with exactly the tenants the panel sent. Names that are not
// tenants (root, a system account, a missing account, a malformed name) are
// refused and never added: a compromised panel must not be able to put a
// system account in the group.
func TestPingAccessApply_SetsExactlyTheTenantMembers(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:old,keep", map[string]error{
		"keep":    nil,
		"new":     nil,
		"root":    errors.New("uid 0"),
		"sysacct": errors.New("uid 500"),
	})
	resp, err := applyPing(t, `{"members":["keep","new","root","sysacct","ghost","Bad name"]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"usermod -aG " + pingGroupName + " new", "gpasswd -d old " + pingGroupName}
	if strings.Join(f.execs, "|") != strings.Join(want, "|") {
		t.Fatalf("execs = %q, want %q", f.execs, want)
	}
	for _, name := range []string{"root", "sysacct", "ghost", "Bad name"} {
		if _, ok := resp.Refused[name]; !ok {
			t.Errorf("%q not reported as refused: %+v", name, resp.Refused)
		}
	}
	if _, ok := resp.Refused["keep"]; ok {
		t.Errorf("a valid tenant was refused: %+v", resp.Refused)
	}
}

// The kernel lets a process open an ICMP ping socket only when one of its
// groups is inside net.ipv4.ping_group_range. The range becomes exactly the
// jabali-ping gid, at runtime and in the sysctl.d file that survives a reboot.
func TestPingAccessApply_SetsThePingGroupRange(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:", map[string]error{"a": nil})
	resp, err := applyPing(t, `{"members":["a"]}`)
	if err != nil {
		t.Fatal(err)
	}
	proc, _ := os.ReadFile(f.procPath)
	if strings.Join(strings.Fields(string(proc)), " ") != "977 977" {
		t.Errorf("runtime range = %q, want 977 977", proc)
	}
	conf, _ := os.ReadFile(f.confPath)
	if !strings.Contains(string(conf), "\nnet.ipv4.ping_group_range = 977 977\n") {
		t.Errorf("sysctl.d file = %q, want the 977 977 range", conf)
	}
	if resp.Range != "977 977" {
		t.Errorf("resp.Range = %q", resp.Range)
	}
}

// A distribution that opens ping sockets to every group (Debian 13's systemd
// 50-default.conf: 0 2147483647) is narrowed to the one group: ping is a
// per-package allowance.
func TestPingAccessApply_NarrowsAnOpenRange(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:", nil)
	if err := os.WriteFile(f.procPath, []byte("0\t2147483647\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPing(t, `{"members":[]}`); err != nil {
		t.Fatal(err)
	}
	proc, _ := os.ReadFile(f.procPath)
	if strings.Join(strings.Fields(string(proc)), " ") != "977 977" {
		t.Errorf("runtime range = %q, want 977 977", proc)
	}
}

// Steady state: members, runtime range and file already match — no usermod,
// no gpasswd, and the file is not rewritten.
func TestPingAccessApply_SteadyStateChangesNothing(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:a", map[string]error{"a": nil})
	if _, err := applyPing(t, `{"members":["a"]}`); err != nil {
		t.Fatal(err)
	}
	f.execs = nil
	before, _ := os.Stat(f.confPath)
	if _, err := applyPing(t, `{"members":["a"]}`); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Errorf("steady state ran %q", f.execs)
	}
	after, _ := os.Stat(f.confPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("steady state rewrote the sysctl.d file")
	}
}

// An empty list is a real answer (no package allows ping) and clears the group.
func TestPingAccessApply_EmptyListClearsTheGroup(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:a,b", nil)
	if _, err := applyPing(t, `{"members":[]}`); err != nil {
		t.Fatal(err)
	}
	want := []string{"gpasswd -d a " + pingGroupName, "gpasswd -d b " + pingGroupName}
	if strings.Join(f.execs, "|") != strings.Join(want, "|") {
		t.Fatalf("execs = %q, want %q", f.execs, want)
	}
}

// A call without the members field is refused rather than read as "clear the
// group".
func TestPingAccessApply_MissingMembersIsRefused(t *testing.T) {
	f := newPingFake(t, pingGroupName+":x:977:a", nil)
	_, err := applyPing(t, `{}`)
	var aerr *agentwire.AgentError
	if !errors.As(err, &aerr) || aerr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid argument", err)
	}
	if len(f.execs) != 0 {
		t.Errorf("ran %q on a refused call", f.execs)
	}
}

// A host that predates the installer's jabali-ping group gets it on the first
// apply.
func TestPingAccessApply_CreatesTheGroup(t *testing.T) {
	f := newPingFake(t, "", map[string]error{"a": nil})
	if _, err := applyPing(t, `{"members":["a"]}`); err != nil {
		t.Fatal(err)
	}
	want := []string{"groupadd --system " + pingGroupName, "usermod -aG " + pingGroupName + " a"}
	if strings.Join(f.execs, "|") != strings.Join(want, "|") {
		t.Fatalf("execs = %q, want %q", f.execs, want)
	}
}
