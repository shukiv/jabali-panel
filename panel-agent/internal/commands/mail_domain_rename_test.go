package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeStalwart is a tiny in-memory registry the JMAP fake routes against, so the
// mail.domain.rename tests can drive every status branch and assert the exact
// mutations (rename in place, orphan destroy, catch-all rewrite).
type fakeStalwart struct {
	domains  map[string]string // name -> id
	catchall map[string]string // domainId -> catchAllAddress ("" = none)
	accounts map[string]string // accountId -> domainId

	renamed   map[string]string // domainId -> new name (recorded x:Domain/set update name)
	destroyed []string          // recorded x:Domain/set destroy ids
	catchSet  map[string]string // domainId -> new catchAllAddress (recorded)
}

func (f *fakeStalwart) idByName(name string) string { return f.domains[name] }

func (f *fakeStalwart) routes() map[string]jmapHandler {
	if f.renamed == nil {
		f.renamed = map[string]string{}
	}
	if f.catchSet == nil {
		f.catchSet = map[string]string{}
	}
	return map[string]jmapHandler{
		"x:Domain/query": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Filter struct {
					Name string `json:"name"`
				} `json:"filter"`
			}
			_ = json.Unmarshal(args, &a)
			if id := f.domains[a.Filter.Name]; id != "" {
				return map[string]any{"ids": []string{id}}, nil
			}
			return map[string]any{"ids": []string{}}, nil
		},
		"x:Account/query": func(json.RawMessage) (any, *jmapFakeError) {
			ids := make([]string, 0, len(f.accounts))
			for id := range f.accounts {
				ids = append(ids, id)
			}
			return map[string]any{"ids": ids}, nil
		},
		"x:Account/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &a)
			list := make([]map[string]any, 0, len(a.IDs))
			for _, id := range a.IDs {
				list = append(list, map[string]any{"id": id, "domainId": f.accounts[id]})
			}
			return map[string]any{"list": list}, nil
		},
		"x:Domain/get": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(args, &a)
			list := make([]map[string]any, 0, len(a.IDs))
			for _, id := range a.IDs {
				var ca any
				if v, ok := f.catchall[id]; ok && v != "" {
					ca = v
				}
				list = append(list, map[string]any{"id": id, "catchAllAddress": ca})
			}
			return map[string]any{"list": list}, nil
		},
		"x:Domain/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Update  map[string]map[string]any `json:"update"`
				Destroy []string                  `json:"destroy"`
			}
			_ = json.Unmarshal(args, &a)
			if len(a.Destroy) > 0 {
				f.destroyed = append(f.destroyed, a.Destroy...)
				return map[string]any{"destroyed": a.Destroy}, nil
			}
			updated := map[string]any{}
			for id, patch := range a.Update {
				if name, ok := patch["name"].(string); ok {
					f.renamed[id] = name
				}
				if ca, ok := patch["catchAllAddress"]; ok {
					if s, isStr := ca.(string); isStr {
						f.catchSet[id] = s
					} else {
						f.catchSet[id] = "" // cleared to null
					}
				}
				updated[id] = nil
			}
			return map[string]any{"updated": updated}, nil
		},
	}
}

func callRename(t *testing.T, f *fakeStalwart, old, new string, dry bool) mailDomainRenameResult {
	t.Helper()
	srv := newJMAPServer(t, f.routes())
	wireJMAP(t, srv)
	raw, _ := json.Marshal(mailDomainRenameParams{Old: old, New: new, DryRun: dry})
	out, err := mailDomainRenameHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	res, ok := out.(mailDomainRenameResult)
	if !ok {
		t.Fatalf("unexpected result type %T", out)
	}
	return res
}

func TestMailDomainRename_NotInRegistry(t *testing.T) {
	f := &fakeStalwart{domains: map[string]string{}}
	if res := callRename(t, f, "old.com", "new.com", true); res.Status != "not_in_registry" {
		t.Fatalf("status: got %q want not_in_registry", res.Status)
	}
}

func TestMailDomainRename_Already(t *testing.T) {
	// old gone, new present, new EMPTY -> idempotent-retry / harmless orphan; proceed.
	f := &fakeStalwart{domains: map[string]string{"new.com": "n1"}}
	if res := callRename(t, f, "old.com", "new.com", true); res.Status != "already" {
		t.Fatalf("status: got %q want already", res.Status)
	}
}

func TestMailDomainRename_AlreadyWithAccountsIsConflict(t *testing.T) {
	// old gone, new present AND new holds accounts -> another domain's mail.
	// Refuse (fail closed toward tenant isolation) rather than let the DB trigger
	// point this domain's retained mailboxes at an occupied name. Must hold even
	// in dry_run, so the panel refuses BEFORE it moves any files.
	f := &fakeStalwart{
		domains:  map[string]string{"new.com": "n1"},
		accounts: map[string]string{"a1": "n1"},
	}
	for _, dry := range []bool{true, false} {
		res := callRename(t, f, "old.com", "new.com", dry)
		if res.Status != "conflict" {
			t.Fatalf("dry=%v status: got %q want conflict", dry, res.Status)
		}
		if len(f.renamed) != 0 || len(f.destroyed) != 0 {
			t.Fatalf("dry=%v mutated: renamed=%v destroyed=%v", dry, f.renamed, f.destroyed)
		}
	}
}

