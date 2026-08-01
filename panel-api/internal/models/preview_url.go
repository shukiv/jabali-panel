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

// PreviewHost returns the full preview hostname for a domain under the
// panel hostname. Empty when hostname is unset — callers skip preview
// wiring entirely in that case.
func PreviewHost(domainName, panelHostname string) string {
	if panelHostname == "" || domainName == "" {
		return ""
	}
	return PreviewSlug(domainName) + "." + PreviewWildcardBase(panelHostname)
}

// PreviewWildcardBase is the label the wildcard DNS record + cert cover:
// "preview.<hostname>".
func PreviewWildcardBase(panelHostname string) string {
	return "preview." + strings.TrimSuffix(strings.ToLower(panelHostname), ".")
}

// PreviewWildcardName is the DNS/SAN wildcard: "*.preview.<hostname>".
func PreviewWildcardName(panelHostname string) string {
	return "*." + PreviewWildcardBase(panelHostname)
}
