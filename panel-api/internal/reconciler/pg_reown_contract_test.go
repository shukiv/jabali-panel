package reconciler

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The db.postgres.reown_superuser_objects wire contract (GH #2004). The same
// fixtures are decoded by the agent's handler test
// (panel-agent/internal/commands/db_postgres_reown_test.go), so a field
// renamed on either side fails one of them.

func pgReownRoundTrip[T any](t *testing.T, path string) {
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

func TestContract_PGReownRequest(t *testing.T) {
	pgReownRoundTrip[pgReownRequest](t, "../agent/testdata/db_postgres_reown_request.json")
}

func TestContract_PGReownResponse(t *testing.T) {
	pgReownRoundTrip[pgReownResponse](t, "../agent/testdata/db_postgres_reown_response.json")
}
