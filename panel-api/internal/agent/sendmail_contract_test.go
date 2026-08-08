// Authoritative cross-boundary contract for the JAB-230 relay-credential
// commands: sendmail.cred.{ensure, remove}. Panel-side typed structs here
// MUST stay in sync with the handler shapes in
// panel-agent/internal/commands/sendmail_cred.go; the bidirectional
// round-trip catches drift. Same pattern as mailbox_contract_test.go.
package agent

import (
	_ "embed"
	"testing"
)

//go:embed testdata/sendmail_cred_ensure_request.json
var sendmailCredEnsureRequestFixture []byte

//go:embed testdata/sendmail_cred_ensure_response.json
var sendmailCredEnsureResponseFixture []byte

//go:embed testdata/sendmail_cred_remove_request.json
var sendmailCredRemoveRequestFixture []byte

//go:embed testdata/sendmail_cred_remove_response.json
var sendmailCredRemoveResponseFixture []byte

// SendmailCredEnsureRequest provisions one per-domain relay credential for
// the jabali-sendmail shim (JAB-230).
type SendmailCredEnsureRequest struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Host     string `json:"host"`
	// MakeDefault re-points default.cred at this domain. The first cred a
	// user gets always becomes the default regardless.
	MakeDefault bool `json:"make_default,omitempty"`
}

type SendmailCredEnsureResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
}

type SendmailCredRemoveRequest struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
}

type SendmailCredRemoveResponse struct {
	Ok bool `json:"ok"`
}

func TestSendmailCredEnsureRequest_RoundTrips(t *testing.T) {
	roundTripJSON[SendmailCredEnsureRequest](t, sendmailCredEnsureRequestFixture)
}

func TestSendmailCredEnsureResponse_RoundTrips(t *testing.T) {
	roundTripJSON[SendmailCredEnsureResponse](t, sendmailCredEnsureResponseFixture)
}

func TestSendmailCredRemoveRequest_RoundTrips(t *testing.T) {
	roundTripJSON[SendmailCredRemoveRequest](t, sendmailCredRemoveRequestFixture)
}

func TestSendmailCredRemoveResponse_RoundTrips(t *testing.T) {
	roundTripJSON[SendmailCredRemoveResponse](t, sendmailCredRemoveResponseFixture)
}
