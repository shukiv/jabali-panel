package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Admin-configurable allowlist of the notification channel KINDS a non-admin
// tenant may create (JAB-171 phase 4b). A server-wide set enforced
// authoritatively in panel-api; admins are never restricted by it.
//
// Security-sensitive tenant cap → risky kinds default OFF. The stored value
// being empty means "use the safe default set" (OrDefault), NOT "deny all" —
// the whole surface is disabled via ServerSettings.TenantNotificationsEnabled,
// not by an empty allowlist.

// TenantConfigurableKinds is the closed set of kinds an admin MAY place in the
// allowlist. email is admin opt-in like webhook/slack/sms: a tenant email
// channel is forced to deliver only to the caller's own account address over the
// local submission path (phase 4d), so it carries no open-relay / SSRF risk.
var TenantConfigurableKinds = []string{
	NotificationChannelKindNtfy,
	NotificationChannelKindTelegram,
	NotificationChannelKindDiscord,
	NotificationChannelKindWebpush,
	NotificationChannelKindWebhook,
	NotificationChannelKindSlack,
	NotificationChannelKindSMS,
	NotificationChannelKindEmail,
}

// safeDefaultTenantKinds is the allowlist assumed when none is stored: the
// kinds that carry no tenant-supplied network destination (telegram/webpush) or
// whose destination is SSRF-guarded (ntfy/discord, phase 4a). webhook/slack/sms
// require an explicit admin opt-in.
var safeDefaultTenantKinds = []string{
	NotificationChannelKindNtfy,
	NotificationChannelKindTelegram,
	NotificationChannelKindDiscord,
	NotificationChannelKindWebpush,
}

// TenantNotificationKinds is a set of channel kinds, stored as a JSON array.
type TenantNotificationKinds []string

// DefaultTenantNotificationKinds returns a copy of the safe default set.
func DefaultTenantNotificationKinds() TenantNotificationKinds {
	out := make(TenantNotificationKinds, len(safeDefaultTenantKinds))
	copy(out, safeDefaultTenantKinds)
	return out
}

// OrDefault substitutes the safe default set when nothing is stored, so an
// unset column means "safe defaults", not "deny everything".
func (k TenantNotificationKinds) OrDefault() TenantNotificationKinds {
	if len(k) == 0 {
		return DefaultTenantNotificationKinds()
	}
	return k
}

// Allows reports whether tenants may create a channel of this kind. Fail-closed:
// an unknown kind is denied.
func (k TenantNotificationKinds) Allows(kind string) bool {
	want := strings.ToLower(strings.TrimSpace(kind))
	for _, v := range k {
		if v == want {
			return true
		}
	}
	return false
}

// Sanitize lowercases, de-duplicates and drops any kind not in
// TenantConfigurableKinds, so a PATCH cannot smuggle an unsupported or
// not-yet-safe kind (e.g. email) into the allowlist. Returns a stable-sorted
// set.
func (k TenantNotificationKinds) Sanitize() TenantNotificationKinds {
	allowed := make(map[string]bool, len(TenantConfigurableKinds))
	for _, c := range TenantConfigurableKinds {
		allowed[c] = true
	}
	seen := make(map[string]bool, len(k))
	out := make(TenantNotificationKinds, 0, len(k))
	for _, v := range k {
		key := strings.ToLower(strings.TrimSpace(v))
		if allowed[key] && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Value implements driver.Valuer — stored as a JSON array string; empty → "[]".
func (k TenantNotificationKinds) Value() (driver.Value, error) {
	if len(k) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal([]string(k))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner for a JSON array in a TEXT/JSON column.
func (k *TenantNotificationKinds) Scan(src any) error {
	if src == nil {
		*k = TenantNotificationKinds{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("TenantNotificationKinds: unsupported scan type %T", src)
	}
	if len(b) == 0 {
		*k = TenantNotificationKinds{}
		return nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("TenantNotificationKinds: unmarshal: %w", err)
	}
	*k = TenantNotificationKinds(out)
	return nil
}
