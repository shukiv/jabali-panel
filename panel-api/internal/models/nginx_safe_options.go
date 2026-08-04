package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

// Tenant-safe nginx domain options (GH #307).
//
// A curated, structured set of nginx behaviours a non-admin domain owner may
// set on their OWN vhost (when the admin has opted in via
// server_settings.tenant_domain_options_enabled). Unlike raw
// nginx_custom_directives (admin-only — SSRF/file-read/auth-strip surface),
// every option here renders to a FIXED, vetted directive with no caller-supplied
// target, path, or routing. The panel emits the directives; the tenant only
// chooses values.

// NginxSafeOptions is stored as JSON on the domain row.
type NginxSafeOptions struct {
	// MaxBodyMB sets client_max_body_size (MiB). 0 = unset (inherit default).
	MaxBodyMB int `json:"max_body_mb,omitempty"`
	// HSTS adds a Strict-Transport-Security header.
	HSTS bool `json:"hsts,omitempty"`
	// SecurityHeaders adds X-Frame-Options / X-Content-Type-Options /
	// Referrer-Policy.
	SecurityHeaders bool `json:"security_headers,omitempty"`
	// Gzip enables gzip for common text content types.
	Gzip bool `json:"gzip,omitempty"`
	// InterceptErrors (GH #879) shows the branded 500 page when the PHP app
	// returns a 5xx. A PHP fatal is a 500 with an EMPTY body, so without this
	// visitors get the browser's raw error screen. Tri-state override of
	// server_settings.intercept_app_errors_default: nil = inherit, true/false
	// = force per-domain. The default matters because apps like WordPress
	// render their own error pages (recovery-mode notice) and API endpoints
	// return JSON error bodies — interception replaces those.
	// NOT rendered by Render(): it must sit INSIDE the vhost's PHP locations
	// (location-scoped, 50x-only error_page so app 404/403 pages survive),
	// so the reconciler plumbs it as its own domain.create param instead.
	InterceptErrors *bool `json:"intercept_errors,omitempty"`
}

// maxBodyCapMB bounds client_max_body_size so a tenant can't set an absurd
// value; 10 GiB matches the panel vhost's own ceiling.
const maxBodyCapMB = 10240

// Validate rejects out-of-range values. Booleans are always valid.
func (o NginxSafeOptions) Validate() error {
	if o.MaxBodyMB < 0 || o.MaxBodyMB > maxBodyCapMB {
		return fmt.Errorf("max_body_mb must be 0..%d", maxBodyCapMB)
	}
	return nil
}

// Render returns the nginx directive block for the enabled options, each a
// fixed safe directive (no caller-supplied target/path/key). Empty when nothing
// is set. Indented to sit inside the server { } block where custom_directives
// are injected.
func (o NginxSafeOptions) Render() string {
	var b strings.Builder
	if o.MaxBodyMB > 0 {
		fmt.Fprintf(&b, "    client_max_body_size %dm;\n", o.MaxBodyMB)
	}
	if o.HSTS {
		b.WriteString(`    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;` + "\n")
	}
	if o.SecurityHeaders {
		b.WriteString(`    add_header X-Frame-Options "SAMEORIGIN" always;` + "\n")
		b.WriteString(`    add_header X-Content-Type-Options "nosniff" always;` + "\n")
		b.WriteString(`    add_header Referrer-Policy "strict-origin-when-cross-origin" always;` + "\n")
	}
	if o.Gzip {
		b.WriteString("    gzip on;\n")
		b.WriteString("    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss image/svg+xml;\n")
	}
	return b.String()
}

// Value implements driver.Valuer — JSON, "{}" when empty.
func (o NginxSafeOptions) Value() (driver.Value, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner for a JSON TEXT column.
func (o *NginxSafeOptions) Scan(src any) error {
	if src == nil {
		*o = NginxSafeOptions{}
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("NginxSafeOptions: unsupported scan type %T", src)
	}
	if len(b) == 0 {
		*o = NginxSafeOptions{}
		return nil
	}
	return json.Unmarshal(b, o)
}
