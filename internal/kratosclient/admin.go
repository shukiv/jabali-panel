package kratosclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// AdminTraits is the identity-trait payload Kratos stores under `identity.traits`.
// Jabali's identity schema (install/kratos-identity-schema.json) defines these
// three fields; the IsAdmin flag is boolean so `omitempty` would drop the false
// value and break the schema's required-fields check — tag is plain `json:"is_admin"`.
type AdminTraits struct {
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
	IsAdmin  bool   `json:"is_admin"`
}

// AdminIdentity is the subset of the Kratos admin-API identity shape that
// Jabali consumes. The full shape has more fields (metadata_public,
// verifiable_addresses, …) but we only read id + traits today.
type AdminIdentity struct {
	ID     string      `json:"id"`
	Traits AdminTraits `json:"traits"`
}

// CreateIdentityWithPassword posts to POST {admin}/admin/identities with a
// bcrypt-hashed password under credentials.password.config.hashed_password.
// Kratos 1.3.1 accepts $2a$/$2b$/$2y$ hashes verbatim when the config enables
// hashers.bcrypt (see install/kratos.yml.tmpl). Returns the new identity ID.
//
// passwordHash MUST be a full bcrypt digest (60 chars, starts with $2a$, $2b$,
// or $2y$). Passing a plain password or a truncated hash is caught by Kratos
// with a 400 — but we refuse empty/obviously-too-short hashes up front so the
// operator sees a local-level error with context.
func (c *Client) CreateIdentityWithPassword(ctx context.Context, traits AdminTraits, passwordHash string) (string, error) {
	if len(passwordHash) < 59 {
		return "", fmt.Errorf("createidentitywithpassword: bcrypt hash looks invalid (got %d chars, want 60)", len(passwordHash))
	}

	payload := map[string]any{
		"schema_id": "default",
		"traits":    traits,
		"credentials": map[string]any{
			"password": map[string]any{
				"config": map[string]any{
					"hashed_password": passwordHash,
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("createidentitywithpassword: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.adminURL+"/admin/identities", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("createidentitywithpassword: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("createidentitywithpassword: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(resp.Body)
		// 409 = duplicate traits. Look up the existing identity by
		// email + return its id so the caller can stamp the panel
		// row without manually cleaning Kratos. This is the
		// migration-rerun + rebuild-kratos-orphan path: a previous
		// destroy left the Kratos row behind, fresh create errors
		// here. Caller wants the existing id, not a new one.
		if resp.StatusCode == http.StatusConflict && traits.Email != "" {
			if id, lookupErr := c.IdentityIDByEmail(ctx, traits.Email); lookupErr == nil && id != "" {
				return id, ErrIdentityExisted
			}
		}
		return "", fmt.Errorf("createidentitywithpassword: status %d: %s", resp.StatusCode, string(errBody))
	}

	var result AdminIdentity
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("createidentitywithpassword: decode: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("createidentitywithpassword: kratos returned empty id")
	}

	return result.ID, nil
}

// ErrIdentityExisted is returned by CreateIdentityWithPassword when
// Kratos rejected the create with 409 conflict (duplicate trait) but
// we successfully looked up the existing identity by email. The
// returned id IS valid + the caller should stamp the panel row.
// Distinguished from a "real" error so callers can fast-path the
// reuse without parsing strings.
var ErrIdentityExisted = fmt.Errorf("kratos identity already exists for traits")

// IdentityIDByEmail returns the Kratos identity id whose primary
// trait email matches. Scans pages until found; "" + nil when no
// match. Used by CreateIdentityWithPassword's 409 conflict path.
func (c *Client) IdentityIDByEmail(ctx context.Context, email string) (string, error) {
	wanted := lc(email)
	if wanted == "" {
		return "", nil
	}
	token := ""
	for {
		page, next, err := c.ListIdentitiesPage(ctx, 250, token)
		if err != nil {
			return "", err
		}
		for _, id := range page {
			if lc(id.Traits.Email) == wanted {
				return id.ID, nil
			}
		}
		if next == "" {
			return "", nil
		}
		token = next
	}
}

// ErrIdentityNotFound is returned by GetIdentity when Kratos replies 404
// (or the identity-id is empty). Callers use this to distinguish "identity
// doesn't exist in Kratos" from transport/server errors — the rebuild
// command treats ErrIdentityNotFound as "needs rebuild" and any other
// error as "abort, something is wrong with Kratos itself".
var ErrIdentityNotFound = fmt.Errorf("kratos identity not found")

// GetIdentity looks up an identity by ID on the admin API. Returns
// ErrIdentityNotFound for 404 so callers can distinguish the "already
// rebuilt on a previous run" case from a transport failure. Any other
// non-2xx status returns a wrapped error. A nil identity-id short-circuits
// with ErrIdentityNotFound — rebuildOne passes this for users whose
// panel row has kratos_identity_id = NULL (shouldn't happen in practice
// since we only target NON-NULL rows, but defensive).
func (c *Client) GetIdentity(ctx context.Context, identityID string) (*AdminIdentity, error) {
	if identityID == "" {
		return nil, ErrIdentityNotFound
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID), nil)
	if err != nil {
		return nil, fmt.Errorf("getidentity: request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("getidentity: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrIdentityNotFound
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getidentity: status %d: %s", resp.StatusCode, string(errBody))
	}
	var result AdminIdentity
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("getidentity: decode: %w", err)
	}
	return &result, nil
}

// RecoveryCode is the subset of Kratos's admin recovery-code response Jabali
// consumes. Kratos returns more (`expires_at`, raw code) but operators only
// need the pre-filled URL — clicking it lands on the password-reset flow.
type RecoveryCode struct {
	RecoveryLink string `json:"recovery_link"`
	RecoveryCode string `json:"recovery_code"`
	ExpiresAt    string `json:"expires_at"`
}

// CreateRecoveryCode generates a recovery link for the given identity ID.
// Used by `jabali admin rebuild-kratos` (post-DB-loss recovery) so the
// operator can distribute a one-click password-reset URL per user without
// handing out a shared temp password.
//
// Kratos admin endpoint: POST /admin/recovery/code
// Body: {"identity_id": "<uuid>", "expires_in": "1h"}
// 201 response body: {"recovery_link": "...", "recovery_code": "...", "expires_at": "..."}.
//
// `expiresIn` accepts Kratos's duration format (e.g. "1h", "15m", "24h").
// Pass "" to use the server's default (1h in current configs).
func (c *Client) CreateRecoveryCode(ctx context.Context, identityID, expiresIn string) (*RecoveryCode, error) {
	if identityID == "" {
		return nil, fmt.Errorf("createrecoverycode: identityID is empty")
	}

	payload := map[string]any{"identity_id": identityID}
	if expiresIn != "" {
		payload["expires_in"] = expiresIn
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("createrecoverycode: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.adminURL+"/admin/recovery/code", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("createrecoverycode: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("createrecoverycode: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(resp.Body)
		// Kratos returns 404 when the admin recovery endpoint is
		// disabled in config (selfservice.methods.code.config.lifespan
		// missing OR the recovery flow disabled outright). Surface a
		// clear hint so the operator knows to flip the config rather
		// than chasing a phantom routing problem.
		if resp.StatusCode == http.StatusNotFound &&
			bytes.Contains(errBody, []byte("disabled by system administrator")) {
			return nil, fmt.Errorf(
				"createrecoverycode: Kratos admin recovery endpoint disabled — enable selfservice.methods.code.enabled=true in /etc/jabali-panel/kratos.yml + restart jabali-kratos.service. Detail: %s",
				string(errBody))
		}
		return nil, fmt.Errorf("createrecoverycode: status %d: %s", resp.StatusCode, string(errBody))
	}

	var result RecoveryCode
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("createrecoverycode: decode: %w", err)
	}
	if result.RecoveryLink == "" {
		return nil, fmt.Errorf("createrecoverycode: kratos returned empty recovery_link")
	}
	return &result, nil
}

// SetPassword replaces the password credential on an existing identity via
// PATCH /admin/identities/{id} with an RFC 6902 JSON patch. Used by `jabali
// user password <id>` (operator-initiated reset).
//
// passwordHash MUST be a full bcrypt digest (60 chars, $2a$/$2b$/$2y$). The
// JSON patch op is "add" because it creates the `password` credential block
// when the identity was provisioned without one (Kratos drops empty credential
// objects on create) — per RFC 6902, `add` also replaces an existing member
// at that path, which is exactly the reset semantics we want here.
func (c *Client) SetPassword(ctx context.Context, identityID, passwordHash string) error {
	if identityID == "" {
		return fmt.Errorf("setpassword: identityID is empty")
	}
	if len(passwordHash) < 59 {
		return fmt.Errorf("setpassword: bcrypt hash looks invalid (got %d chars, want 60)", len(passwordHash))
	}

	patch := []map[string]any{
		{
			"op":    "add",
			"path":  "/credentials/password/config/hashed_password",
			"value": passwordHash,
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("setpassword: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("setpassword: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json-patch+json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("setpassword: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrIdentityNotFound
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setpassword: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

// UpdateUsernameTrait PATCHes identity.traits.username. Under the M54 v2
// schema (username = password identifier), this makes Kratos re-derive
// credentials.password.identifiers from the new trait — flipping the login
// identifier from email to username WITHOUT touching credentials.password.config
// (so the password hash is preserved). Proven on Kratos v26.2.0 (M54 spike
// 2026-06-10). Uses json-patch `add` so it works whether or not the identity
// already had a username trait (RFC 6902 `add` replaces an existing member).
func (c *Client) UpdateUsernameTrait(ctx context.Context, identityID, username string) error {
	if identityID == "" {
		return fmt.Errorf("updateusernametrait: identityID is empty")
	}
	if username == "" {
		return fmt.Errorf("updateusernametrait: username is empty (would leave the identity with no login identifier)")
	}
	patch := []map[string]any{
		{"op": "add", "path": "/traits/username", "value": username},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("updateusernametrait: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("updateusernametrait: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json-patch+json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("updateusernametrait: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrIdentityNotFound
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("updateusernametrait: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

// RemoveSecondFactor strips the totp + lookup_secret credentials from
// an identity. Used by the admin "Reset 2FA" path when a user has
// lost their authenticator and burned through their recovery codes.
// The user keeps their password; on next login they're back to aal1
// and can opt back into 2FA from /profile.
//
// JSON-Patch `remove` against a missing path returns 422; we issue the
// two removes as separate requests and treat 422/404 as success — the
// desired end state is "no second factor", and we don't care which
// (or neither) was previously enrolled. 404 on the identity itself is
// returned as ErrIdentityNotFound so the caller can render a clean
// 404 to the operator.
// SetIdentityState flips Kratos identity.state between "active" and
// "inactive" via PATCH /admin/identities/{id}. Inactive identities are
// rejected at /sessions/whoami (no new session) and existing sessions
// are invalidated at next request — so flipping to inactive logs the
// user out of panel + webmail + every Kratos-fronted UI within ~1 req.
//
// Used by the admin user-suspend endpoint. Returns ErrIdentityNotFound
// for 404 so the caller can render a clean 404 instead of a 5xx.
func (c *Client) SetIdentityState(ctx context.Context, identityID, state string) error {
	if identityID == "" {
		return fmt.Errorf("setidentitystate: identityID is empty")
	}
	if state != "active" && state != "inactive" {
		return fmt.Errorf("setidentitystate: state must be active|inactive, got %q", state)
	}
	patch := []map[string]any{{"op": "replace", "path": "/state", "value": state}}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("setidentitystate: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("setidentitystate: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json-patch+json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("setidentitystate: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrIdentityNotFound
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setidentitystate: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

func (c *Client) RemoveSecondFactor(ctx context.Context, identityID string) error {
	if identityID == "" {
		return fmt.Errorf("removesecondfactor: identityID is empty")
	}
	for _, path := range []string{"/credentials/totp", "/credentials/lookup_secret"} {
		patch := []map[string]any{{"op": "remove", "path": path}}
		body, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("removesecondfactor: marshal %s: %w", path, err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
			c.adminURL+"/admin/identities/"+url.PathEscape(identityID), bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("removesecondfactor: request %s: %w", path, err)
		}
		httpReq.Header.Set("Content-Type", "application/json-patch+json")
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("removesecondfactor: do %s: %w", path, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			// Removed.
		case http.StatusNotFound:
			return ErrIdentityNotFound
		case http.StatusUnprocessableEntity, http.StatusBadRequest:
			// Path didn't exist — that means this method wasn't enrolled.
			// Treat as success; the desired state is "absent".
		default:
			return fmt.Errorf("removesecondfactor: %s: status %d: %s", path, resp.StatusCode, string(respBody))
		}
	}
	return nil
}

// DeleteIdentity removes an identity by ID. 404 is treated as success because
// a missing identity is the desired end state; this keeps the delete idempotent
// during unwind/cleanup paths where we may race with a concurrent delete.
// Returns nil for an empty ID (callers pass the zero value when a prior create
// failed; the defer would otherwise hit /admin/identities/ and confuse the log).
func (c *Client) DeleteIdentity(ctx context.Context, identityID string) error {
	if identityID == "" {
		return nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID), nil)
	if err != nil {
		return fmt.Errorf("deleteidentity: request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("deleteidentity: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleteidentity: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

// ListIdentitiesPage fetches up to perPage identities starting at pageToken.
// Caller iterates until the returned next page token is empty.
//
// Kratos 1.3.1 admin-API pagination is keyset-based: the response includes a
// `Link: <...>; rel="next"` header whose URL exposes the next `page_token`.
// For simplicity we parse just the token out of that URL; if the header is
// missing or rel="next" isn't present, nextPageToken is returned as "".
func (c *Client) ListIdentitiesPage(ctx context.Context, perPage int, pageToken string) ([]AdminIdentity, string, error) {
	if perPage <= 0 {
		perPage = 250
	}

	q := url.Values{}
	q.Set("per_page", strconv.Itoa(perPage))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/admin/identities?"+q.Encode(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("listidentities: request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("listidentities: do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("listidentities: status %d: %s", resp.StatusCode, string(errBody))
	}

	var ids []AdminIdentity
	if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil {
		return nil, "", fmt.Errorf("listidentities: decode: %w", err)
	}

	next := parseNextPageToken(resp.Header.Get("Link"))
	return ids, next, nil
}

// AllIdentitiesByEmail scans every admin-identities page once and returns a map
// from lowercased email → identity ID. Used for migration idempotency: if an
// email already maps to a Kratos identity, kratos-migrate skips it instead of
// retrying CreateIdentityWithPassword (which would 409 on duplicate traits).
//
// Callers should accept that the map reflects the state at scan time; new
// identities created concurrently won't show up. For a single-operator panel
// this is acceptable.
func (c *Client) AllIdentitiesByEmail(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	token := ""
	for {
		page, next, err := c.ListIdentitiesPage(ctx, 250, token)
		if err != nil {
			return nil, err
		}
		for _, id := range page {
			if id.Traits.Email == "" {
				continue
			}
			out[lc(id.Traits.Email)] = id.ID
		}
		if next == "" {
			break
		}
		token = next
	}
	return out, nil
}

// parseNextPageToken extracts the `page_token` query param from the Link
// header's rel="next" URL, if present. Returns "" when there's no next page.
func parseNextPageToken(linkHeader string) string {
	// Simple parser: look for <URL>; rel="next" and pull page_token from URL.
	// Kratos format: `<https://.../admin/identities?per_page=250&page_token=ABC>; rel="next"`.
	if linkHeader == "" {
		return ""
	}
	for _, part := range splitCSV(linkHeader) {
		if !containsRel(part, "next") {
			continue
		}
		u := extractURL(part)
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		return parsed.Query().Get("page_token")
	}
	return ""
}

func splitCSV(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func containsRel(segment, want string) bool {
	// segment like: `<url>; rel="next"` — cheap substring match is fine.
	return bytes.Contains([]byte(segment), []byte(`rel="`+want+`"`))
}

func extractURL(segment string) string {
	start := bytes.IndexByte([]byte(segment), '<')
	end := bytes.IndexByte([]byte(segment), '>')
	if start < 0 || end < 0 || end <= start+1 {
		return ""
	}
	return segment[start+1 : end]
}

func lc(s string) string {
	// ASCII-only lowercase is fine for email local-parts + domains.
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// ExportedIdentity represents a single identity in Kratos export format.
// This matches the structure returned by Kratos export API.
type ExportedIdentity struct {
	ID                  string          `json:"id"`
	SchemaID            string          `json:"schema_id"`
	Traits              json.RawMessage `json:"traits"`
	State               string          `json:"state"`
	StateChangedAt      *string         `json:"state_changed_at,omitempty"`
	MetadataPublic      json.RawMessage `json:"metadata_public,omitempty"`
	MetadataAdmin       json.RawMessage `json:"metadata_admin,omitempty"`
	AvailableAAL        json.RawMessage `json:"available_aal,omitempty"`
	Credentials         json.RawMessage `json:"credentials,omitempty"`
	RecoveryAddresses   json.RawMessage `json:"recovery_addresses,omitempty"`
	VerifiableAddresses json.RawMessage `json:"verifiable_addresses,omitempty"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

// ExportIdentities returns every identity WITH its password credential (the
// bcrypt hash), suitable for embedding in a backup bundle and re-importing via
// ImportIdentities.
//
// GH #954 post-mortem: the old implementation did one GET with `?export=true` —
// but `export=true` is not a Kratos API parameter (silently ignored) and list
// responses NEVER include credentials, so every backup's "exported identity"
// lacked the password hash and the restored user could not log in. The real
// contract (verified against Kratos v26.2.0): list to enumerate ids, then
// re-fetch each with `?include_credential=password`, which DOES return
// credentials.password.config.hashed_password.
func (c *Client) ExportIdentities(ctx context.Context) ([]ExportedIdentity, error) {
	var out []ExportedIdentity
	token := ""
	for {
		page, next, err := c.ListIdentitiesPage(ctx, 250, token)
		if err != nil {
			return nil, fmt.Errorf("exportidentities: %w", err)
		}
		for _, id := range page {
			full, err := c.getExportedIdentity(ctx, id.ID)
			if err != nil {
				return nil, fmt.Errorf("exportidentities: identity %s: %w", id.ID, err)
			}
			out = append(out, *full)
		}
		if next == "" {
			break
		}
		token = next
	}
	return out, nil
}

// getExportedIdentity fetches one identity including its password credential.
func (c *Client) getExportedIdentity(ctx context.Context, identityID string) (*ExportedIdentity, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID)+"?include_credential=password", nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(errBody))
	}
	var full ExportedIdentity
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &full, nil
}

// exportedPasswordHash extracts credentials.password.config.hashed_password
// from an exported identity's raw credentials blob. Empty when the blob is
// missing, malformed, or carries no password credential (old backups made
// before ExportIdentities captured credentials).
func exportedPasswordHash(credentials json.RawMessage) string {
	if len(credentials) == 0 {
		return ""
	}
	var creds struct {
		Password struct {
			Config struct {
				HashedPassword string `json:"hashed_password"`
			} `json:"config"`
		} `json:"password"`
	}
	if err := json.Unmarshal(credentials, &creds); err != nil {
		return ""
	}
	return creds.Password.Config.HashedPassword
}

// ImportIdentities re-creates previously exported identities.
//
// GH #954 post-mortem: the old implementation POSTed the batch to
// `/admin/identities/import` — a route that does not exist in Kratos (the
// router matches /admin/identities/{id} with id="import", where POST is not
// allowed → 405 on every restore, ever). The real contract is one
// POST /admin/identities per identity with a CreateIdentityBody: only the
// fields Kratos accepts on create, with the password credential transformed
// from the exported shape (password.config.hashed_password) into the create
// shape (credentials.password.config.hashed_password).
//
// An identity whose traits already exist (409 conflict) is skipped, not an
// error — restore re-runs and same-box drills hit this constantly.
func (c *Client) ImportIdentities(ctx context.Context, identities []ExportedIdentity) error {
	for _, id := range identities {
		schemaID := id.SchemaID
		if schemaID == "" {
			schemaID = "default"
		}
		payload := map[string]any{
			"schema_id": schemaID,
			"traits":    id.Traits,
		}
		if id.State != "" {
			payload["state"] = id.State
		}
		if len(id.MetadataPublic) > 0 && string(id.MetadataPublic) != "null" {
			payload["metadata_public"] = id.MetadataPublic
		}
		if len(id.MetadataAdmin) > 0 && string(id.MetadataAdmin) != "null" {
			payload["metadata_admin"] = id.MetadataAdmin
		}
		if hash := exportedPasswordHash(id.Credentials); hash != "" {
			payload["credentials"] = map[string]any{
				"password": map[string]any{
					"config": map[string]any{"hashed_password": hash},
				},
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("importidentities: marshal: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.adminURL+"/admin/identities", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("importidentities: request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("importidentities: do: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		status := resp.StatusCode
		if cerr := resp.Body.Close(); cerr != nil && status < 300 {
			return fmt.Errorf("importidentities: close body: %w", cerr)
		}
		if status == http.StatusConflict {
			continue // identity already present — restore re-run / same-box drill
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return fmt.Errorf("importidentities: status %d", status)
		}
	}
	return nil
}

// IdentityHasPassword reports whether the identity has a password credential
// set (a stored hash). Restore flows use it to decide whether the account can
// actually log in or needs a recovery link.
func (c *Client) IdentityHasPassword(ctx context.Context, identityID string) (bool, error) {
	if identityID == "" {
		return false, ErrIdentityNotFound
	}
	full, err := c.getExportedIdentity(ctx, identityID)
	if err != nil {
		return false, fmt.Errorf("identityhaspassword: %w", err)
	}
	return exportedPasswordHash(full.Credentials) != "", nil
}

// randomCanarySuffix returns a hex-encoded random token for constructing a
// disposable email + unique trait values during the bcrypt canary check.
func randomCanarySuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// SessionDevice is the subset of a Kratos session device Jabali surfaces.
type SessionDevice struct {
	IPAddress string `json:"ip_address"`
	UserAgent string `json:"user_agent"`
	Location  string `json:"location"`
}

// AdminSession is the subset of a Kratos admin Session (GH #338) the panel's
// Active Sessions view consumes.
type AdminSession struct {
	ID              string          `json:"id"`
	Active          bool            `json:"active"`
	ExpiresAt       string          `json:"expires_at"`
	AuthenticatedAt string          `json:"authenticated_at"`
	AAL             string          `json:"authenticator_assurance_level"`
	Identity        *Identity       `json:"identity"`
	Devices         []SessionDevice `json:"devices"`
}

// ListActiveSessions returns all currently-active Kratos sessions with their
// identity + devices (GH #338). Kratos admin: GET /admin/sessions.
func (c *Client) ListActiveSessions(ctx context.Context) ([]AdminSession, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.adminURL+"/admin/sessions?active=true&expand=Identity&expand=Devices&page_size=250", nil)
	if err != nil {
		return nil, fmt.Errorf("listsessions: request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("listsessions: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("listsessions: status %d: %s", resp.StatusCode, string(errBody))
	}
	var result []AdminSession
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("listsessions: decode: %w", err)
	}
	return result, nil
}

// RevokeSession deactivates a single Kratos session (GH #338). Kratos admin:
// DELETE /admin/sessions/{id} (204 on success).
func (c *Client) RevokeSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("revokesession: empty session id")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.adminURL+"/admin/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return fmt.Errorf("revokesession: request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("revokesession: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revokesession: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

// RevokeIdentitySessions deletes ALL active sessions for one identity (JAB-120):
// the headless equivalent of "sign this user out everywhere". Kratos admin:
// DELETE /admin/identities/{id}/sessions. A 404 (identity has no sessions /
// unknown) is treated as success — the desired end state already holds.
func (c *Client) RevokeIdentitySessions(ctx context.Context, identityID string) error {
	if identityID == "" {
		return fmt.Errorf("revokeidentitysessions: empty identity id")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.adminURL+"/admin/identities/"+url.PathEscape(identityID)+"/sessions", nil)
	if err != nil {
		return fmt.Errorf("revokeidentitysessions: request: %w", err)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("revokeidentitysessions: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revokeidentitysessions: status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}
