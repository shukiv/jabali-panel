package userops

import "testing"

func TestParseReprovisionedUID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want uint32 // 0 means "expect nil"
	}{
		{"valid uid", `{"username":"alice","uid":1042,"home_dir":"/home/alice"}`, 1042},
		{"uid only", `{"uid":1007}`, 1007},
		{"zero uid ignored", `{"uid":0}`, 0},
		{"negative uid ignored", `{"uid":-1}`, 0},
		{"missing uid", `{"username":"alice"}`, 0},
		{"malformed json", `not json`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReprovisionedUID([]byte(tc.raw))
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("expected nil (keep prior uid), got %d", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected uid %d, got nil", tc.want)
			}
			if *got != tc.want {
				t.Fatalf("uid = %d, want %d", *got, tc.want)
			}
		})
	}
}
