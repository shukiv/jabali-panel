package mailthrottle

import (
	"encoding/json"
	"strings"
	"testing"
)

// stalwartGet is what `stalwart-cli get MtaOutboundThrottle <id> --json`
// printed on the .60 test box (stalwart-cli 1.0.12) for a throttle created
// with Payload's shape; only the description is swapped for the one
// Description builds. Decoding it must give back an equal Throttle, or the
// panel would see a difference on every reconcile tick and rewrite the object
// (and reload Stalwart's settings) forever.
const stalwartGet = `{"enable":true,"description":"jabali user probe@example.invalid: 5 per hour","key":{"sender":true},"match":{"match":{"0":{"if":"sender == 'probe@example.invalid'","then":"true"}},"else":"false"},"rate":{"count":5,"period":3600000},"id":"jg1nyykmahqa"}`

func TestPayload_RoundTripsThroughStalwartGet(t *testing.T) {
	want := Payload(ApplyRequest{Scope: ScopeUser, ScopeRef: "probe@example.invalid", Window: WindowHour, Limit: 5})
	var got Throttle
	if err := json.Unmarshal([]byte(stalwartGet), &got); err != nil {
		t.Fatal(err)
	}
	if !want.Equal(got) {
		t.Fatalf("Stalwart's copy differs from the payload:\nwant %+v\ngot  %+v", want, got)
	}
}

func TestPayload_WireShape(t *testing.T) {
	cases := []struct {
		name  string
		req   ApplyRequest
		wants []string
	}{
		{
			name: "global hourly",
			req:  ApplyRequest{Scope: ScopeGlobal, Window: WindowHour, Limit: 100},
			wants: []string{
				`"enable":true`,
				`"key":{}`,
				`"rate":{"count":100,"period":3600000}`,
				`"match":{"match":{},"else":"true"}`,
			},
		},
		{
			name: "user daily",
			req:  ApplyRequest{Scope: ScopeUser, ScopeRef: "alice@example.com", Window: WindowDay, Limit: 500},
			wants: []string{
				`"key":{"sender":true}`,
				`"rate":{"count":500,"period":86400000}`,
				`"match":{"match":{"0":{"if":"sender == 'alice@example.com'","then":"true"}},"else":"false"}`,
			},
		},
		{
			name: "domain hourly",
			req:  ApplyRequest{Scope: ScopeDomain, ScopeRef: "example.com", Window: WindowHour, Limit: 50},
			wants: []string{
				`"key":{"senderDomain":true}`,
				`"match":{"match":{"0":{"if":"sender_domain == 'example.com'","then":"true"}},"else":"false"}`,
			},
		},
		{
			name: "every sender gets its own bucket",
			req:  ApplyRequest{Scope: ScopeUser, Window: WindowHour, Limit: 30},
			wants: []string{
				`"key":{"sender":true}`,
				`"match":{"match":{},"else":"true"}`,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.req.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			body, err := json.Marshal(Payload(c.req))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.wants {
				if !strings.Contains(string(body), w) {
					t.Errorf("payload missing %s\nfull: %s", w, body)
				}
			}
		})
	}
}

func TestApplyRequest_ValidateRejects(t *testing.T) {
	ok := ApplyRequest{Scope: ScopeUser, ScopeRef: "alice@example.com", Window: WindowHour, Limit: 1}
	cases := map[string]func(r *ApplyRequest){
		"quote in sender breaks out of the expression": func(r *ApplyRequest) { r.ScopeRef = "a@example.com' || true || '" },
		"backslash in sender":                          func(r *ApplyRequest) { r.ScopeRef = `a\@example.com` },
		"space in sender":                              func(r *ApplyRequest) { r.ScopeRef = "a b@example.com" },
		"quote in domain": func(r *ApplyRequest) {
			r.Scope, r.ScopeRef = ScopeDomain, "example.com' || true || '"
		},
		"uppercase domain":        func(r *ApplyRequest) { r.Scope, r.ScopeRef = ScopeDomain, "Example.com" },
		"email on domain scope":   func(r *ApplyRequest) { r.Scope = ScopeDomain },
		"ref on global scope":     func(r *ApplyRequest) { r.Scope = ScopeGlobal },
		"unknown scope":           func(r *ApplyRequest) { r.Scope = "mailbox" },
		"unknown window":          func(r *ApplyRequest) { r.Window = "minute" },
		"zero limit":              func(r *ApplyRequest) { r.Limit = 0 },
		"id that reads as a flag": func(r *ApplyRequest) { r.StalwartID = "-rf" },
		"id with a slash":         func(r *ApplyRequest) { r.StalwartID = "a/b" },
		"id with a space":         func(r *ApplyRequest) { r.StalwartID = "a b" },
		"overlong id":             func(r *ApplyRequest) { r.StalwartID = strings.Repeat("a", 65) },
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("baseline request rejected: %v", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := ok
			mutate(&r)
			if err := r.Validate(); err == nil {
				t.Fatalf("Validate accepted %+v", r)
			}
		})
	}
}

func TestValidStalwartID(t *testing.T) {
	if !ValidStalwartID("jg1nyykmahqa") {
		t.Fatal("valid id rejected")
	}
	for _, id := range []string{"", "-x", "a,b", "a b", "../x"} {
		if ValidStalwartID(id) {
			t.Errorf("id %q accepted", id)
		}
	}
}

func TestThrottleEqual(t *testing.T) {
	a := Payload(ApplyRequest{Scope: ScopeGlobal, Window: WindowHour, Limit: 10})
	b := a
	b.Key = nil
	b.Match.Match = nil
	if !a.Equal(b) {
		t.Error("nil and empty maps must compare equal")
	}
	c := a
	c.Rate.Count = 11
	if a.Equal(c) {
		t.Error("a different count must not compare equal")
	}
	d := Payload(ApplyRequest{Scope: ScopeUser, ScopeRef: "a@example.com", Window: WindowHour, Limit: 10})
	e := Payload(ApplyRequest{Scope: ScopeUser, ScopeRef: "b@example.com", Window: WindowHour, Limit: 10})
	e.Description = d.Description
	if d.Equal(e) {
		t.Error("a different match rule must not compare equal")
	}
}

// The reconciler removes unreferenced throttles by this prefix, so every
// description the panel writes must carry it.
func TestDescription_CarriesTheOwnedPrefix(t *testing.T) {
	for _, r := range []ApplyRequest{
		{Scope: ScopeGlobal, Window: WindowHour, Limit: 1},
		{Scope: ScopeUser, ScopeRef: "a@example.com", Window: WindowDay, Limit: 2},
		{Scope: ScopeDomain, Window: WindowHour, Limit: 3},
	} {
		if d := Payload(r).Description; !strings.HasPrefix(d, OwnedPrefix) {
			t.Errorf("description %q lacks %q", d, OwnedPrefix)
		}
	}
}
