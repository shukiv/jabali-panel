package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
)

// fakeStalwartCLI stands in for stalwart-cli. It answers the calls the
// throttle verbs make with the output stalwart-cli 1.0.12 printed on the .60
// test box, including how a missing id fails.
type fakeStalwartCLI struct {
	t       *testing.T
	objects map[string]string // id -> object JSON as `get --json` prints it
	nextID  int
	calls   [][]string
	reloads int
	down    bool // every call fails the way an unreachable Stalwart does
}

const fakeStalwartToken = "s3cret-token"

func useFakeStalwartCLI(t *testing.T) *fakeStalwartCLI {
	t.Helper()
	f := &fakeStalwartCLI{t: t, objects: map[string]string{}}
	prevExec, prevTok, prevReload := execCommandContext, stalwartAdminTokenFunc, mailThrottleReload
	stalwartAdminTokenFunc = func() (string, error) { return fakeStalwartToken, nil }
	mailThrottleReload = func(context.Context) error {
		f.reloads++
		return nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "stalwart-cli" {
			t.Fatalf("unexpected command %q", name)
		}
		f.calls = append(f.calls, args)
		out, errOut, rc := f.answer(args)
		dir := t.TempDir()
		outFile, errFile := filepath.Join(dir, "out"), filepath.Join(dir, "err")
		if err := os.WriteFile(outFile, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(errFile, []byte(errOut), 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("cat %s; cat %s >&2; exit %d", outFile, errFile, rc))
	}
	t.Cleanup(func() { execCommandContext, stalwartAdminTokenFunc, mailThrottleReload = prevExec, prevTok, prevReload })
	return f
}

func (f *fakeStalwartCLI) answer(args []string) (stdout, stderr string, rc int) {
	if f.down {
		return "", "error: error sending request for url (http://127.0.0.1:8446/jmap/session): connection refused", 1
	}
	flag := func(name string) string {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	switch {
	case args[0] == "create" && args[1] == mailthrottle.StalwartType:
		f.nextID++
		id := fmt.Sprintf("new%d", f.nextID)
		f.objects[id] = withID(f.t, flag("--json"), id)
		return "Created MtaOutboundThrottle " + id + "\n", "", 0
	case args[0] == "get":
		id := args[2]
		obj, ok := f.objects[id]
		if !ok {
			return "error: MtaOutboundThrottle " + id + " not found\n", "", 1
		}
		return obj + "\n", "", 0
	case args[0] == "update":
		id := args[2]
		if _, ok := f.objects[id]; !ok {
			return "", "error: notFound\nerror: update failed\n", 1
		}
		f.objects[id] = withID(f.t, flag("--json"), id)
		return "Updated MtaOutboundThrottle " + id + "\n", "", 0
	case args[0] == "delete":
		id := flag("--ids")
		if _, ok := f.objects[id]; !ok {
			return id + " failed: error: notFound\n0 deleted, 1 failed\n", "error: one or more deletions failed\n", 1
		}
		delete(f.objects, id)
		return id + " deleted\n1 deleted, 0 failed\n", "", 0
	}
	f.t.Fatalf("unexpected stalwart-cli call %v", args)
	return "", "", 1
}

// withID adds the id Stalwart reports on get, as Stalwart does.
func withID(t *testing.T, body, id string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("stalwart-cli got a non-JSON payload %q: %v", body, err)
	}
	m["id"] = id
	b, _ := json.Marshal(m)
	return string(b)
}

func (f *fakeStalwartCLI) verbs() []string {
	var v []string
	for _, c := range f.calls {
		v = append(v, c[0]+" "+c[1])
	}
	return v
}

func applyThrottle(t *testing.T, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error) {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := mailThrottleApplyHandler(context.Background(), raw)
	if err != nil {
		return mailthrottle.ApplyResult{}, err
	}
	return out.(mailthrottle.ApplyResult), nil
}

func storedThrottle(t *testing.T, f *fakeStalwartCLI, id string) mailthrottle.Throttle {
	t.Helper()
	var th mailthrottle.Throttle
	if err := json.Unmarshal([]byte(f.objects[id]), &th); err != nil {
		t.Fatalf("object %s: %v", id, err)
	}
	return th
}

var userHourly = mailthrottle.ApplyRequest{Scope: mailthrottle.ScopeUser, ScopeRef: "alice@example.com", Window: mailthrottle.WindowHour, Limit: 50}

func TestMailThrottleApply_CreatesAndReloads(t *testing.T) {
	f := useFakeStalwartCLI(t)
	res, err := applyThrottle(t, userHourly)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.StalwartID != "new1" || !res.Changed {
		t.Fatalf("result = %+v, want new1 changed", res)
	}
	if !storedThrottle(t, f, "new1").Equal(mailthrottle.Payload(userHourly)) {
		t.Fatalf("Stalwart holds %s, want the payload for %+v", f.objects["new1"], userHourly)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}
}

func TestMailThrottleApply_UnchangedObjectIsLeftAlone(t *testing.T) {
	f := useFakeStalwartCLI(t)
	first, err := applyThrottle(t, userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.calls, f.reloads = nil, 0

	req := userHourly
	req.StalwartID = first.StalwartID
	res, err := applyThrottle(t, req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Changed || res.StalwartID != first.StalwartID {
		t.Fatalf("result = %+v, want unchanged %s", res, first.StalwartID)
	}
	if got := f.verbs(); len(got) != 1 || got[0] != "get "+mailthrottle.StalwartType {
		t.Fatalf("calls = %v, want only the get", got)
	}
}

func TestMailThrottleApply_ChangedLimitUpdatesInPlace(t *testing.T) {
	f := useFakeStalwartCLI(t)
	first, err := applyThrottle(t, userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.reloads = 0

	req := userHourly
	req.StalwartID, req.Limit = first.StalwartID, 70
	res, err := applyThrottle(t, req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Changed || res.StalwartID != first.StalwartID {
		t.Fatalf("result = %+v, want changed %s", res, first.StalwartID)
	}
	if got := storedThrottle(t, f, first.StalwartID).Rate.Count; got != 70 {
		t.Fatalf("Stalwart count = %d, want 70", got)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}
}

func TestMailThrottleApply_ReplacesAnObjectStalwartLost(t *testing.T) {
	f := useFakeStalwartCLI(t)
	req := userHourly
	req.StalwartID = "gone123"
	res, err := applyThrottle(t, req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.StalwartID != "new1" || !res.Changed {
		t.Fatalf("result = %+v, want a new object", res)
	}
	if _, ok := f.objects["new1"]; !ok {
		t.Fatal("no object created")
	}
}

func TestMailThrottleApply_RejectsBadInputBeforeCallingStalwart(t *testing.T) {
	f := useFakeStalwartCLI(t)
	bad := []mailthrottle.ApplyRequest{
		{Scope: mailthrottle.ScopeUser, ScopeRef: "a@example.com' || true || '", Window: mailthrottle.WindowHour, Limit: 1},
		{Scope: mailthrottle.ScopeDomain, ScopeRef: "example.com'", Window: mailthrottle.WindowHour, Limit: 1},
		{Scope: mailthrottle.ScopeGlobal, Window: mailthrottle.WindowHour, Limit: 0},
		{Scope: mailthrottle.ScopeGlobal, Window: mailthrottle.WindowHour, Limit: 1, StalwartID: "--password=x"},
	}
	for _, req := range bad {
		_, err := applyThrottle(t, req)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", req, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("stalwart-cli ran for rejected input: %v", f.calls)
	}
}

func TestMailThrottleApply_StalwartDownIsAnError(t *testing.T) {
	f := useFakeStalwartCLI(t)
	f.down = true
	req := userHourly
	req.StalwartID = "abc"
	if _, err := applyThrottle(t, req); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the connection error (not a silent create)", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want the get only", f.verbs())
	}
}

func TestMailThrottleDelete(t *testing.T) {
	f := useFakeStalwartCLI(t)
	first, err := applyThrottle(t, userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.reloads = 0
	del := func(id string) (mailthrottle.DeleteResult, error) {
		raw, _ := json.Marshal(mailthrottle.DeleteRequest{StalwartID: id})
		out, err := mailThrottleDeleteHandler(context.Background(), raw)
		if err != nil {
			return mailthrottle.DeleteResult{}, err
		}
		return out.(mailthrottle.DeleteResult), nil
	}

	res, err := del(first.StalwartID)
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v; want deleted", res, err)
	}
	if _, ok := f.objects[first.StalwartID]; ok {
		t.Fatal("object still in Stalwart")
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}

	// Already gone: done, not an error, so the panel can clear its id.
	res, err = del(first.StalwartID)
	if err != nil || res.Deleted {
		t.Fatalf("second delete = %+v, %v; want not-deleted and no error", res, err)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d after a no-op delete, want still 1", f.reloads)
	}

	if _, err := del("-rf"); err == nil {
		t.Fatal("delete accepted an id that reads as a flag")
	}
}

func TestMailThrottle_TokenNeverInArgv(t *testing.T) {
	f := useFakeStalwartCLI(t)
	if _, err := applyThrottle(t, userHourly); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		for _, a := range c {
			if strings.Contains(a, fakeStalwartToken) {
				t.Fatalf("token in argv: %v", c)
			}
		}
	}
}