func TestMailDomainRename_DryRunOK_NoMutation(t *testing.T) {
	f := &fakeStalwart{domains: map[string]string{"old.com": "o1"}}
	res := callRename(t, f, "old.com", "new.com", true)
	if res.Status != "ok" {
		t.Fatalf("status: got %q want ok", res.Status)
	}
	if len(f.renamed) != 0 || len(f.destroyed) != 0 {
		t.Fatalf("dry_run mutated: renamed=%v destroyed=%v", f.renamed, f.destroyed)
	}
}

func TestMailDomainRename_ConflictRefuses(t *testing.T) {
	// both present AND new has an account -> conflict, no mutation.
	f := &fakeStalwart{
		domains:  map[string]string{"old.com": "o1", "new.com": "n1"},
		accounts: map[string]string{"a1": "n1"},
	}
	res := callRename(t, f, "old.com", "new.com", false)
	if res.Status != "conflict" {
		t.Fatalf("status: got %q want conflict", res.Status)
	}
	if len(f.renamed) != 0 || len(f.destroyed) != 0 {
		t.Fatalf("conflict mutated: renamed=%v destroyed=%v", f.renamed, f.destroyed)
	}
}

func TestMailDomainRename_RenamesInPlace(t *testing.T) {
	f := &fakeStalwart{
		domains:  map[string]string{"old.com": "o1"},
		catchall: map[string]string{"o1": ""},
	}
	res := callRename(t, f, "old.com", "new.com", false)
	if res.Status != "renamed" {
		t.Fatalf("status: got %q want renamed", res.Status)
	}
	if f.renamed["o1"] != "new.com" {
		t.Fatalf("expected o1 renamed to new.com, got %v", f.renamed)
	}
	if res.CatchallRewritten {
		t.Fatalf("no catch-all set, should not report a rewrite")
	}
}

func TestMailDomainRename_DestroysEmptyOrphanThenRenames(t *testing.T) {
	// new is an empty orphan (0 accounts) -> destroy it, then rename old in place.
	f := &fakeStalwart{
		domains:  map[string]string{"old.com": "o1", "new.com": "n1"},
		accounts: map[string]string{"a1": "o1"}, // accounts only on OLD
	}
	res := callRename(t, f, "old.com", "new.com", false)
	if res.Status != "renamed" {
		t.Fatalf("status: got %q want renamed", res.Status)
	}
	if len(f.destroyed) != 1 || f.destroyed[0] != "n1" {
		t.Fatalf("expected empty orphan n1 destroyed, got %v", f.destroyed)
	}
	if f.renamed["o1"] != "new.com" {
		t.Fatalf("expected o1 renamed to new.com, got %v", f.renamed)
	}
}

func TestMailDomainRename_RewritesCatchAll(t *testing.T) {
	f := &fakeStalwart{
		domains:  map[string]string{"old.com": "o1"},
		catchall: map[string]string{"o1": "postmaster@old.com"},
	}
	res := callRename(t, f, "old.com", "new.com", false)
	if res.Status != "renamed" || !res.CatchallRewritten {
		t.Fatalf("got status=%q catchall=%v; want renamed + rewritten", res.Status, res.CatchallRewritten)
	}
	if f.catchSet["o1"] != "postmaster@new.com" {
		t.Fatalf("catch-all: got %q want postmaster@new.com", f.catchSet["o1"])
	}
}

func TestMailDomainRename_ExternalCatchAllLeftAlone(t *testing.T) {
	f := &fakeStalwart{
		domains:  map[string]string{"old.com": "o1"},
		catchall: map[string]string{"o1": "ops@external.example"},
	}
	res := callRename(t, f, "old.com", "new.com", false)
	if res.CatchallRewritten {
		t.Fatalf("external catch-all should not be rewritten")
	}
	if _, set := f.catchSet["o1"]; set {
		t.Fatalf("external catch-all should be left untouched, got %v", f.catchSet)
	}
}

func TestMailDomainRename_BadParams(t *testing.T) {
	for _, tc := range []mailDomainRenameParams{
		{Old: "", New: "new.com"},
		{Old: "old.com", New: ""},
		{Old: "same.com", New: "same.com"},
		{Old: "old.com", New: "-bad-"},
	} {
		raw, _ := json.Marshal(tc)
		if _, err := mailDomainRenameHandler(context.Background(), raw); err == nil {
			t.Fatalf("expected error for params %+v", tc)
		}
	}
}
