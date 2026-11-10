package commands

// JMAP client for Stalwart v0.16's management API (ADR-0045).
//
// Stalwart's v0.15 REST surface under /api/principal/... is gone; all
// management operations happen through the JMAP-shaped endpoint at /jmap.
// This file is the narrow client the panel-agent command handlers use:
//
//  - accountIDByEmail(ctx, email): resolve a registry Account id from an
//    email string (uses Account/query with a filter).
//  - accountQuota(ctx, id): read quotaUsed + messageCount + lastAuthenticatedAt
//    for an Account (uses Account/get).
//  - accountDestroy(ctx, id): drop a stale registry Account record (uses
//    Account/set { destroy }).
//  - domainIDByName + domainDestroy: same shape for Domain objects.
//
// Auth is HTTP Basic with ("admin", <token from stalwart-admin.token>),
// paired with STALWART_RECOVERY_ADMIN=admin:<token> in stalwart.env so
// Stalwart accepts it against the env-seeded admin credential. A follow-up
// install.sh commit wires that env line.
//
// Timeouts: loopback JMAP calls resolve in single-digit ms on a healthy
// host; a 5-second cap catches process-wedge without surprising operators.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const (
	defaultStalwartAdminURL       = "http://127.0.0.1:8446"
	defaultStalwartAdminTokenPath = "/etc/jabali-panel/stalwart-admin.token"

	// jmapAPIPath is the Stalwart default path for the JMAP method endpoint.
	// Normally the client would discover this via GET /jmap/session; we
	// hardcode for simplicity because the panel-agent only talks to a
	// Stalwart we provisioned ourselves.
	jmapAPIPath = "/jmap"

	// jmapSessionPath is the session-discovery endpoint — not currently
	// used but reserved for a future capability-aware reconciler.
	jmapSessionPath = "/jmap/session"

	envStalwartAdminURL       = "JABALI_STALWART_ADMIN_URL"
	envStalwartAdminTokenPath = "JABALI_STALWART_ADMIN_TOKEN_PATH"

	stalwartHTTPTimeout = 5 * time.Second

	// jmapAdminUser is the fixed username the panel-agent uses when
	// authenticating against Stalwart. Paired with the token from
	// stalwart-admin.token via STALWART_RECOVERY_ADMIN.
	jmapAdminUser = "admin"
)

// USING is the JMAP capability list the client advertises in every
// request body. Matches what stalwart-cli itself sends (see upstream
// cli/src/jmap/protocol.rs USING constant).
var jmapUsing = []string{
	"urn:ietf:params:jmap:core",
	"urn:stalwart:jmap",
	// Identity/get + Identity/set (GH #197 display name → webmail From).
	"urn:ietf:params:jmap:submission",
}

// Test injection seams. _test.go files swap these and restore in Cleanup.
var stalwartHTTPClientFunc = func() *http.Client {
	return &http.Client{Timeout: stalwartHTTPTimeout}
}

var stalwartAdminURLFunc = func() string {
	if u := os.Getenv(envStalwartAdminURL); u != "" {
		return u
	}
	return defaultStalwartAdminURL
}

var stalwartAdminTokenFunc = func() (string, error) {
	path := os.Getenv(envStalwartAdminTokenPath)
	if path == "" {
		path = defaultStalwartAdminTokenPath
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-owned path; 0640 on disk
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// jmapRequestBody is the wire shape for a JMAP request (RFC 8620 §3.3).
// Matches the stalwart-cli Rust struct exactly.
type jmapRequestBody struct {
	Using       []string          `json:"using"`
	MethodCalls []jmapMethodCall  `json:"methodCalls"`
	CreatedIds  map[string]string `json:"createdIds,omitempty"`
}

// jmapMethodCall is a [name, args, callId] triple. We marshal as a
// 3-element JSON array to match the JMAP wire contract.
type jmapMethodCall struct {
	Name   string
	Args   any
	CallID string
}

func (c jmapMethodCall) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{c.Name, c.Args, c.CallID})
}

func (c *jmapMethodCall) UnmarshalJSON(data []byte) error {
	var arr [3]json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("method call must be a 3-element array: %w", err)
	}
	if err := json.Unmarshal(arr[0], &c.Name); err != nil {
		return fmt.Errorf("method name: %w", err)
	}
	c.Args = arr[1] // keep as RawMessage; caller decodes into their struct
	if err := json.Unmarshal(arr[2], &c.CallID); err != nil {
		return fmt.Errorf("method callId: %w", err)
	}
	return nil
}

