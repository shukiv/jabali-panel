package logaccess

import (
	"errors"
	"testing"
)

// TestValidateGrantScope is the parity contract for JAB-303: both the HTTP
// handler and the CLI mint through ValidateGrantScope, so this full truth table
// pins the identical accept/reject matrix for both adapters. The security-
// critical row is {non-admin, no domain} -> ErrDomainRequired: a nil-domain
// grant is server-wide, and before this fix the CLI let a tenant beneficiary
// obtain one.
func TestValidateGrantScope(t *testing.T) {
	cases := []struct {
		name           string
		isAdmin        bool
		domainProvided bool
		domainOwned    bool
		wantErr        error
	}{
		// Non-admin beneficiary (tenant).
		{"tenant, no domain -> rejected (server-wide is admin-only)", false, false, false, ErrDomainRequired},
		{"tenant, own domain -> allowed", false, true, true, nil},
		{"tenant, other's domain -> rejected", false, true, false, ErrDomainNotOwned},

		// Admin beneficiary.
		{"admin, no domain -> allowed (server-wide)", true, false, false, nil},
		{"admin, any domain -> allowed", true, true, false, nil},
		{"admin, own domain -> allowed", true, true, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGrantScope(tc.isAdmin, tc.domainProvided, tc.domainOwned)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}
