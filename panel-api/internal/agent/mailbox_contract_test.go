// Authoritative cross-boundary contract for the M6 mail commands:
//   mailbox.{create, delete, set_quota, set_password, usage}
//   domain.{email_enable, email_disable}
//
// Any drift between the typed panel-side structs in this file and the
// handler response shapes in panel-agent/internal/commands/mailbox_*.go
// or domain_email_*.go is a bug the bidirectional round-trip below will
// catch. Fixtures under testdata/ are the canonical wire bytes; keeping
// them ASCII + short so a code review of the JSON is humane.
//
// Pattern mirrors php_ext_contract_test.go (M9.6). Same generic round-
// trip helper so a future auditor can grep for roundTrip[X] to find
// every wire contract in the repo.
package agent

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"testing"
)

// --- fixtures --------------------------------------------------------

//go:embed testdata/mailbox_create_request.json
var mailboxCreateRequestFixture []byte

//go:embed testdata/mailbox_create_response.json
var mailboxCreateResponseFixture []byte

//go:embed testdata/mailbox_delete_request.json
var mailboxDeleteRequestFixture []byte

//go:embed testdata/mailbox_delete_response.json
var mailboxDeleteResponseFixture []byte

//go:embed testdata/mailbox_set_quota_request.json
var mailboxSetQuotaRequestFixture []byte

//go:embed testdata/mailbox_set_quota_response.json
var mailboxSetQuotaResponseFixture []byte

//go:embed testdata/mailbox_set_password_request.json
var mailboxSetPasswordRequestFixture []byte

//go:embed testdata/mailbox_set_password_response.json
var mailboxSetPasswordResponseFixture []byte

//go:embed testdata/mailbox_usage_request.json
var mailboxUsageRequestFixture []byte

//go:embed testdata/mailbox_usage_response.json
var mailboxUsageResponseFixture []byte

//go:embed testdata/domain_email_enable_request.json
var domainEmailEnableRequestFixture []byte

//go:embed testdata/domain_email_enable_response.json
var domainEmailEnableResponseFixture []byte

//go:embed testdata/domain_email_dkim_ensure_request.json
var domainEmailDkimEnsureRequestFixture []byte

//go:embed testdata/domain_email_dkim_ensure_response.json
var domainEmailDkimEnsureResponseFixture []byte

//go:embed testdata/domain_email_disable_request.json
var domainEmailDisableRequestFixture []byte

//go:embed testdata/domain_email_disable_response.json
var domainEmailDisableResponseFixture []byte

// --- typed panel-side shapes ----------------------------------------

// MailboxIdentityRequest is the request shape shared by mailbox.create,
// mailbox.delete, and mailbox.set_password (all three need only the
// row id plus the email address the agent will invalidate in Stalwart's
// cache). Consolidating into one type so the panel's handler code can
// use a single struct literal across the three calls.
type MailboxIdentityRequest struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type MailboxOkResponse struct {
	Ok bool `json:"ok"`
}

type MailboxSetQuotaRequest struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	QuotaBytes uint64 `json:"quota_bytes"`
}

type MailboxSetQuotaResponse struct {
	Ok         bool   `json:"ok"`
	QuotaBytes uint64 `json:"quota_bytes"`
}

