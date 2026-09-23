package models

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// package_egress.go — helpers for hosting_packages.egress_ssh_out_cidrs
// (GH #1798). The column scopes the per-package outbound-SSH allowance to a
// set of CIDRs; empty means "anywhere".

// DefaultEgressSSHOutCIDRs is what an unset egress_ssh_out_cidrs expands to:
// anywhere, IPv4 + IPv6. Applied only when a package has egress_ssh_out = 1
// but the admin left the scope blank.
var DefaultEgressSSHOutCIDRs = []string{"0.0.0.0/0", "::/0"}

// NormalizeEgressSSHOutCIDRs validates admin input for the
// egress_ssh_out_cidrs column and returns its canonical stored form. Empty
// (or whitespace) stays empty and means "anywhere" at apply time. A non-empty
// value MUST be a JSON array of CIDR strings, each parseable by net.ParseCIDR;
// the result is re-marshalled so the column always holds a clean array. This
// is the API-boundary validation the agent's nft renderer relies on — a
// malformed CIDR must never reach the ruleset.
func NormalizeEgressSSHOutCIDRs(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", nil
	}
	var cidrs []string
	if err := json.Unmarshal([]byte(in), &cidrs); err != nil {
		return "", fmt.Errorf("egress_ssh_out_cidrs must be a JSON array of CIDR strings: %w", err)
	}
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return "", fmt.Errorf("invalid CIDR %q in egress_ssh_out_cidrs", c)
		}
		out = append(out, c)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseEgressSSHOutCIDRs decodes the stored egress_ssh_out_cidrs value into
// the CIDRs to allow outbound :22 to. Empty (or an empty array) means anywhere
// and returns a copy of DefaultEgressSSHOutCIDRs. Any error (malformed JSON, or
// a CIDR that does not parse) is returned to the caller so the reconciler fails
// CLOSED — it drops the SSH-out allowance rather than falling open to "anywhere"
// or letting a bad token reach the agent's nft renderer verbatim.
//
// The CIDRs are re-validated here (not merely trusted from write-time
// Normalize) because a value can reach the column through a path that skips the
// API boundary — a backup restore, a cPanel import, a direct DB edit. Without
// this re-check a malformed CIDR would break the whole nft file and fail egress
// OPEN box-wide (the GH #681 scenario). net.ParseCIDR is the same gate the agent
// applies to operator-supplied defaults.
func ParseEgressSSHOutCIDRs(stored string) ([]string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return append([]string(nil), DefaultEgressSSHOutCIDRs...), nil
	}
	var cidrs []string
	if err := json.Unmarshal([]byte(stored), &cidrs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return nil, fmt.Errorf("invalid CIDR %q in egress_ssh_out_cidrs", c)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultEgressSSHOutCIDRs...), nil
	}
	return out, nil
}
