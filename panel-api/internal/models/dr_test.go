package models

import "testing"

func TestServerRoleHelpers(t *testing.T) {
	cases := []struct {
		role      string
		isStandby bool
	}{
		{"", false}, // unseeded → primary (fail safe)
		{"primary", false},
		{"standby", true},
		{"garbage", false}, // unknown → primary, never wrongly park a live box
	}
	for _, c := range cases {
		s := ServerSettings{ServerRole: c.role}
		if s.IsStandby() != c.isStandby {
			t.Errorf("role %q: IsStandby()=%v want %v", c.role, s.IsStandby(), c.isStandby)
		}
		if s.IsPrimary() == c.isStandby {
			t.Errorf("role %q: IsPrimary() must be the inverse of IsStandby()", c.role)
		}
	}
}

func TestIsValidServerRole(t *testing.T) {
	for _, ok := range []string{"", "primary", "standby"} {
		if !IsValidServerRole(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"secondary", "PRIMARY", "replica"} {
		if IsValidServerRole(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
