package commands

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// GH #1795: a forwarder added to a mailbox whose Stalwart Principal has not
// materialized yet (never authed, never received) must NOT be silently dropped.
// forwarder.apply now mirrors autoresponder.set: when the account query comes
// back empty it provisions the Principal (x:Account/set) and retries, instead of
// returning CodeNotFound — which the panel swallows best-effort with no periodic
// backstop, so the redirect Sieve would otherwise never be applied.
//
// This test FAILS on the pre-fix handler: the first empty x:Account/query makes
// it return CodeNotFound, so ensureCalled/sieveCalled stay false and the handler
// errors.
func TestForwarderApply_EnsuresRegistryWhenAccountMissing(t *testing.T) {
	var mu sync.Mutex
	accountQueries := 0
	ensureCalled := false
	sieveCalled := false

	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/query": jmapHandlerReturning(jmapQueryResult{IDs: []string{"dom-1"}, Total: 1}),
		"x:Account/query": func(_ json.RawMessage) (any, *jmapFakeError) {
			mu.Lock()
			defer mu.Unlock()
			accountQueries++
			// First lookup: not registered yet. After the ensure create, the
			// Principal exists.
			if accountQueries == 1 {
				return jmapQueryResult{IDs: nil, Total: 0}, nil
			}
			return jmapQueryResult{IDs: []string{"acct-1"}, Total: 1}, nil
		},
		"x:Account/set": func(_ json.RawMessage) (any, *jmapFakeError) {
			mu.Lock()
			ensureCalled = true
			mu.Unlock()
			return jmapSetResult{Created: map[string]json.RawMessage{"#a1": json.RawMessage(`{"id":"acct-1"}`)}}, nil
		},
		"x:SieveUserScript/get": jmapHandlerReturning(jmapGetResult{List: nil}),
		"x:SieveUserScript/set": func(_ json.RawMessage) (any, *jmapFakeError) {
			mu.Lock()
			sieveCalled = true
			mu.Unlock()
			return jmapSetResult{Created: map[string]json.RawMessage{"jabali-fwds": json.RawMessage(`{"id":"sieve-1"}`)}}, nil
		},
	})
	defer srv.Close()
	wireJMAP(t, srv)

	params := json.RawMessage(`{"mailbox_email":"notifications@example.com","aliases":[],"externals":[{"target":"a@outside.org","keep_copy":false}]}`)
	res, err := forwarderApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("forwarder.apply errored on an unregistered mailbox (pre-fix behavior): %v", err)
	}
	if r, ok := res.(forwarderApplyResponse); !ok || !r.Ok {
		t.Fatalf("expected forwarderApplyResponse{Ok:true}, got %#v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if !ensureCalled {
		t.Error("x:Account/set (accountEnsureInRegistry) was never called — the missing Principal was not provisioned")
	}
	if !sieveCalled {
		t.Error("x:SieveUserScript/set was never called — the redirect Sieve was not applied after ensure")
	}
}

func TestBuildExternalSieve(t *testing.T) {
	cases := []struct {
		name      string
		externals []forwarderExternal
		want      string
	}{
		{
			name:      "empty",
			externals: nil,
			want:      "",
		},
		{
			name:      "single keep-copy",
			externals: []forwarderExternal{{Target: "a@example.org", KeepCopy: true}},
			want:      "require [\"copy\"];\nredirect :copy \"a@example.org\";\n",
		},
		{
			name:      "single forward-only (no copy)",
			externals: []forwarderExternal{{Target: "a@example.org", KeepCopy: false}},
			// No "copy" extension required when nothing keeps a copy.
			want: "redirect \"a@example.org\";\n",
		},
		{
			name: "mixed",
			externals: []forwarderExternal{
				{Target: "keep@example.org", KeepCopy: true},
				{Target: "fwd@example.org", KeepCopy: false},
			},
			want: "require [\"copy\"];\nredirect :copy \"keep@example.org\";\nredirect \"fwd@example.org\";\n",
		},
		{
			name: "blank target skipped",
			externals: []forwarderExternal{
				{Target: "", KeepCopy: true},
				{Target: "ok@example.org", KeepCopy: false},
			},
			// hasCopy is true (the blank entry sets it) but the blank
			// redirect line is skipped.
			want: "require [\"copy\"];\nredirect \"ok@example.org\";\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildExternalSieve(tc.externals)
			if got != tc.want {
				t.Errorf("buildExternalSieve mismatch:\n want: %q\n got:  %q", tc.want, got)
			}
		})
	}
}
