package api

import (
	"encoding/json"
	"testing"
)

func TestValidateLogRetention(t *testing.T) {
	ok := []string{
		``,
		`null`,
		`{"notifications": 90}`,
		`{"audit": 0, "bandwidth": 3650}`,
	}
	for _, in := range ok {
		if err := validateLogRetention(json.RawMessage(in)); err != nil {
			t.Errorf("validateLogRetention(%q) = %v, want nil", in, err)
		}
	}
	bad := []string{
		`{"not_a_category": 90}`, // unknown key
		`{"updates": -1}`,        // negative
		`{"updates": 100000}`,    // over the cap
		`[1,2,3]`,                // wrong shape
	}
	for _, in := range bad {
		if err := validateLogRetention(json.RawMessage(in)); err == nil {
			t.Errorf("validateLogRetention(%q) = nil, want error", in)
		}
	}
}