type MailboxUsageRequest struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// MailboxUsageResponse is what the reconciler's 5-minute sampler writes
// into mailboxes.last_usage_*. Stalwart's admin-API reply carries more
// than this (roles, identity list, …); we intentionally narrow to the
// fields the panel consumes.
type MailboxUsageResponse struct {
	UsedBytes    uint64 `json:"used_bytes"`
	MessageCount uint64 `json:"message_count"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
}

type DomainEmailRequest struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
}

type DomainEmailEnableResponse struct {
	Ok               bool   `json:"ok"`
	DKIMSelector     string `json:"dkim_selector"`
	DKIMPublicKey    string `json:"dkim_public_key"`
	DKIMRSASelector  string `json:"dkim_rsa_selector"`
	DKIMRSAPublicKey string `json:"dkim_rsa_public_key"`
}

// domain_email.dkim_ensure (GH #2475), as the reconciler's DKIM pass sends
// and reads it.
type DomainEmailDkimEnsureRequest struct {
	DomainName string `json:"domain_name"`
}

type DomainEmailDkimEnsureResponse struct {
	Ok               bool     `json:"ok"`
	Ed25519Selector  string   `json:"ed25519_selector"`
	Ed25519PublicKey string   `json:"ed25519_public_key"`
	RSASelector      string   `json:"rsa_selector"`
	RSAPublicKey     string   `json:"rsa_public_key"`
	Created          []string `json:"created"`
}

// --- round-trip helper ----------------------------------------------

// roundTripJSON proves fixture ↔ typed struct are semantically equal in
// both directions:
//
//  1. Unmarshal fixture into T.
//  2. Marshal T back to JSON.
//  3. Re-parse both the original fixture and the remarshalled bytes as
//     generic map[string]any + reflect.DeepEqual them.
//
// Compare-as-generic-JSON instead of byte-for-byte because Go's
// json.Marshal can reorder keys vs. the hand-written fixture. We care
// about semantic equality, not textual.
//
// A mismatch means either:
//  - T dropped a field the fixture has (loss of data on the wire, panel
//    would silently ignore incoming information) — caught by gotAny
//    missing a key.
//  - T introduced a field the fixture lacks with a non-zero default —
//    caught by gotAny having an extra key. (JSON omitempty protects
//    zero-value cases, which is why LastUsedAt uses omitempty.)
func roundTripJSON[T any](t *testing.T, raw []byte) {
	t.Helper()
	var typed T
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatalf("unmarshal %T: %v", typed, err)
	}
	remarshalled, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("remarshal %T: %v", typed, err)
	}
	var gotAny any
	if err := json.Unmarshal(remarshalled, &gotAny); err != nil {
		t.Fatalf("re-unmarshal %T: %v", typed, err)
	}
	var wantAny any
	if err := json.Unmarshal(raw, &wantAny); err != nil {
		t.Fatalf("unmarshal raw for %T: %v", typed, err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		gotPretty, _ := json.MarshalIndent(gotAny, "", "  ")
		wantPretty, _ := json.MarshalIndent(wantAny, "", "  ")
		t.Fatalf("%T round-trip mismatch\nwant:\n%s\ngot:\n%s", typed, wantPretty, gotPretty)
	}
}

// --- tests ----------------------------------------------------------

func TestMailboxCreateRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxIdentityRequest](t, mailboxCreateRequestFixture)
}

func TestMailboxCreateResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxOkResponse](t, mailboxCreateResponseFixture)
}

func TestMailboxDeleteRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxIdentityRequest](t, mailboxDeleteRequestFixture)
}

func TestMailboxDeleteResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxOkResponse](t, mailboxDeleteResponseFixture)
}

func TestMailboxSetQuotaRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxSetQuotaRequest](t, mailboxSetQuotaRequestFixture)
}

func TestMailboxSetQuotaResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxSetQuotaResponse](t, mailboxSetQuotaResponseFixture)
}

func TestMailboxSetPasswordRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxIdentityRequest](t, mailboxSetPasswordRequestFixture)
}

func TestMailboxSetPasswordResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxOkResponse](t, mailboxSetPasswordResponseFixture)
}

func TestMailboxUsageRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxUsageRequest](t, mailboxUsageRequestFixture)
}

func TestMailboxUsageResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxUsageResponse](t, mailboxUsageResponseFixture)
}

func TestDomainEmailEnableRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[DomainEmailRequest](t, domainEmailEnableRequestFixture)
}

func TestDomainEmailEnableResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[DomainEmailEnableResponse](t, domainEmailEnableResponseFixture)
}

func TestDomainEmailDkimEnsureRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[DomainEmailDkimEnsureRequest](t, domainEmailDkimEnsureRequestFixture)
}

func TestDomainEmailDkimEnsureResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[DomainEmailDkimEnsureResponse](t, domainEmailDkimEnsureResponseFixture)
}

func TestDomainEmailDisableRequest_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[DomainEmailRequest](t, domainEmailDisableRequestFixture)
}

func TestDomainEmailDisableResponse_RoundTrips(t *testing.T) {
	t.Parallel()
	roundTripJSON[MailboxOkResponse](t, domainEmailDisableResponseFixture)
}
