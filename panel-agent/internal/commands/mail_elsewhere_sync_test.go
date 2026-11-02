package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// The RCPT script the installer writes (converge_stalwart_mail_elsewhere).
const elsewhereRcptScript = `{"match":{"0":{"if":"is_empty(authenticated_as) && is_local_domain(rcpt_domain) && key_exists('jabali-mail-elsewhere', rcpt_domain)","then":"'jabali-mail-elsewhere'"}},"else":"false"}`

// Stalwart's shipped RCPT script: no refusal.
const defaultRcptScript = `{"match":{},"else":"false"}`

// fakeElsewhereStalwart is a stateful fake of the objects the verb touches.
type fakeElsewhereStalwart struct {
	rcptScript string
	domains    map[string]*mailElsewhereDomainRow // id → row
	keys       map[string]*mailElsewhereKeyRow    // id → row
	events     []string                           // "relay:<name>=<bool>", "key+:<k>", "key-:<k>", "reload"
	reloadFail bool
	nextID     int
}

func newFakeElsewhere(rcptScript string) *fakeElsewhereStalwart {
	return &fakeElsewhereStalwart{
		rcptScript: rcptScript,
		domains:    map[string]*mailElsewhereDomainRow{},
		keys:       map[string]*mailElsewhereKeyRow{},
	}
}

func (f *fakeElsewhereStalwart) addDomain(name string, relay bool) {
	f.nextID++
	id := fmt.Sprintf("d%d", f.nextID)
	f.domains[id] = &mailElsewhereDomainRow{ID: id, Name: name, AllowRelaying: relay}
}

func (f *fakeElsewhereStalwart) addKey(ns, key string) {
	f.nextID++
	id := fmt.Sprintf("k%d", f.nextID)
	f.keys[id] = &mailElsewhereKeyRow{ID: id, Namespace: ns, Key: key}
}

func (f *fakeElsewhereStalwart) relaying() map[string]bool {
	out := map[string]bool{}
	for _, d := range f.domains {
		out[d.Name] = d.AllowRelaying
	}
	return out
}

func (f *fakeElsewhereStalwart) keyNames(ns string) []string {
	var out []string
	for _, k := range f.keys {
		if k.Namespace == ns {
			out = append(out, k.Key)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeElsewhereStalwart) routes() map[string]jmapHandler {
	return map[string]jmapHandler{
		"x:MtaStageRcpt/get": func(json.RawMessage) (any, *jmapFakeError) {
			return map[string]any{"list": []any{map[string]any{"id": "singleton", "script": json.RawMessage(f.rcptScript)}}}, nil
		},
		"x:Domain/query": func(json.RawMessage) (any, *jmapFakeError) {
			ids := []string{}
			for id := range f.domains {
				ids = append(ids, id)
			}
			return map[string]any{"ids": ids, "total": len(ids)}, nil
		},
		"x:Domain/get": func(json.RawMessage) (any, *jmapFakeError) {
			list := []any{}
			for _, d := range f.domains {
				list = append(list, *d)
			}
			return map[string]any{"list": list}, nil
		},
		"x:Domain/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var p struct {
				Update map[string]map[string]bool `json:"update"`
			}
			_ = json.Unmarshal(args, &p)
			updated := map[string]any{}
			for id, patch := range p.Update {
				f.domains[id].AllowRelaying = patch["allowRelaying"]
				f.events = append(f.events, fmt.Sprintf("relay:%s=%v", f.domains[id].Name, patch["allowRelaying"]))
				updated[id] = nil
			}
			return map[string]any{"updated": updated}, nil
		},
		"x:MemoryLookupKey/query": func(args json.RawMessage) (any, *jmapFakeError) {
			// Stalwart's namespace filter is a text match: return every key
			// whose namespace contains the filter, like the real server may.
			var p struct {
				Filter struct {
					Namespace string `json:"namespace"`
				} `json:"filter"`
			}
			_ = json.Unmarshal(args, &p)
			ids := []string{}
			for id, k := range f.keys {
				if strings.Contains(k.Namespace, p.Filter.Namespace) {
					ids = append(ids, id)
				}
			}
			return map[string]any{"ids": ids, "total": len(ids)}, nil
		},
		"x:MemoryLookupKey/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var p struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &p)
			list := []any{}
			for _, id := range p.IDs {
				list = append(list, *f.keys[id])
			}
			return map[string]any{"list": list}, nil
		},
		"x:MemoryLookupKey/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var p struct {
				Create  map[string]mailElsewhereKeyRow `json:"create"`
				Destroy []string                       `json:"destroy"`
			}
			_ = json.Unmarshal(args, &p)
			created := map[string]any{}
			for cid, k := range p.Create {
				f.addKey(k.Namespace, k.Key)
				f.events = append(f.events, "key+:"+k.Key)
				created[cid] = map[string]any{"id": "x"}
			}
			for _, id := range p.Destroy {
				f.events = append(f.events, "key-:"+f.keys[id].Key)
				delete(f.keys, id)
			}
			return map[string]any{"created": created, "destroyed": p.Destroy}, nil
		},
		"x:Action/set": func(args json.RawMessage) (any, *jmapFakeError) {
			if !strings.Contains(string(args), `"ReloadLookupStores"`) {
				return nil, &jmapFakeError{Type: "invalidArguments", Description: "unexpected action " + string(args)}
			}
			if f.reloadFail {
				return nil, &jmapFakeError{Type: "serverFail", Description: "reload failed"}
			}
			f.events = append(f.events, "reload")
			return map[string]any{"created": map[string]any{"reload": map[string]any{"id": "a"}}}, nil
		},
	}
}

