package websitemail

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The mail.relay.apply wire contract (GH #2056). The same fixtures are
// decoded by the agent's handler test
// (panel-agent/internal/commands/mail_relay_apply_test.go), so a field
// renamed on either side fails one of them.

func roundTrip[T any](t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var typed T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&typed); err != nil {
		t.Fatalf("decode %s into %T: %v", path, typed, err)
	}
	again, _ := json.Marshal(typed)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%T drops or renames fields:\nwant %s\ngot  %s", typed, raw, again)
	}
}

func TestContract_ApplyRequest(t *testing.T) {
	roundTrip[ApplyRequest](t, "../agent/testdata/mail_relay_apply_request.json")
}

func TestContract_ApplyResponse(t *testing.T) {
	roundTrip[ApplyResponse](t, "../agent/testdata/mail_relay_apply_response.json")
}
