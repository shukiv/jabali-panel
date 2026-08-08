package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AutomationToken — M44 HMAC-signed bearer token for external
// automations. Only `id`, `name`, `scopes`, and audit-trail fields
// are visible to listing endpoints; `secret_enc` is decrypted by the
// HMAC verify middleware on every request and never leaves the
// process. Plaintext secret is returned once at mint time.
type AutomationToken struct {
	ID         string           `gorm:"column:id;primaryKey;type:char(26)" json:"id"`
	Name       string           `gorm:"column:name;type:varchar(100);not null;uniqueIndex:uq_automation_tokens_name" json:"name"`
	Scopes     AutomationScopes `gorm:"column:scopes_json;type:json;not null" json:"scopes"`
	SecretEnc  []byte           `gorm:"column:secret_enc;type:varbinary(255);not null" json:"-"`
	CreatedBy  *string          `gorm:"column:created_by;type:char(26)" json:"created_by,omitempty"`
	CreatedAt  time.Time        `gorm:"column:created_at;type:datetime(6);not null" json:"created_at"`
	LastUsedAt *time.Time       `gorm:"column:last_used_at;type:datetime(6)" json:"last_used_at,omitempty"`
	LastUsedIP *string          `gorm:"column:last_used_ip;type:varchar(45)" json:"last_used_ip,omitempty"`
	RevokedAt  *time.Time       `gorm:"column:revoked_at;type:datetime(6)" json:"revoked_at,omitempty"`
	// JAB-140: write-token safety. A write token is a headless, server-wide
	// admin key, so it carries extra guards independent of its scopes:
	//   WritesEnabled — master switch; false pauses ALL writes (reads unaffected).
	//   ExpiresAt     — optional short expiry; past → 401 (nil = no expiry).
	//   IPAllowlist   — optional CIDR set; client IP must match (empty/nil = any).
	WritesEnabled bool       `gorm:"column:writes_enabled;type:tinyint(1);not null;default:1" json:"writes_enabled"`
	ExpiresAt     *time.Time `gorm:"column:expires_at;type:datetime(6)" json:"expires_at,omitempty"`
	IPAllowlist   CIDRList   `gorm:"column:ip_allowlist_json;type:json" json:"ip_allowlist,omitempty"`
}

// CIDRList is a JSON-serialised []string of CIDR / IP entries for a token's IP
// allowlist (JAB-140). nil / empty = no restriction.
type CIDRList []string

func (c *CIDRList) Scan(src any) error {
	if src == nil {
		*c = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("cidr_list: unsupported scan type")
	}
	if len(b) == 0 {
		*c = nil
		return nil
	}
	return json.Unmarshal(b, c)
}

func (c CIDRList) Value() (driver.Value, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}

func (AutomationToken) TableName() string { return "automation_tokens" }

// AutomationScopes is a typed wrapper around []string with GORM
// JSON serialisation. Keeps the scope list explicit at the struct
// level while letting MariaDB store it as JSON for ad-hoc admin
// queries.
type AutomationScopes []string

// Scan implements the sql.Scanner interface.
func (s *AutomationScopes) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("automation_scopes: unsupported scan type")
	}
	if len(b) == 0 {
		*s = nil
		return nil
	}
	return json.Unmarshal(b, s)
}

// Value implements the driver.Valuer interface.
func (s AutomationScopes) Value() (driver.Value, error) {
	if s == nil {
		s = AutomationScopes{}
	}
	return json.Marshal(s)
}

// Has returns true when the scope grants the requested capability.
// "read:*" matches any "read:..." capability; an exact-match scope
// like "read:domains" matches its own capability and nothing else.
// Unknown wildcards (e.g. a typo "rea:*") never match anything.
func (s AutomationScopes) Has(want string) bool {
	for _, granted := range s {
		if granted == want {
			return true
		}
		// Wildcard support: "read:*" matches "read:<anything>".
		if len(granted) > 2 && granted[len(granted)-2:] == ":*" {
			prefix := granted[:len(granted)-1] // "read:"
			if len(want) >= len(prefix) && want[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// AllowedAutomationScopes is the single source of truth for valid automation
// token scopes, shared by the admin REST handler and the root CLI (Gitea #544)
// so scope validation can't drift between the two surfaces.
var AllowedAutomationScopes = []string{
	"read:*",
	"read:domains",
	"read:users",
	"read:applications",
	"read:status",
	"read:mail",
	// ADR-0164: hosting-package list for billing-panel product mapping.
	"read:packages",
	// JAB-140 write scopes — least-privilege, independent of read. read:* does
	// NOT imply any write scope (Has() wildcard is prefix-scoped).
	"write:*",
	"write:services",
	"write:users",
	"write:domains",
	"write:cache",
	"write:backups",
	// JAB-140 delete scopes — RESERVED (no destructive endpoint ships in M2).
	// Kept distinct from write:* so a create-capable token can never
	// cascade-delete; a future destructive verb must demand delete:<resource>.
	"delete:*",
	"delete:users",
	"delete:domains",
}

// IsAllowedAutomationScope reports whether s is a recognised automation scope.
func IsAllowedAutomationScope(s string) bool {
	for _, a := range AllowedAutomationScopes {
		if a == s {
			return true
		}
	}
	return false
}

// scopeFamilies are the wildcard prefixes checked for the "both wildcard and an
// explicit child" conflict + dedup. JAB-140 extends the JAB-84 read-only logic
// symmetrically to write:* and delete:*.
var scopeFamilies = []string{"read:", "write:", "delete:"}

// hasWildcardConflictFamily reports whether the list holds BOTH "<fam>*" and an
// explicit "<fam><resource>" (fam includes the trailing colon, e.g. "read:").
func hasWildcardConflictFamily(scopes []string, fam string) bool {
	wildcard := fam + "*"
	var wild, child bool
	for _, s := range scopes {
		if s == wildcard {
			wild = true
		} else if strings.HasPrefix(s, fam) {
			child = true
		}
	}
	return wild && child
}

// HasWildcardConflict reports whether ANY scope family (read/write/delete) has a
// wildcard + explicit-child conflict — a redundant combination the mint flow
// rejects: within a family a token is either wildcard or explicit, not both.
func HasWildcardConflict(scopes []string) bool {
	for _, fam := range scopeFamilies {
		if hasWildcardConflictFamily(scopes, fam) {
			return true
		}
	}
	return false
}

// HasReadWildcardConflict is retained for callers that predate JAB-140; it now
// covers every scope family via HasWildcardConflict.
func HasReadWildcardConflict(scopes []string) bool { return HasWildcardConflict(scopes) }

// NormalizeScopes drops redundant explicit "<fam><resource>" scopes when the
// family wildcard "<fam>*" is present, per family (JAB-84 + JAB-140). Order
// preserved.
func NormalizeScopes(scopes []string) []string {
	if !HasWildcardConflict(scopes) {
		return scopes
	}
	// A family is deduped only when its own wildcard is present.
	wildFam := map[string]bool{}
	for _, s := range scopes {
		for _, fam := range scopeFamilies {
			if s == fam+"*" {
				wildFam[fam] = true
			}
		}
	}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		drop := false
		for _, fam := range scopeFamilies {
			if wildFam[fam] && s != fam+"*" && strings.HasPrefix(s, fam) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, s)
		}
	}
	return out
}
