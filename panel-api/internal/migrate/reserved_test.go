package migrate

import "testing"

func TestIsReservedAccount(t *testing.T) {
	reserved := []string{"root", "ROOT", " root ", "admin", "www-data", "mysql", "clp", "systemd-resolve", "systemd-anything", "nobody"}
	for _, n := range reserved {
		if !IsReservedAccount(n) {
			t.Errorf("IsReservedAccount(%q) = false, want true", n)
		}
	}
	ok := []string{"alice", "customer1", "web-user", "acme", "", "rooty", "adminuser"}
	for _, n := range ok {
		if IsReservedAccount(n) {
			t.Errorf("IsReservedAccount(%q) = true, want false", n)
		}
	}
}
