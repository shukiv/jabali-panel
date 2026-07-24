package reconciler

import "testing"

// JAB-170 phase 3: a shared-cert domain's vhost points at the ONE shared pair
// under /etc/jabali/ssl/shared/<id>/, matching the agent's sslSharedRoot.
func TestSharedCertPaths(t *testing.T) {
	cert, key := sharedCertPaths("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	wantCert := "/etc/jabali/ssl/shared/01ARZ3NDEKTSV4RRFFQ69G5FAV/fullchain.pem"
	wantKey := "/etc/jabali/ssl/shared/01ARZ3NDEKTSV4RRFFQ69G5FAV/privkey.pem"
	if cert != wantCert {
		t.Errorf("cert = %q, want %q", cert, wantCert)
	}
	if key != wantKey {
		t.Errorf("key = %q, want %q", key, wantKey)
	}
}
