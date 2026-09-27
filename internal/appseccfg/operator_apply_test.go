package appseccfg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The wire keys are the panel↔agent contract. Pin them so a rename on one side
// cannot silently decode as empty lists on the other.
func TestOperatorApplyParams_WireKeys(t *testing.T) {
	p := NewOperatorApplyParams(
		[]Exclusion{{Host: "forum.example.com", URIPrefix: "/api/", RuleID: "920450", Note: "n"}},
		[]HostMode{{Host: "code.example.com", Mode: HostModeDetect, Note: "m"}},
	)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"exclusions":[{"host":"forum.example.com","uri_prefix":"/api/","rule_id":"920450","note":"n"}],` +
		`"host_modes":[{"host":"code.example.com","mode":"detect","note":"m"}]}`
	if string(raw) != want {
		t.Fatalf("wire shape changed:\n got %s\nwant %s", raw, want)
	}
	var back OperatorApplyParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", back, p)
	}
}

// An empty desired state is legitimate (the last exclusion was removed), and it
// must reach the agent as [] so the agent removes the file.
func TestNewOperatorApplyParams_EmptyListsSendArrays(t *testing.T) {
	raw, err := json.Marshal(NewOperatorApplyParams(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"exclusions":[],"host_modes":[]}` {
		t.Fatalf("empty lists must marshal as [], got %s", raw)
	}
	var back OperatorApplyParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Check(); err != nil {
		t.Fatalf("an explicit empty desired state was refused: %v", err)
	}
}

// A missing or null list is refused, never read as "none": that would remove
// every live entry of that kind.
func TestOperatorApplyParams_CheckRefusesMissingLists(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"exclusions":[]}`,
		`{"host_modes":[]}`,
		`{"exclusions":null,"host_modes":[]}`,
		`{"exclusions":[],"host_modes":null}`,
	} {
		var p OperatorApplyParams
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		if err := p.Check(); err == nil || !strings.Contains(err.Error(), "both required") {
			t.Errorf("%s: want a both-required error, got %v", body, err)
		}
	}
}

func TestOperatorApplyParams_CheckBoundsEachList(t *testing.T) {
	over := make([]Exclusion, MaxOperatorApplyRows+1)
	if err := NewOperatorApplyParams(over, nil).Check(); err == nil {
		t.Error("an oversized exclusion list was accepted")
	}
	overModes := make([]HostMode, MaxOperatorApplyRows+1)
	if err := NewOperatorApplyParams(nil, overModes).Check(); err == nil {
		t.Error("an oversized host-mode list was accepted")
	}
	atLimit := make([]Exclusion, MaxOperatorApplyRows)
	if err := NewOperatorApplyParams(atLimit, nil).Check(); err != nil {
		t.Errorf("a list at the limit was refused: %v", err)
	}
}
