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

// FreeHostnameBase is the base domain of the Jabali free-hostname service
// (JAB-213). A panel whose hostname is <label>.jabalihosted.com gets its DNS
// + TLS from that service.
const FreeHostnameBase = "jabalihosted.com"

// EffectivePreviewBase resolves the base every preview host hangs off:
// server_settings.preview_base when set (GH #836 — custom domain or
// magic-DNS base like 203-0-113-7.sslip.io), else the derived default. Empty
// when neither is configured — callers skip preview wiring entirely.
func EffectivePreviewBase(srv *ServerSettings) string {
	if srv == nil {
		return ""
	}
	if b := NormalizePreviewBase(srv.PreviewBase); b != "" {
		return b
	}
	host := strings.TrimSuffix(strings.ToLower(srv.Hostname), ".")
	if host == "" {
		return ""
	}
	// JAB-213: on a free jabalihosted.com hostname, previews must sit EXACTLY
	// one label under <label>.jabalihosted.com — the service's wildcard A
	// (*.<label>) and the wildcard cert both cover a single label only, so
	// "<slug>.<label>.jabalihosted.com" resolves + is certified, whereas the
	// normal "preview.<hostname>" default would be two labels deep and covered
	// by neither. So the preview base IS the hostname itself here.
	if strings.HasSuffix(host, "."+FreeHostnameBase) {
		return host
	}
	return "preview." + host
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