// jmapResponseBody — response shape. Mirror of jmapRequestBody with
// `methodResponses` instead of `methodCalls`.
type jmapResponseBody struct {
	MethodResponses []jmapMethodCall  `json:"methodResponses"`
	SessionState    string            `json:"sessionState,omitempty"`
	CreatedIds      map[string]string `json:"createdIds,omitempty"`
}

// jmapCall issues a single-method JMAP POST against /jmap and decodes
// the single response into `out`. If Stalwart returns a "error" method
// (wrong credentials, invalid args, unknown method), this returns a
// structured AgentError. Connection-level failure is CodeUnavailable.
func jmapCall(ctx context.Context, method string, args any, out any) error {
	body := jmapRequestBody{
		Using: jmapUsing,
		MethodCalls: []jmapMethodCall{
			{Name: method, Args: args, CallID: "c0"},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal jmap request: %w", err)
	}

	url := stalwartAdminURLFunc() + jmapAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build jmap request: %w", err)
	}
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return fmt.Errorf("admin token: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(jmapAdminUser, token)

	resp, err := stalwartHTTPClientFunc().Do(req)
	if err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: fmt.Sprintf("stalwart JMAP unreachable: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "stalwart JMAP rejected basic auth — admin token rotated without agent restart?",
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart JMAP returned HTTP %d", resp.StatusCode),
		}
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read jmap response: %w", err)
	}
	var parsed jmapResponseBody
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart JMAP returned unparseable body: %v", err),
		}
	}
	if len(parsed.MethodResponses) != 1 {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("expected 1 method response, got %d", len(parsed.MethodResponses)),
		}
	}

	mr := parsed.MethodResponses[0]
	if mr.Name == "error" {
		// JMAP-level error: args carry { type, description }.
		var errResp struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		if raw, ok := mr.Args.(json.RawMessage); ok {
			_ = json.Unmarshal(raw, &errResp)
		}
		return &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart JMAP error: %s — %s",
				errResp.Type, errResp.Description),
		}
	}
	if mr.Name != method {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart JMAP method mismatch: expected %q, got %q", method, mr.Name),
		}
	}

	if out == nil {
		return nil
	}
	raw, ok := mr.Args.(json.RawMessage)
	if !ok {
		return fmt.Errorf("internal: method-call args not RawMessage")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart JMAP response decode: %v", err),
		}
	}
	return nil
}

// jmapQueryResult is the shape of <Object>/query responses.
type jmapQueryResult struct {
	IDs   []string `json:"ids"`
	Total uint64   `json:"total"`
}

// jmapGetResult wraps the `list` array returned by <Object>/get. Each
// element is an opaque raw message — callers decode into their own
// struct with the fields they care about.
type jmapGetResult struct {
	List     []json.RawMessage `json:"list"`
	NotFound []string          `json:"notFound"`
}

// jmapSetResult is the shape of <Object>/set responses.
type jmapSetResult struct {
	Created   map[string]json.RawMessage `json:"created,omitempty"`
	Updated   map[string]json.RawMessage `json:"updated,omitempty"`
	Destroyed []string                   `json:"destroyed,omitempty"`
	// NotCreated/NotUpdated/NotDestroyed carry { type, description } per id.
	NotCreated   map[string]json.RawMessage `json:"notCreated,omitempty"`
	NotUpdated   map[string]json.RawMessage `json:"notUpdated,omitempty"`
	NotDestroyed map[string]json.RawMessage `json:"notDestroyed,omitempty"`
}

