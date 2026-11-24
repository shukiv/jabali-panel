package domainops

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// DefaultCreateWWW is the www choice for a domain created without one: on for
// a main (registrable) domain such as example.com or example.co.il, off for a
// name under one (blog.example.com, whose www would be www.blog.example.com)
// and for a www. name itself. The public suffix list decides what is a main
// domain, so two-part suffixes (co.il, co.uk, com.au) count right.
func DefaultCreateWWW(name string) bool {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if n == "" || strings.HasPrefix(n, "www.") {
		return false
	}
	apex, err := publicsuffix.EffectiveTLDPlusOne(n)
	return err == nil && apex == n
}
