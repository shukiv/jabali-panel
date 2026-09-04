package api

import "testing"

// GH #1462: the CalDAV/CardDAV override is interpolated unquoted into
// space-separated SRV content, so validateDAVHost must accept only a bare
// hostname[:port] and reject anything that could corrupt the record or
// redirect discovery (spaces, schemes, paths, bad ports).
func TestValidateDAVHost(t *testing.T) {
	ok := []string{
		"",                      // empty clears the override
		"nextcloud.example.com", // plain host
		"dav.example.com:8443",  // host:port
		"a.b.c.example.co.uk",   // multi-label
		"host-1.example.com",    // hyphen
	}
	for _, v := range ok {
		if err := validateDAVHost(v); err != nil {
			t.Errorf("validateDAVHost(%q) = %v, want nil", v, err)
		}
	}

	bad := []string{
		"nextcloud",                        // single label (no dot)
		"nextcloud.example.com:0",          // port 0
		"nextcloud.example.com:70000",      // port > 65535
		"nextcloud.example.com:abc",        // non-numeric port
		"https://nextcloud.example.com",    // scheme
		"nextcloud.example.com/remote.php", // path
		"nextcloud.example.com 443",        // space (SRV-content break)
		"next cloud.example.com",           // space in host
		"dav.exa\nmple.com",                // internal control char (not trimmable)
		"-bad.example.com",                 // label starts with hyphen
	}
	for _, v := range bad {
		if err := validateDAVHost(v); err == nil {
			t.Errorf("validateDAVHost(%q) = nil, want error", v)
		}
	}
}