// accountIDByEmail resolves a registry Account's id by its email
// address. Returns "" + nil if Stalwart doesn't have a record yet
// (mailbox in panel DB but nobody has authenticated yet — registry is
// populated lazily on first auth per ADR-0045). The caller decides
// whether absence is an error for their operation.
//
// Filter property: `emailAddress` (schema-verified — x:UserAccount has
// emailAddress as format:"emailAddress", derived server-side from
// name+'@'+domain.name; Group accounts use the same property name).
func accountIDByEmail(ctx context.Context, email string) (string, error) {
	// Stalwart v0.16 rejects filters on `emailAddress` with
	// `unsupportedFilter` even though the schema documents the property
	// — it only accepts filters on the stored columns (`name`,
	// `domainId`, `memberTenantId`, …). The SQL directory stores the
	// local part in `name` and the registry domain id in `domainId`, so
	// we reconstruct the compound filter from the email's two halves.
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		// Defence in depth — requireEmail should have rejected this.
		return "", fmt.Errorf("accountIDByEmail: malformed email %q", email)
	}
	localPart, domainName := email[:at], email[at+1:]

	// If the domain isn't in the registry yet, nobody has authed
	// against it, so there's no account to find either. Match the
	// "no registry entry" semantic the caller already expects (empty
	// id, nil error).
	domainID, err := domainIDByName(ctx, domainName)
	if err != nil {
		return "", err
	}
	if domainID == "" {
		return "", nil
	}

	args := map[string]any{
		// JMAP FilterCondition per RFC 8620 §4.4.1: property names at
		// the top level. The pair (name, domainId) is unique per
		// ADR-0042's schema (UNIQUE (domain_id, local_part)).
		"filter": map[string]any{
			"name":     localPart,
			"domainId": domainID,
		},
		"limit": 1,
	}
	var result jmapQueryResult
	if err := jmapCall(ctx, "x:Account/query", args, &result); err != nil {
		return "", err
	}
	if len(result.IDs) == 0 {
		return "", nil
	}
	return result.IDs[0], nil
}

// accountQuotaView is the subset of the x:UserAccount JMAP object
// that mailbox.usage needs. Property name + type verified against
// the upstream schema at
// github.com/stalwartlabs/stalwart/resources/schema/schema.json.gz
// (pulled 2026-04-21, SHA-URL-b64 aJJKvnpsjjwAEzJ0eKDWdfSLK_RvBKsLhk8BwCq-7qA).
//
// SCHEMA GAP — v0.16 does not expose per-Account properties
// equivalent to v0.15's usageResponse.messageCount or lastUsedAt.
// Verified absent from schema.json (regex grep:
//
//	'messageCount'        → 0 occurrences
//	'lastAuth*|lastLogin*' → 0 occurrences
//	'lastUsed*'           → 0 occurrences
//
// mailbox.usage therefore always returns message_count=0 and
// last_used_at="" against v0.16. The wire contract is preserved
// (panel-side sampler still sets last_usage_bytes); the two empty
// fields are pinned to their zero-value semantics in the handler.
// A future Stalwart version exposing message count or last-auth
// timestamp lifts this gap with a one-line property addition.
type accountQuotaView struct {
	UsedDiskQuota uint64 `json:"usedDiskQuota"`
}

// accountQuota fetches usage bytes for a resolved account id.
func accountQuota(ctx context.Context, id string) (accountQuotaView, error) {
	args := map[string]any{
		"ids":        []string{id},
		"properties": []string{"usedDiskQuota"},
	}
	var result jmapGetResult
	if err := jmapCall(ctx, "x:Account/get", args, &result); err != nil {
		return accountQuotaView{}, err
	}
	if len(result.List) == 0 {
		// Account existed at query time but vanished at get time —
		// race with a parallel delete. Treat as zero usage, no error.
		return accountQuotaView{}, nil
	}
	var view accountQuotaView
	if err := json.Unmarshal(result.List[0], &view); err != nil {
		return accountQuotaView{}, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("decode x:Account/get result: %v", err),
		}
	}
	return view, nil
}

// accountDestroy removes a registry Account record. Idempotent: if
// the id doesn't exist (already-destroyed race), the response's
// notDestroyed entry is ignored rather than surfaced as an error.
func accountDestroy(ctx context.Context, id string) error {
	args := map[string]any{
		"destroy": []string{id},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Account/set", args, &result); err != nil {
		return err
	}
	// Success if id appears in destroyed, or in notDestroyed with a
	// "not found" reason (already gone). Any other notDestroyed reason
	// is an error.
	for _, d := range result.Destroyed {
		if d == id {
			return nil
		}
	}
	if reason, ok := result.NotDestroyed[id]; ok {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type == "notFound" {
			return nil
		}
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart Account/set destroy %s refused: %s", id, string(reason)),
		}
	}
	// No signal either way — return ok (JMAP's contract is that the id
	// must appear in either destroyed or notDestroyed, so this branch
	// shouldn't fire, but don't fail the caller on a malformed response).
	return nil
}

