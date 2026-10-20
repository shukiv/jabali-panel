package dbshadow

import "testing"

func TestMysqladminUser(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"alice", "alice_mysqladmin", true},
		{"a1_b2", "a1_b2_mysqladmin", true},
		{"claude-uitest", "", false}, // "-" is a legal panel username, never a shadow
		{"_svc", "", false},
		{"Alice", "", false},
		{"", "", false},
		{"a234567890123456789012345678901b", "a234567890123456789012345678901b_mysqladmin", true}, // 32 chars
		{"a2345678901234567890123456789012x", "", false},                                          // 33 chars
	}
	for _, c := range cases {
		got, ok := MysqladminUser(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("MysqladminUser(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
