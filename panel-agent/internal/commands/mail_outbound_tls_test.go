package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// strategyRow is the minimal MtaTlsStrategy shape the fake server emits.
func strategyGetRoute(rows []map[string]any) jmapHandler {
	return func(json.RawMessage) (any, *jmapFakeError) {
		return map[string]any{"list": rows, "notFound": []string{}}, nil
	}
}

func strategyQueryRoute(ids []string) jmapHandler {
	return func(json.RawMessage) (any, *jmapFakeError) {
		return map[string]any{"ids": ids, "total": len(ids)}, nil
	}
}

// captureSetRoute records the /set update payload and returns success for
// every id it was asked to update.
func captureSetRoute(captured *map[string]map[string]any) jmapHandler {
	return func(args json.RawMessage) (any, *jmapFakeError) {
		var p struct {
			Update map[string]map[string]any `json:"update"`
		}
		_ = json.Unmarshal(args, &p)
		*captured = p.Update
		updated := map[string]any{}
		for id := range p.Update {
			updated[id] = nil
		}
		return map[string]any{"updated": updated}, nil
	}
}

func callEnsure(t *testing.T) mailOutboundTLSResponse {
	t.Helper()
	out, err := mailOutboundTLSEnsureHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var resp mailOutboundTLSResponse
	if e := json.Unmarshal(b, &resp); e != nil {
		t.Fatalf("decode resp: %v", e)
	}
	return resp
}

func TestOutboundTLS_PatchesBothStrategies(t *testing.T) {
	var setPayload map[string]map[string]any
	var reloaded bool
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:MtaTlsStrategy/query": strategyQueryRoute([]string{"idDef", "idInv"}),
		"x:MtaTlsStrategy/get": strategyGetRoute([]map[string]any{
			{"id": "idDef", "name": "default", "dane": "optional"},
			{"id": "idInv", "name": "invalid-tls", "dane": "optional"},
		}),
		"x:MtaTlsStrategy/set": captureSetRoute(&setPayload),
		"x:Action/set": func(json.RawMessage) (any, *jmapFakeError) {
			reloaded = true
			return map[string]any{"created": map[string]any{"reload": nil}}, nil
		},
	})
	wireJMAP(t, srv)

	resp := callEnsure(t)
	if len(resp.Changed) != 2 {
		t.Fatalf("expected 2 changed, got %v", resp.Changed)
	}
	for _, id := range []string{"idDef", "idInv"} {
		v, ok := setPayload[id]
		if !ok {
			t.Fatalf("id %s not in set payload %v", id, setPayload)
		}
		if v["dane"] != "disable" {
			t.Fatalf("id %s dane=%v, want disable", id, v["dane"])
		}
		if len(v) != 1 {
			t.Fatalf("id %s patch touched more than dane: %v", id, v)
		}
	}
	if !reloaded {
		t.Fatalf("expected a settings reload after the patch")
	}
}

func TestOutboundTLS_NoDriftWhenDisabled(t *testing.T) {
	// No /set or /Action route: if the handler tries to write or reload, the
	// fake 400s and the handler errors — which is exactly what we want to catch.
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:MtaTlsStrategy/query": strategyQueryRoute([]string{"idDef", "idInv"}),
		"x:MtaTlsStrategy/get": strategyGetRoute([]map[string]any{
			{"id": "idDef", "name": "default", "dane": "disable"},
			{"id": "idInv", "name": "invalid-tls", "dane": "disable"},
		}),
	})
	wireJMAP(t, srv)

	resp := callEnsure(t)
	if len(resp.Changed) != 0 {
		t.Fatalf("expected no change, got %v", resp.Changed)
	}
}

func TestOutboundTLS_SkipsOperatorCustomStrategy(t *testing.T) {
	var setPayload map[string]map[string]any
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:MtaTlsStrategy/query": strategyQueryRoute([]string{"idDef", "idInv", "idCustom"}),
		"x:MtaTlsStrategy/get": strategyGetRoute([]map[string]any{
			{"id": "idDef", "name": "default", "dane": "optional"},
			{"id": "idInv", "name": "invalid-tls", "dane": "optional"},
			{"id": "idCustom", "name": "my-strict-route", "dane": "require"},
		}),
		"x:MtaTlsStrategy/set": captureSetRoute(&setPayload),
		"x:Action/set":         func(json.RawMessage) (any, *jmapFakeError) { return map[string]any{}, nil },
	})
	wireJMAP(t, srv)

	resp := callEnsure(t)
	if len(resp.Changed) != 2 {
		t.Fatalf("expected 2 changed (default+invalid-tls), got %v", resp.Changed)
	}
	if _, touched := setPayload["idCustom"]; touched {
		t.Fatalf("operator custom strategy was clobbered: %v", setPayload)
	}
}

func TestOutboundTLS_MissingStrategyPatchesWhatExists(t *testing.T) {
	var setPayload map[string]map[string]any
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:MtaTlsStrategy/query": strategyQueryRoute([]string{"idDef"}),
		"x:MtaTlsStrategy/get": strategyGetRoute([]map[string]any{
			{"id": "idDef", "name": "default", "dane": "optional"},
		}),
		"x:MtaTlsStrategy/set": captureSetRoute(&setPayload),
		"x:Action/set":         func(json.RawMessage) (any, *jmapFakeError) { return map[string]any{}, nil },
	})
	wireJMAP(t, srv)

	resp := callEnsure(t)
	if len(resp.Changed) != 1 || resp.Changed[0] != "default" {
		t.Fatalf("expected only default patched, got %v", resp.Changed)
	}
}

func TestOutboundTLS_StalwartUnavailableSkips(t *testing.T) {
	// Point the URL at a closed port so jmapCall's HTTP Do() fails →
	// CodeUnavailable → the verb returns skipped, not an error.
	origURL, origTok, origClient := stalwartAdminURLFunc, stalwartAdminTokenFunc, stalwartHTTPClientFunc
	stalwartAdminURLFunc = func() string { return "http://127.0.0.1:1" }
	stalwartAdminTokenFunc = func() (string, error) { return "t", nil }
	t.Cleanup(func() {
		stalwartAdminURLFunc, stalwartAdminTokenFunc, stalwartHTTPClientFunc = origURL, origTok, origClient
	})

	resp := callEnsure(t)
	if !resp.Skipped {
		t.Fatalf("expected skipped=true when Stalwart unreachable, got %+v", resp)
	}
	if len(resp.Changed) != 0 {
		t.Fatalf("expected no changes on skip, got %v", resp.Changed)
	}
}