// createDomain creates a new x:Domain via JMAP x:Domain/set and
// returns the server-assigned id. Field types verified against
// schema.json.gz (ADR-0045 §Schema-pull):
//
//   - name: string
//   - the enabled flag is left out: Stalwart 0.16 calls it isEnabled and 1.0
//     calls it enable, and 1.0 refuses the old name ("Invalid property").
//     Both create the domain enabled when it is missing (checked on 0.16.27
//     and 1.0.2).
//   - directoryId: nullable objectId (omitted → uses Authentication.directoryId)
//   - dkimManagement: x:DkimManagement (tagged-enum object, variants
//     {"@type":"Manual"} or {"@type":"Automatic", ...props}). We use
//     Manual — panel generates keys + publishes TXT via PowerDNS per ADR-0043.
//   - dnsManagement: x:DnsManagement ({"@type":"Manual"} or
//     {"@type":"Automatic", ...}). We use Manual — PowerDNS is the
//     single DNS writer per ADR-0002.
//   - certificateManagement: x:CertificateManagement ({"@type":"Manual"} —
//     we reuse the panel's certbot plumbing rather than Stalwart's ACME).
//   - subAddressing: x:SubAddressing ({"@type":"Enabled"} — plus-addressing
//     is standard and we don't need custom rewrites).
//
// All four tagged-enum objects are set explicitly. The schema marks
// them as non-nullable object properties, so omitting them risks a
// "required field missing" error at create time.
func createDomain(ctx context.Context, name string) (string, error) {
	args := map[string]any{
		"create": map[string]any{
			"#d1": map[string]any{
				"name":                  name,
				"dkimManagement":        map[string]any{"@type": "Manual"},
				"dnsManagement":         map[string]any{"@type": "Manual"},
				"certificateManagement": map[string]any{"@type": "Manual"},
				"subAddressing":         map[string]any{"@type": "Enabled"},
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Domain/set", args, &result); err != nil {
		return "", err
	}
	created, ok := result.Created["#d1"]
	if !ok {
		if reason, nok := result.NotCreated["#d1"]; nok {
			return "", &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("stalwart x:Domain/set create refused: %s", string(reason)),
			}
		}
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "stalwart x:Domain/set: neither created nor notCreated contained our tempId",
		}
	}
	var createdObj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &createdObj); err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("decode x:Domain/set create response: %v", err),
		}
	}
	return createdObj.ID, nil
}

// createDkimSignature creates an x:DkimSignature object of variant
// Dkim1Ed25519Sha256 with the given domainId + selector + PEM-wrapped
// PKCS#8 private key.
//
// Shape verified against schema.json.gz (ADR-0045 §Schema-pull):
//   - Tagged-enum @type = "Dkim1Ed25519Sha256"
//   - privateKey is x:SecretText variant "Text" with {secret: string}
//   - stage enum "active" (vs pending/retiring/retired — we want this
//     signing immediately since the panel has already published the
//     public half via PowerDNS)
//   - canonicalization enum "relaxed/relaxed" (industry standard)
//
// The PEM-wrapped PKCS#8 DER form of the Ed25519 seed is what
// Stalwart's signing path expects (source:
// crates/common/src/config/smtp/auth.rs —
// `Ed25519Key::from_pkcs8_maybe_unchecked_der`).
func createDkimSignature(ctx context.Context, domainID, selector, pemPrivateKey string) error {
	return createDkim1Signature(ctx, dkim1Ed25519Type, domainID, selector, pemPrivateKey)
}

// createDkim1Signature creates a DKIM1 x:DkimSignature of variant typ
// (dkim1Ed25519Type or dkim1RSAType, GH #2475). Both variants take the same
// fields; the RSA one takes a PKCS#1 or PKCS#8 PEM.
func createDkim1Signature(ctx context.Context, typ, domainID, selector, pemPrivateKey string) error {
	args := map[string]any{
		"create": map[string]any{
			"#sig1": map[string]any{
				"@type":    typ,
				"domainId": domainID,
				"selector": selector,
				"privateKey": map[string]any{
					"@type":  "Text",
					"secret": pemPrivateKey,
				},
				"canonicalization": "relaxed/relaxed",
				"stage":            "active",
				// RFC 6376 §5.4 canonical "sign these" set. Schema marks
				// headers as `set` (Map<String> in the registry), which
				// Stalwart serialises as {"<name>": true} — NOT a JSON
				// array. Sending [] rejects with "unsupportedFilter" or
				// a type-mismatch error at create time. See also the
				// NetworkListener.bind fix in the apply-plan template.
				"headers": map[string]bool{
					"From":         true,
					"To":           true,
					"Cc":           true,
					"Subject":      true,
					"Date":         true,
					"Message-ID":   true,
					"In-Reply-To":  true,
					"References":   true,
					"MIME-Version": true,
					"Content-Type": true,
				},
				"report": false,
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:DkimSignature/set", args, &result); err != nil {
		return err
	}
	if _, ok := result.Created["#sig1"]; ok {
		return nil
	}
	if reason, nok := result.NotCreated["#sig1"]; nok {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart x:DkimSignature/set create refused: %s", string(reason)),
		}
	}
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: "stalwart x:DkimSignature/set: unexpected response shape",
	}
}

