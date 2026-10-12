package commands

// Since GH #273, cron.apply writes a tenant's units into
// ~/.config/systemd/user, but cron.remove and cron.list still looked in
// /etc/jabali-panel/cron-units/<user>. Deleting or disabling a job reported
// no change and left its timer running, and the reconciler's orphan sweep
// never saw the units.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const (
	cronTestJob  = "01M4PX4G65CAKB2W9MA5QGTMW6"
	cronTestJob2 = "01M4F9SX8FSA72EKDMX6SX3BJH"
	cronTestJob3 = "01M4FE7CE303RMFTB20NMM9R9R"
)

// cronTestEnv points the cron handlers at a fake tenant "alice" whose home is
// a temp dir, a temp legacy root, and a systemctl that records its calls.
type cronTestEnv struct {
	home, legacy string
	calls        [][]string
	// fail, when set, decides the exit of a recorded call: a non-empty
	// return is written to stderr and the call exits 1.
	fail func(args []string) string
}

func newCronTestEnv(t *testing.T) *cronTestEnv {
	t.Helper()
	cur, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	env := &cronTestEnv{home: t.TempDir(), legacy: t.TempDir()}
	fake := &user.User{Username: "alice", Uid: cur.Uid, Gid: cur.Gid, HomeDir: env.home}

	prevLookup, prevLegacy, prevExec := lookupCronUser, legacyCronUnitsRoot, execCommandContext
	t.Cleanup(func() { lookupCronUser, legacyCronUnitsRoot, execCommandContext = prevLookup, prevLegacy, prevExec })
	lookupCronUser = func(name string) (*user.User, error) {
		if name == "alice" {
			return fake, nil
		}
		return nil, user.UnknownUserError(name)
	}
	legacyCronUnitsRoot = env.legacy
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		call := append([]string{name}, args...)
		env.calls = append(env.calls, call)
		if env.fail != nil {
			if msg := env.fail(call); msg != "" {
				return exec.CommandContext(ctx, "sh", "-c", `echo "$1" >&2; exit 1`, "sh", msg)
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	return env
}

func (e *cronTestEnv) unitsDir() string { return filepath.Join(e.home, ".config", "systemd", "user") }

// plant writes a job's service and timer into dir; enabled adds the
// timers.target.wants link `systemctl --user enable` makes.
func plant(t *testing.T, dir, jobID string, enabled bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".service", ".timer"} {
		if err := os.WriteFile(filepath.Join(dir, "jabali-cron-"+jobID+ext), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if enabled {
		wants := filepath.Join(dir, "timers.target.wants")
		if err := os.MkdirAll(wants, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "jabali-cron-"+jobID+".timer"), filepath.Join(wants, "jabali-cron-"+jobID+".timer")); err != nil {
			t.Fatal(err)
		}
	}
}

func lexists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func cronRemove(t *testing.T, jobID string) (*cronRemoveResponse, error) {
	t.Helper()
	params, _ := json.Marshal(cronRemoveParams{UserID: "u1", Username: "alice", JobID: jobID})
	res, err := cronRemoveHandler(context.Background(), params)
	if err != nil {
		return nil, err
	}
	return res.(*cronRemoveResponse), nil
}

func cronList(t *testing.T, username string) ([]string, error) {
	t.Helper()
	params, _ := json.Marshal(cronListParams{UserID: "u1", Username: username})
	res, err := cronListHandler(context.Background(), params)
	if err != nil {
		return nil, err
	}
	return res.(*cronListResponse).UnitFiles, nil
}

// systemctlCalls returns the `systemctl --user` arguments of each recorded call.
func (e *cronTestEnv) systemctlCalls() []string {
	var out []string
	for _, c := range e.calls {
		for i, a := range c {
			if a == "--user" {
				out = append(out, strings.Join(c[i+1:], " "))
			}
		}
	}
	return out
}

func TestCronRemove_RemovesTheUnitsCronApplyWrote(t *testing.T) {
	env := newCronTestEnv(t)
	plant(t, env.unitsDir(), cronTestJob, true)
	plant(t, env.unitsDir(), cronTestJob2, true) // another job, kept

	resp, err := cronRemove(t, cronTestJob)
	if err != nil {
		t.Fatal(err)
	}
	if resp.NoChange {
		t.Fatal("no_change for a job whose units are on disk: its timer keeps running")
	}
	for _, p := range []string{
		filepath.Join(env.unitsDir(), "jabali-cron-"+cronTestJob+".service"),
		filepath.Join(env.unitsDir(), "jabali-cron-"+cronTestJob+".timer"),
		filepath.Join(env.unitsDir(), "timers.target.wants", "jabali-cron-"+cronTestJob+".timer"),
	} {
		if lexists(p) {
			t.Errorf("%s is still there", p)
		}
	}
	if !lexists(filepath.Join(env.unitsDir(), "jabali-cron-"+cronTestJob2+".timer")) {
		t.Error("removed another job's timer")
	}
	got := strings.Join(env.systemctlCalls(), " | ")
	if got != "disable --now jabali-cron-"+cronTestJob+".timer | daemon-reload" {
		t.Errorf("systemctl --user calls = %q, want the timer disabled then a daemon-reload", got)
	}
}

func TestCronRemove_CleansTheLegacyLocation(t *testing.T) {
	env := newCronTestEnv(t)
	plant(t, filepath.Join(env.legacy, "alice"), cronTestJob, false)

	resp, err := cronRemove(t, cronTestJob)
	if err != nil {
		t.Fatal(err)
	}
	if resp.NoChange {
		t.Error("no_change with units in the legacy location")
	}
	if lexists(filepath.Join(env.legacy, "alice", "jabali-cron-"+cronTestJob+".timer")) {
		t.Error("legacy timer is still there")
	}
}

func TestCronRemove_NothingOnDiskIsNoChange(t *testing.T) {
	env := newCronTestEnv(t)

	resp, err := cronRemove(t, cronTestJob)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.NoChange {
		t.Error("want no_change with no units on disk")
	}
	if len(env.calls) != 0 {
		t.Errorf("ran %v with nothing to remove", env.calls)
	}
}

// The agent runs as root inside a tenant-writable home: a symlinked .config
// must not redirect the removal outside it.
func TestCronRemove_RefusesASymlinkedConfig(t *testing.T) {
	env := newCronTestEnv(t)
	outside := t.TempDir()
	plant(t, filepath.Join(outside, "systemd", "user"), cronTestJob, false)
	if err := os.Symlink(outside, filepath.Join(env.home, ".config")); err != nil {
		t.Fatal(err)
	}

	if _, err := cronRemove(t, cronTestJob); err == nil {
		t.Error("removal through a symlinked .config was not refused")
	}
	if !lexists(filepath.Join(outside, "systemd", "user", "jabali-cron-"+cronTestJob+".timer")) {
		t.Error("root removed a file outside the tenant's home")
	}
}

// With the user's systemd manager down, the units still go, and the panel is
// told so it keeps the row.
func TestCronRemove_UnreachableManagerStillRemovesTheUnits(t *testing.T) {
	env := newCronTestEnv(t)
	plant(t, env.unitsDir(), cronTestJob, true)
	env.fail = func(args []string) string {
		if strings.Contains(strings.Join(args, " "), "disable --now") {
			return "Failed to connect to bus: Connection refused"
		}
		return ""
	}

	_, err := cronRemove(t, cronTestJob)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != "user_manager_unreachable" {
		t.Fatalf("err = %v, want user_manager_unreachable", err)
	}
	for _, p := range []string{
		filepath.Join(env.unitsDir(), "jabali-cron-"+cronTestJob+".service"),
		filepath.Join(env.unitsDir(), "jabali-cron-"+cronTestJob+".timer"),
		filepath.Join(env.unitsDir(), "timers.target.wants", "jabali-cron-"+cronTestJob+".timer"),
	} {
		if lexists(p) {
			t.Errorf("%s is still there", p)
		}
	}
}

func TestCronList_ListsTheUnitsCronApplyWrote(t *testing.T) {
	env := newCronTestEnv(t)
	plant(t, env.unitsDir(), cronTestJob, true)
	plant(t, env.unitsDir(), cronTestJob2, false)
	plant(t, filepath.Join(env.legacy, "alice"), cronTestJob3, false)
	plant(t, filepath.Join(env.legacy, "alice"), cronTestJob, false) // in both places
	// Not cron job timers: cron.remove would refuse these ids.
	for _, name := range []string{"jabali-cron-notaulid.timer", "backup.timer", "jabali-cron-.timer"} {
		if err := os.WriteFile(filepath.Join(env.unitsDir(), name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := cronList(t, "alice")
	if err != nil {
		t.Fatal(err)
	}
	want := cronTestJob2 + "," + cronTestJob3 + "," + cronTestJob // sorted
	if strings.Join(got, ",") != want {
		t.Errorf("cron.list = %v, want %s", got, want)
	}
}

func TestCronList_NoUnitsIsEmpty(t *testing.T) {
	newCronTestEnv(t)
	for _, username := range []string{"alice", "nosuchuser"} {
		got, err := cronList(t, username)
		if err != nil || len(got) != 0 {
			t.Errorf("%s: cron.list = %v, %v; want an empty list", username, got, err)
		}
	}
}

func TestCronList_RefusesASymlinkedConfig(t *testing.T) {
	env := newCronTestEnv(t)
	outside := t.TempDir()
	plant(t, filepath.Join(outside, "systemd", "user"), cronTestJob, false)
	if err := os.Symlink(outside, filepath.Join(env.home, ".config")); err != nil {
		t.Fatal(err)
	}

	if got, err := cronList(t, "alice"); err == nil {
		t.Errorf("listing through a symlinked .config was not refused: %v", got)
	}
}
