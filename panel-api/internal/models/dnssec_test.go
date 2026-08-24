package models

import "testing"

func TestParseDNSSECEnableReply(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantOK  bool
		wantLen int
	}{
		{"ok true with keys", `{"ok":true,"keys":[{"key_tag":1,"key_type":"KSK"},{"key_tag":2,"key_type":"ZSK"}]}`, true, 2},
		{"ok true no keys", `{"ok":true,"keys":[]}`, true, 0},
		{"ok false", `{"ok":false,"keys":[{"key_tag":1}]}`, false, 0},
		{"missing ok defaults false", `{"keys":[{"key_tag":1}]}`, false, 0},
		{"malformed json", `{"ok":true,"keys":`, false, 0},
		{"empty", ``, false, 0},
		{"garbage", `not json at all`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply, ok := ParseDNSSECEnableReply([]byte(tc.raw))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			// On a non-success the caller must not use keys (fail closed).
			if ok && len(reply.Keys) != tc.wantLen {
				t.Errorf("keys len = %d, want %d", len(reply.Keys), tc.wantLen)
			}
		})
	}
}
