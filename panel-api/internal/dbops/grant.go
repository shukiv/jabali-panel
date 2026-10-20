package dbops

import (
	"fmt"
	"strings"
)

// privilegesToCanonicalString converts a privileges slice to the canonical form:
// ["SELECT","INSERT","UPDATE","DELETE","CREATE","DROP","ALTER","INDEX"] → "SELECT,INSERT,UPDATE,DELETE,CREATE,DROP,ALTER,INDEX"
// ["ALL"] → "ALL"
// Returns empty string if the slice is empty or contains only whitespace.
func privilegesToCanonicalString(privs []string) (string, error) {
	if len(privs) == 0 {
		return "", nil
	}

	// Check if "ALL" is present.
	for _, p := range privs {
		if p == "ALL" {
			return "ALL", nil
		}
	}

	// Canonical order and valid tokens.
	canonicalOrder := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"}
	validTokens := map[string]bool{
		"SELECT": true,
		"INSERT": true,
		"UPDATE": true,
		"DELETE": true,
		"CREATE": true,
		"DROP":   true,
		"ALTER":  true,
		"INDEX":  true,
	}

	// Validate and collect seen privileges.
	seen := make(map[string]bool)
	for _, p := range privs {
		if !validTokens[p] {
			return "", fmt.Errorf("invalid privilege: %s", p)
		}
		seen[p] = true
	}

	// Build canonical string in order.
	var result []string
	for _, canonical := range canonicalOrder {
		if seen[canonical] {
			result = append(result, canonical)
		}
	}

	if len(result) == 0 {
		return "", nil
	}

	return strings.Join(result, ","), nil
}

// CanonicalDBGrant resolves a grant's privilege inputs into the stored
// canonical privilege string + the computed grant_level, applying the same
// validation the REST handler and the operator CLI both use (JAB-284) so the
// two adapters can never drift. Exactly one of an explicit privileges list or a
// rw/ro level must be supplied: rw = ALL, ro = SELECT; a custom list that is
// not exactly ALL/SELECT stores with level "custom".
func CanonicalDBGrant(privileges []string, level string) (canonical, computedLevel string, err error) {
	switch {
	case len(privileges) > 0:
		canonical, err = privilegesToCanonicalString(privileges)
		if err != nil {
			return "", "", err
		}
	case level == "rw":
		canonical = "ALL"
	case level == "ro":
		canonical = "SELECT"
	case level != "":
		return "", "", fmt.Errorf("grant_level must be 'rw' or 'ro' (got %q)", level)
	default:
		return "", "", fmt.Errorf("either privileges or grant_level must be provided")
	}
	computedLevel = "custom"
	switch canonical {
	case "ALL":
		computedLevel = "rw"
	case "SELECT":
		computedLevel = "ro"
	}
	return canonical, computedLevel, nil
}
