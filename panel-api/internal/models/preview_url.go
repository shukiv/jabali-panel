package models

import "strings"

// Preview URL naming. A domain's preview host is
// <slug>.preview.<hostname>, where slug is the domain name with dots
// flattened to dashes ("shop.example.com" → "shop-example-com").
//
// The flattening is not injective ("my-site.com" and "my.site.com"
// collide on "my-site-com") — the enable handler refuses the second
// domain of such a pair, so a collision can never reach nginx as a
// duplicate server_name.

// PreviewSlug returns the single-label slug for a domain name.
func PreviewSlug(domainName string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSuffix(domainName, ".")), ".", "-")
}

// EffectivePreviewBase resolves the base every preview host hangs off:
// server_settings.preview_base when set (GH #836 — custom domain or
// magic-DNS base like 203-0-113-7.sslip.io), else the derived
// "preview.<hostname>". Empty when neither is configured — callers skip
// preview wiring entirely in that case.
func EffectivePreviewBase(srv *ServerSettings) string {
	if srv == nil {
		return ""
	}
	if b := NormalizePreviewBase(srv.PreviewBase); b != "" {
		return b
	}
	if srv.Hostname == "" {
		return ""
	}
	return "preview." + strings.TrimSuffix(strings.ToLower(srv.Hostname), ".")
}

// NormalizePreviewBase canonicalises operator input: lowercase, no
// leading "*." (the wildcard is implied), no trailing dot, no
// whitespace.
func NormalizePreviewBase(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "*.")
	return strings.TrimSuffix(v, ".")
}

// PreviewHost returns the full preview hostname for a domain under the
// given base. Empty when either is unset.
func PreviewHost(domainName, base string) string {
	if base == "" || domainName == "" {
		return ""
	}
	return PreviewSlug(domainName) + "." + base
}

// PreviewWildcardName is the DNS/SAN wildcard covering every preview
// host under base.
func PreviewWildcardName(base string) string {
	return "*." + base
}
