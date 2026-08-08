// Cross-boundary contract for the v1 wire shapes — the panel-side client
// (installer + certbot hook, phase 3) embeds the SAME fixtures and must
// round-trip them against its own typed structs. Same discipline as
// panel-api/internal/agent's contract tests: drift breaks a test, not an
// install at 2am.
package hostedsvc

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"testing"
)

//go:embed testdata/claim_request.json
var claimRequestFixture []byte

//go:embed testdata/claim_response.json
var claimResponseFixture []byte

//go:embed testdata/register_request.json
var registerRequestFixture []byte

//go:embed testdata/acme_present_request.json
var acmePresentRequestFixture []byte

//go:embed testdata/heartbeat_response.json
var heartbeatResponseFixture []byte

//go:embed testdata/error_response.json
var errorResponseFixture []byte

func roundTrip[T any](t *testing.T, raw []byte) {
	t.Helper()
	var typed T
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("unmarshal %T: %v", typed, err)
	}
	re, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("remarshal %T: %v", typed, err)
	}
	var got, want any
	if err := json.Unmarshal(re, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%T round-trip mismatch\nwant: %s\ngot:  %s", typed, raw, re)
	}
}

func TestWireFixturesRoundTrip(t *testing.T) {
	roundTrip[RegisterRequest](t, registerRequestFixture)
	roundTrip[ClaimRequest](t, claimRequestFixture)
	roundTrip[ClaimResponse](t, claimResponseFixture)
	roundTrip[AcmePresentRequest](t, acmePresentRequestFixture)
	roundTrip[HeartbeatResponse](t, heartbeatResponseFixture)
	roundTrip[ErrorResponse](t, errorResponseFixture)
}
