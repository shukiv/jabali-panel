package stalwartadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailthrottle"
)

// fakeStalwart holds MtaOutboundThrottle objects and answers the JMAP calls
// Throttles makes the way Stalwart did on the .60 test box, including how a
// missing id fails.
type fakeStalwart struct {
	t       *testing.T
	jmap    *jmapFake
	objects map[string]map[string]any // id -> object as x:…/get returns it
	nextID  int
	reloads int
}

func newFakeThrottles(t *testing.T) (Throttles, *fakeStalwart) {
	t.Helper()
	f := &fakeStalwart{t: t, objects: map[string]map[string]any{}}
	c, j := newTestClient(t, f.answer)
	f.jmap = j
	return Throttles{Client: c}, f
}

// down points the client at a closed port: Stalwart is unreachable.
func (f *fakeStalwart) down(th Throttles) {
	srv := httptest.NewServer(nil)
	srv.Close()
	th.Client.URL = srv.URL
}

func (f *fakeStalwart) answer(method string, raw json.RawMessage) (int, string, any) {
	var args struct {
		IDs        []string                  `json:"ids"`
		Properties []string                  `json:"properties"`
		Create     map[string]map[string]any `json:"create"`
		Update     map[string]map[string]any `json:"update"`
		Destroy    []string                  `json:"destroy"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		f.t.Errorf("%s args: %v", method, err)
	}
	switch method {
	case "x:Action/set":
		f.reloads++
		return 200, method, map[string]any{"created": map[string]any{"reload": map[string]any{"id": fmt.Sprint("r", f.reloads)}}}
	case "x:MtaOutboundThrottle/query":
		ids := []string{}
		for id := range f.objects {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return 200, method, map[string]any{"ids": ids}
	case "x:MtaOutboundThrottle/get":
		list, notFound := []any{}, []string{}
		for _, id := range args.IDs {
			obj, ok := f.objects[id]
			if !ok {
				notFound = append(notFound, id)
				continue
			}
			out := map[string]any{"id": id}
			for k, v := range obj {
				if args.Properties == nil || slices.Contains(args.Properties, k) {
					out[k] = v
				}
			}
			list = append(list, out)
		}
		return 200, method, map[string]any{"list": list, "notFound": notFound}
	case "x:MtaOutboundThrottle/set":
		res := map[string]any{}
		for key, obj := range args.Create {
			f.nextID++
			id := fmt.Sprintf("new%d", f.nextID)
			f.objects[id] = obj
			res["created"] = map[string]any{key: map[string]any{"id": id}}
		}
		for id, obj := range args.Update {
			if _, ok := f.objects[id]; !ok {
				res["notUpdated"] = map[string]any{id: map[string]any{"type": "notFound"}}
				continue
			}
			f.objects[id] = obj
			res["updated"] = map[string]any{id: nil}
		}
		for _, id := range args.Destroy {
			if _, ok := f.objects[id]; !ok {
				res["notDestroyed"] = map[string]any{id: map[string]any{"type": "notFound"}}
				continue
			}
			delete(f.objects, id)
			res["destroyed"] = []string{id}
		}
		return 200, method, res
	}
	f.t.Errorf("unexpected JMAP call %s %s", method, raw)
	return 200, "error", map[string]any{"type": "unknownMethod"}
}

func (f *fakeStalwart) methods() []string {
	var m []string
	for _, r := range f.jmap.requests() {
		m = append(m, r.method)
	}
	return m
}

func (f *fakeStalwart) stored(id string) mailthrottle.Throttle {
	f.t.Helper()
	b, _ := json.Marshal(f.objects[id])
	var th mailthrottle.Throttle
	if err := json.Unmarshal(b, &th); err != nil {
		f.t.Fatalf("object %s: %v", id, err)
	}
	return th
}

var userHourly = mailthrottle.ApplyRequest{Scope: mailthrottle.ScopeUser, ScopeRef: "alice@example.com", Window: mailthrottle.WindowHour, Limit: 50}

func TestThrottlesApply_CreatesAndReloads(t *testing.T) {
	th, f := newFakeThrottles(t)
	res, err := th.Apply(context.Background(), userHourly)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.StalwartID != "new1" || !res.Changed {
		t.Fatalf("result = %+v, want new1 changed", res)
	}
	if !f.stored("new1").Equal(mailthrottle.Payload(userHourly)) {
		t.Fatalf("Stalwart holds %v, want the payload for %+v", f.objects["new1"], userHourly)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}
}

func TestThrottlesApply_UnchangedObjectIsLeftAlone(t *testing.T) {
	th, f := newFakeThrottles(t)
	first, err := th.Apply(context.Background(), userHourly)
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.methods())

	req := userHourly
	req.StalwartID = first.StalwartID
	res, err := th.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Changed || res.StalwartID != first.StalwartID {
		t.Fatalf("result = %+v, want unchanged %s", res, first.StalwartID)
	}
	if got := f.methods()[before:]; len(got) != 1 || got[0] != "x:MtaOutboundThrottle/get" {
		t.Fatalf("calls = %v, want only the get", got)
	}
}

func TestThrottlesApply_ChangedLimitUpdatesInPlace(t *testing.T) {
	th, f := newFakeThrottles(t)
	first, err := th.Apply(context.Background(), userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.reloads = 0

	req := userHourly
	req.StalwartID, req.Limit = first.StalwartID, 70
	res, err := th.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Changed || res.StalwartID != first.StalwartID {
		t.Fatalf("result = %+v, want changed %s", res, first.StalwartID)
	}
	if got := f.stored(first.StalwartID).Rate.Count; got != 70 {
		t.Fatalf("Stalwart count = %d, want 70", got)
	}
	if len(f.objects) != 1 {
		t.Fatalf("Stalwart holds %d throttles, want the one updated in place", len(f.objects))
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}
}

func TestThrottlesApply_ReplacesAnObjectStalwartLost(t *testing.T) {
	th, f := newFakeThrottles(t)
	req := userHourly
	req.StalwartID = "gone123"
	res, err := th.Apply(context.Background(), req)
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

func TestThrottlesApply_RejectsBadInputBeforeCallingStalwart(t *testing.T) {
	th, f := newFakeThrottles(t)
	bad := []mailthrottle.ApplyRequest{
		{Scope: mailthrottle.ScopeUser, ScopeRef: "a@example.com' || true || '", Window: mailthrottle.WindowHour, Limit: 1},
		{Scope: mailthrottle.ScopeDomain, ScopeRef: "example.com'", Window: mailthrottle.WindowHour, Limit: 1},
		{Scope: mailthrottle.ScopeGlobal, Window: mailthrottle.WindowHour, Limit: 0},
		{Scope: mailthrottle.ScopeGlobal, Window: mailthrottle.WindowHour, Limit: 1, StalwartID: "--password=x"},
	}
	for _, req := range bad {
		if _, err := th.Apply(context.Background(), req); err == nil {
			t.Errorf("%+v accepted", req)
		}
	}
	if got := f.methods(); len(got) != 0 {
		t.Fatalf("Stalwart called for rejected input: %v", got)
	}
}

// An unreachable Stalwart must not look like a lost object, or every tick
// would create another throttle.
func TestThrottlesApply_StalwartDownIsAnError(t *testing.T) {
	th, f := newFakeThrottles(t)
	f.down(th)
	req := userHourly
	req.StalwartID = "abc"
	if _, err := th.Apply(context.Background(), req); err == nil {
		t.Fatal("apply against an unreachable Stalwart reported success")
	}
}

// A 401 (wrong or rotated-away token) must not look like a lost object
// either.
func TestThrottlesApply_RejectedTokenIsAnError(t *testing.T) {
	th, f := newFakeThrottles(t)
	f.jmap.answer = func(string, json.RawMessage) (int, string, any) { return 401, "", nil }
	req := userHourly
	req.StalwartID = "abc"
	if _, err := th.Apply(context.Background(), req); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the 401 (not a silent create)", err)
	}
	if got := f.methods(); len(got) != 1 {
		t.Fatalf("calls = %v, want the get only", got)
	}
}

func TestThrottlesDelete(t *testing.T) {
	th, f := newFakeThrottles(t)
	first, err := th.Apply(context.Background(), userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.reloads = 0

	if err := th.Delete(context.Background(), first.StalwartID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := f.objects[first.StalwartID]; ok {
		t.Fatal("object still in Stalwart")
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d, want 1", f.reloads)
	}

	// Already gone: done, not an error, so the panel can clear its id.
	if err := th.Delete(context.Background(), first.StalwartID); err != nil {
		t.Fatalf("second delete: %v, want nil", err)
	}
	if f.reloads != 1 {
		t.Fatalf("reloads = %d after a no-op delete, want still 1", f.reloads)
	}

	before := len(f.methods())
	if err := th.Delete(context.Background(), "-rf"); err == nil {
		t.Fatal("delete accepted an id that reads as a flag")
	}
	if got := f.methods()[before:]; len(got) != 0 {
		t.Fatalf("Stalwart called for a bad id: %v", got)
	}
}

func TestThrottlesDelete_StalwartDownIsAnError(t *testing.T) {
	th, f := newFakeThrottles(t)
	f.down(th)
	if err := th.Delete(context.Background(), "abc"); err == nil {
		t.Fatal("delete against an unreachable Stalwart reported success")
	}
}

func TestThrottlesList(t *testing.T) {
	th, f := newFakeThrottles(t)
	list := func() []mailthrottle.ListItem {
		t.Helper()
		items, err := th.List(context.Background())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return items
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("empty Stalwart listed %v", got)
	}
	first, err := th.Apply(context.Background(), userHourly)
	if err != nil {
		t.Fatal(err)
	}
	f.objects["op1"] = map[string]any{"description": "operator cap", "enable": true}
	// An id that could not be passed back to Delete is dropped.
	f.objects["--ids=all"] = map[string]any{"description": "jabali y"}
	got := list()
	if len(got) != 2 {
		t.Fatalf("listed %v, want 2", got)
	}
	byID := map[string]string{}
	for _, it := range got {
		byID[it.StalwartID] = it.Description
	}
	if byID[first.StalwartID] != mailthrottle.Description(userHourly) || byID["op1"] != "operator cap" {
		t.Fatalf("listed %v", byID)
	}
	reqs := f.jmap.requests()
	last := reqs[len(reqs)-1]
	if last.method != "x:MtaOutboundThrottle/get" || !strings.Contains(string(last.args), `"properties":["description"]`) {
		t.Fatalf("list get = %s %s", last.method, last.args)
	}
}

func TestThrottlesList_StalwartDownIsAnError(t *testing.T) {
	th, f := newFakeThrottles(t)
	f.down(th)
	// The sweep must not read an unreachable Stalwart as "no throttles".
	if items, err := th.List(context.Background()); err == nil {
		t.Fatalf("list = %v, nil; want an error", items)
	}
}
