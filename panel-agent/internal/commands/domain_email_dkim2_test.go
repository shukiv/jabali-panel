package commands

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// fakeDkim2Registry backs the JMAP routes with a mutable signature list so
// one test can walk the full enable→noop→disable→noop cycle.
type fakeDkim2Registry struct {
	sigs      map[string]map[string]any // id -> object
	nextID    int
	lastCreIt map[string]any // last create payload, for shape asserts
}

func newDkim2TestServer(t *testing.T, reg *fakeDkim2Registry) {
	t.Helper()
	server := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/query": func(json.RawMessage) (any, *jmapFakeError) {
			return map[string]any{"ids": []string{"dom1"}, "total": 1}, nil
		},
		"x:DkimSignature/get": func(json.RawMessage) (any, *jmapFakeError) {
			list := make([]map[string]any, 0, len(reg.sigs))
			for _, s := range reg.sigs {
				list = append(list, s)
			}
			return map[string]any{"list": list}, nil
		},
		"x:DkimSignature/set": func(raw json.RawMessage) (any, *jmapFakeError) {
			var req struct {
				Create  map[string]map[string]any `json:"create"`
				Destroy []string                  `json:"destroy"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, &jmapFakeError{Type: "invalidArguments"}
			}
			created := map[string]any{}
			for tempID, obj := range req.Create {
				reg.nextID++
				id := fmt.Sprintf("sig%d", reg.nextID)
				obj["id"] = id
				reg.sigs[id] = obj
				reg.lastCreIt = obj
				created[tempID] = map[string]any{"id": id}
			}
			var destroyed []string
			for _, id := range req.Destroy {
				if _, ok := reg.sigs[id]; ok {
					delete(reg.sigs, id)
					destroyed = append(destroyed, id)
				}
			}
			return map[string]any{"created": created, "destroyed": destroyed}, nil
		},
	})
	wireJMAP(t, server)
}

func writeTestDKIMKey(t *testing.T, domain string) {
	t.Helper()
	dir := t.TempDir()
	orig := dkimKeyDirFunc
	dkimKeyDirFunc = func() string { return dir }
	t.Cleanup(func() { dkimKeyDirFunc = orig })
	kp, err := dkim.GenerateEd25519()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := dkim.WritePrivate(dir+"/"+domain+".key", kp.PrivateRawBase64); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

func callDkim2Set(t *testing.T, domain string, enabled bool) (dkim2SetResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(dkim2SetParams{DomainName: domain, Enabled: enabled})
	out, err := dkim2SetHandler(context.Background(), raw)
	if err != nil {
		return dkim2SetResponse{}, err
	}
	return out.(dkim2SetResponse), nil
}

// GH #648: the full converge cycle — create once, steady-state noop,
// destroy once, steady-state noop — and the created object carries ONLY
// the fields the Dkim2 tagged-enum accepts (probe-verified on 0.16.15:
// canonicalization/report/headers are rejected with invalidPatch).
func TestDkim2Set_ConvergeCycleAndObjectShape(t *testing.T) {
	reg := &fakeDkim2Registry{sigs: map[string]map[string]any{
		// Pre-existing DKIM1 signature must be ignored by the dkim2 filter.
		"sigdkim1": {"id": "sigdkim1", "@type": "Dkim1Ed25519Sha256", "domainId": "dom1", "selector": "jabali"},
	}}
	newDkim2TestServer(t, reg)
	writeTestDKIMKey(t, "example.com")

	resp, err := callDkim2Set(t, "example.com", true)
	if err != nil || !resp.Changed {
		t.Fatalf("enable: err=%v changed=%v, want changed", err, resp.Changed)
	}
	obj := reg.lastCreIt
	if obj["@type"] != "Dkim2Ed25519Sha256" || obj["selector"] != "jabali" || obj["stage"] != "active" {
		t.Fatalf("created object wrong: %v", obj)
	}
	for _, forbidden := range []string{"canonicalization", "report", "headers"} {
		if _, ok := obj[forbidden]; ok {
			t.Fatalf("created object carries %q — the Dkim2 variant rejects it (invalidPatch)", forbidden)
		}
	}
	if _, ok := obj["privateKey"]; !ok {
		t.Fatal("created object missing privateKey")
	}

	// Steady state: signature exists — no change.
	resp, err = callDkim2Set(t, "example.com", true)
	if err != nil || resp.Changed {
		t.Fatalf("enable steady-state: err=%v changed=%v, want no change", err, resp.Changed)
	}
	if len(reg.sigs) != 2 {
		t.Fatalf("signature count = %d, want 2 (dkim1 + dkim2)", len(reg.sigs))
	}

	// Disable: dkim2 destroyed, dkim1 untouched.
	resp, err = callDkim2Set(t, "example.com", false)
	if err != nil || !resp.Changed {
		t.Fatalf("disable: err=%v changed=%v, want changed", err, resp.Changed)
	}
	if len(reg.sigs) != 1 {
		t.Fatalf("signature count = %d after disable, want the dkim1 survivor", len(reg.sigs))
	}
	if _, ok := reg.sigs["sigdkim1"]; !ok {
		t.Fatal("disable destroyed the DKIM1 signature — it must only touch Dkim2 objects")
	}

	// Steady state off: no change.
	resp, err = callDkim2Set(t, "example.com", false)
	if err != nil || resp.Changed {
		t.Fatalf("disable steady-state: err=%v changed=%v, want no change", err, resp.Changed)
	}
}

// A domain Stalwart doesn't know: disable is a friendly noop, enable is a
// hard not_found (the reconciler shouldn't silently think it converged).
func TestDkim2Set_UnknownDomain(t *testing.T) {
	server := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/query": func(json.RawMessage) (any, *jmapFakeError) {
			return map[string]any{"ids": []string{}, "total": 0}, nil
		},
	})
	wireJMAP(t, server)

	resp, err := callDkim2Set(t, "ghost.example", false)
	if err != nil || resp.Changed {
		t.Fatalf("disable unknown: err=%v changed=%v, want clean noop", err, resp.Changed)
	}

	_, err = callDkim2Set(t, "ghost.example", true)
	if err == nil {
		t.Fatal("enable unknown: want not_found error")
	}
	var agentErr *agentwire.AgentError
	if !errors.As(err, &agentErr) || agentErr.Code != agentwire.CodeNotFound {
		t.Fatalf("enable unknown: err=%v, want code not_found", err)
	}
}