// domainIDByName resolves a registry Domain's id by its name.
// Semantics identical to accountIDByEmail.
func domainIDByName(ctx context.Context, name string) (string, error) {
	args := map[string]any{
		// Same correction as accountIDByEmail — FilterCondition is
		// property→value pairs at the top level, not a single
		// {property, value} object.
		"filter": map[string]any{
			"name": name,
		},
		"limit": 1,
	}
	var result jmapQueryResult
	if err := jmapCall(ctx, "x:Domain/query", args, &result); err != nil {
		return "", err
	}
	if len(result.IDs) == 0 {
		return "", nil
	}
	return result.IDs[0], nil
}

// domainDestroy removes a registry Domain record. Same
// not-found-is-ok semantics as accountDestroy.
func domainDestroy(ctx context.Context, id string) error {
	args := map[string]any{
		"destroy": []string{id},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Domain/set", args, &result); err != nil {
		return err
	}
	for _, d := range result.Destroyed {
		if d == id {
			return nil
		}
	}
	if reason, ok := result.NotDestroyed[id]; ok {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type == "notFound" {
			return nil
		}
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart Domain/set destroy %s refused: %s", id, string(reason)),
		}
	}
	return nil
}

// requireEmail validates + lowercases an email-shaped string for agent
// commands. Unchanged from the v0.15 implementation — the email
// validation doesn't depend on the transport.
func requireEmail(raw string) (string, error) {
	if raw == "" {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "email parameter required"}
	}
	if strings.ContainsAny(raw, " \t\n\r;&|<>`$\\(){}'\"!*?[]") {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "shell metacharacter in email"}
	}
	if !strings.Contains(raw, "@") {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "email missing '@'"}
	}
	return strings.ToLower(raw), nil
}

// accountEnsureInRegistry creates the account in Stalwart's JMAP principal
// registry if it doesn't already exist. The SQL directory is authoritative for
// auth (ADR-0045), but JMAP operations (VacationResponse/set, Sieve scripts,
// forwarders) require an accountId which only exists after the first
// authentication — or after this call. Call from mailbox.create so downstream
// features work without waiting for first-auth.
//
// The domain is also created in the registry when absent. Both operations are
// idempotent: alreadyExists responses are treated as success.
func accountEnsureInRegistry(ctx context.Context, email string) error {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return fmt.Errorf("accountEnsureInRegistry: malformed email %q", email)
	}
	localPart, domainName := email[:at], email[at+1:]

	domainID, err := domainIDByName(ctx, domainName)
	if err != nil {
		return err
	}
	if domainID == "" {
		domainID, err = createDomain(ctx, domainName)
		if err != nil {
			return err
		}
	}

	args := map[string]any{
		"create": map[string]any{
			"#a1": map[string]any{
				"name":     localPart,
				"domainId": domainID,
				"@type":    "User",
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Account/set", args, &result); err != nil {
		return err
	}
	if _, ok := result.Created["#a1"]; ok {
		return nil
	}
	if reason, ok := result.NotCreated["#a1"]; ok {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		// alreadyExists OR primaryKeyViolation(email) both mean the
		// principal is already present — the idempotent success case.
		// Stalwart 1.0.8 returns primaryKeyViolation for an existing
		// account email (e.g. account-restore re-importing messages into
		// an account Bug B already recreated); treat it as success so the
		// caller proceeds to use the existing account.
		if r.Type == "alreadyExists" || r.Type == "primaryKeyViolation" {
			return nil
		}
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart x:Account/set create refused: %s", string(reason)),
		}
	}
	return nil
}

// okBody is the trivial positive response shape shared by commands
// whose ack carries no payload.
type okBody struct {
	Ok bool `json:"ok"`
}