func runElsewhereSync(t *testing.T, f *fakeElsewhereStalwart, domains ...string) (mailElsewhereSyncResponse, error) {
	t.Helper()
	wireJMAP(t, newJMAPServer(t, f.routes()))
	if domains == nil {
		domains = []string{}
	}
	params, _ := json.Marshal(map[string]any{"domains": domains})
	out, err := mailElsewhereSyncHandler(context.Background(), params)
	if err != nil {
		return mailElsewhereSyncResponse{}, err
	}
	return out.(mailElsewhereSyncResponse), nil
}

func eventIndex(events []string, e string) int {
	for i, x := range events {
		if x == e {
			return i
		}
	}
	return -1
}

func resetElsewhereReload(t *testing.T) {
	t.Helper()
	mailElsewhereReloadPending = false
	t.Cleanup(func() { mailElsewhereReloadPending = false })
}

// A listed domain gets its lookup key, the keys are reloaded, and only then
// may it accept relaying; a hosted domain is left alone.
func TestMailElsewhereSync_ListsDomainAndAllowsRelayAfterReload(t *testing.T) {
	resetElsewhereReload(t)
	f := newFakeElsewhere(elsewhereRcptScript)
	f.addDomain("moved.example", false)
	f.addDomain("hosted.example", false)

	resp, err := runElsewhereSync(t, f, "Moved.Example", "notinstalwart.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.keyNames(mailElsewhereNamespace); strings.Join(got, ",") != "moved.example,notinstalwart.example" {
		t.Errorf("keys = %v", got)
	}
	rel := f.relaying()
	if !rel["moved.example"] || rel["hosted.example"] {
		t.Errorf("allowRelaying = %v, want only moved.example", rel)
	}
	reload, relay := eventIndex(f.events, "reload"), eventIndex(f.events, "relay:moved.example=true")
	if reload < 0 || relay < 0 || relay < reload {
		t.Errorf("relaying must follow the reload that makes the key live: events %v", f.events)
	}
	if !resp.Routing || resp.KeysAdded != 2 || resp.RelayOn != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

// Steady state writes nothing and does not reload.
func TestMailElsewhereSync_SteadyStateNoWrites(t *testing.T) {
	resetElsewhereReload(t)
	f := newFakeElsewhere(elsewhereRcptScript)
	f.addDomain("moved.example", true)
	f.addKey(mailElsewhereNamespace, "moved.example")

	if _, err := runElsewhereSync(t, f, "moved.example"); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 0 {
		t.Errorf("steady state wrote: %v", f.events)
	}
}

// A domain whose mail came back: relaying goes off BEFORE its key is removed,
// and another lookup's keys are never touched.
func TestMailElsewhereSync_DomainBackHomeRelayOffFirst(t *testing.T) {
	resetElsewhereReload(t)
	f := newFakeElsewhere(elsewhereRcptScript)
	f.addDomain("back.example", true)
	f.addKey(mailElsewhereNamespace, "back.example")
	f.addKey(mailElsewhereNamespace+"-other", "keep.example")
	f.addKey("trusted-domains", "gmail.com")

	if _, err := runElsewhereSync(t, f); err != nil {
		t.Fatal(err)
	}
	if f.relaying()["back.example"] {
		t.Error("back.example still relays")
	}
	if got := f.keyNames(mailElsewhereNamespace); len(got) != 0 {
		t.Errorf("keys left = %v", got)
	}
	if got := f.keyNames(mailElsewhereNamespace + "-other"); len(got) != 1 {
		t.Errorf("a key of another lookup was removed: %v", got)
	}
	if got := f.keyNames("trusted-domains"); len(got) != 1 {
		t.Errorf("a spam-filter key was removed: %v", got)
	}
	off, del := eventIndex(f.events, "relay:back.example=false"), eventIndex(f.events, "key-:back.example")
	if off < 0 || del < 0 || off > del {
		t.Errorf("relaying must go off before the key goes: events %v", f.events)
	}
}

// Without the installer's RCPT refusal, allowRelaying would accept
// unauthenticated mail for the domain: never turn it on, and turn off any
// left on (allowRelaying is owned here for every domain).
func TestMailElsewhereSync_NoRcptRefusalNoRelaying(t *testing.T) {
	resetElsewhereReload(t)
	f := newFakeElsewhere(defaultRcptScript)
	f.addDomain("moved.example", false)
	f.addDomain("stale.example", true)

	resp, err := runElsewhereSync(t, f, "moved.example")
	if err != nil {
		t.Fatal(err)
	}
	rel := f.relaying()
	if rel["moved.example"] || rel["stale.example"] {
		t.Errorf("allowRelaying = %v, want none without the RCPT refusal", rel)
	}
	if resp.Routing {
		t.Error("routing reported configured on the shipped RCPT script")
	}
	// The key is still kept, so routing works as soon as the installer runs.
	if got := f.keyNames(mailElsewhereNamespace); len(got) != 1 {
		t.Errorf("keys = %v", got)
	}
}

// A failed reload leaves relaying off and makes the next call reload even
// though the keys already match.
func TestMailElsewhereSync_FailedReloadRetried(t *testing.T) {
	resetElsewhereReload(t)
	f := newFakeElsewhere(elsewhereRcptScript)
	f.addDomain("moved.example", false)
	f.reloadFail = true

	if _, err := runElsewhereSync(t, f, "moved.example"); err == nil {
		t.Fatal("want an error when the reload fails")
	}
	if f.relaying()["moved.example"] {
		t.Fatal("relaying turned on although the key was never made live")
	}
	f.reloadFail = false
	f.events = nil
	if _, err := runElsewhereSync(t, f, "moved.example"); err != nil {
		t.Fatal(err)
	}
	if eventIndex(f.events, "reload") < 0 || !f.relaying()["moved.example"] {
		t.Errorf("second call must reload then relay: events %v", f.events)
	}
}

func TestMailElsewhereSync_RejectsBadInput(t *testing.T) {
	for _, params := range []string{``, `{"domains":["bad domain"]}`, `{"domains":["nodot"]}`, `{"domains":["a.example\n"]}`, `{"domains":["x'); drop"]}`} {
		_, err := mailElsewhereSyncHandler(context.Background(), json.RawMessage(params))
		requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	}
}

func TestMailElsewhereSync_StalwartUnavailableSkips(t *testing.T) {
	resetElsewhereReload(t)
	origURL, origTok, origClient := stalwartAdminURLFunc, stalwartAdminTokenFunc, stalwartHTTPClientFunc
	stalwartAdminURLFunc = func() string { return "http://127.0.0.1:1" }
	stalwartAdminTokenFunc = func() (string, error) { return "t", nil }
	t.Cleanup(func() {
		stalwartAdminURLFunc, stalwartAdminTokenFunc, stalwartHTTPClientFunc = origURL, origTok, origClient
	})
	out, err := mailElsewhereSyncHandler(context.Background(), json.RawMessage(`{"domains":["moved.example"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !out.(mailElsewhereSyncResponse).Skipped {
		t.Errorf("want skipped, got %+v", out)
	}
}
