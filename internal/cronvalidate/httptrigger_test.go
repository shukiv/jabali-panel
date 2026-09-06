package cronvalidate

import (
	"net"
	"testing"
)

func ownDomains() []string { return []string{"own.example.com", "Aram.Example.com"} }

func TestValidateHTTPTrigger_Accepts(t *testing.T) {
	cases := []struct {
		desc string
		raw  string
	}{
		{"plain curl wp-cron", "curl -s https://own.example.com/wp-cron.php"},
		{"query string allowed", "curl https://own.example.com/pelegapi2.php?term=foo&x=1"},
		{"wget spider", "wget --spider https://own.example.com/manage/get_x.php"},
		{"wget -O /dev/null", "wget -q -O /dev/null https://own.example.com/wp-cron.php"},
		{"curl -o /dev/null", "curl -s -o /dev/null https://own.example.com/a.php"},
		{"http scheme ok", "curl http://own.example.com/keepalive.php"},
		{"case-insensitive host", "curl https://OWN.EXAMPLE.COM/a.php"},
		{"trailing-dot host", "curl https://own.example.com./a.php"},
		{"second owned domain", "wget --spider https://aram.example.com/get.php"},
		{"explicit :443", "curl https://own.example.com:443/a.php"},
		{"explicit :80", "curl http://own.example.com:80/a.php"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			cmd, err := ValidateHTTPTrigger(tc.raw, ownDomains())
			if err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if cmd.Kind != KindHTTPTrigger {
				t.Fatalf("Kind = %q, want %q", cmd.Kind, KindHTTPTrigger)
			}
			if len(cmd.Argv) != 4 || cmd.Argv[0] != jabaliBinPath || cmd.Argv[1] != "cron" || cmd.Argv[2] != "http-trigger" {
				t.Fatalf("Argv not wrapper invocation: %v", cmd.Argv)
			}
			if cmd.Argv[3] != cmd.URL {
				t.Fatalf("Argv url %q != URL %q", cmd.Argv[3], cmd.URL)
			}
		})
	}
}

func TestValidateHTTPTrigger_Rejects(t *testing.T) {
	cases := []struct {
		desc string
		raw  string
		code string
	}{
		{"foreign host (cross-domain)", "curl https://evil.com/x.php", ErrCodeHTTPForeignHost},
		{"foreign subdomain not owned", "curl https://sub.own.example.com/x.php", ErrCodeHTTPForeignHost},
		{"output to real file", "curl -o /tmp/loot https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"upload flag", "curl -T /etc/passwd https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"proxy flag", "curl -x http://127.0.0.1:8080 https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"follow redirects", "curl -L https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"data flag", "curl -d @/etc/passwd https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"wget execute (wgetrc)", "wget -e robots=off https://own.example.com/x.php", ErrCodeHTTPDangerousFlag},
		{"non-web port", "curl https://own.example.com:8080/x.php", ErrCodeHTTPBadPort},
		{"userinfo", "curl https://user:pass@own.example.com/x.php", ErrCodeHTTPUserinfo},
		{"multiple urls", "curl https://own.example.com/a https://own.example.com/b", ErrCodeHTTPMultipleURLs},
		{"no url", "curl -s -f", ErrCodeHTTPNoURL},
		{"file scheme (no http token)", "curl file:///etc/passwd", ErrCodeHTTPUnexpectedArg},
		{"gopher scheme (no http token)", "wget gopher://own.example.com/x", ErrCodeHTTPUnexpectedArg},
		{"not curl/wget", "php /home/u/x.php", ErrCodeHTTPNotCurlWget},
		{"unexpected bare arg", "curl foo https://own.example.com/x.php", ErrCodeHTTPUnexpectedArg},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := ValidateHTTPTrigger(tc.raw, ownDomains())
			if err == nil {
				t.Fatalf("expected reject %s, got accept", tc.code)
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("error is not *ValidationError: %v", err)
			}
			if ve.Code != tc.code {
				t.Fatalf("code = %q, want %q (detail: %s)", ve.Code, tc.code, ve.Detail)
			}
		})
	}
}

// A literal newline in the command must be rejected even on the curl path —
// it would break out of the single-quoted ExecStart token and inject a
// systemd unit directive.
func TestValidateHTTPTrigger_RejectsControlChars(t *testing.T) {
	raw := "curl https://own.example.com/x.php\n[Service]\nExecStartPre=/bin/evil"
	_, err := ValidateHTTPTrigger(raw, ownDomains())
	ve, ok := err.(*ValidationError)
	if !ok || ve.Code != ErrCodeMetacharReject {
		t.Fatalf("expected metachar_reject for embedded newline, got %v", err)
	}
}

// ValidateAny must route curl/wget to the http-trigger validator and
// everything else to the wp/php closed set.
func TestValidateAny_Routing(t *testing.T) {
	dr := []string{"/home/u/domains/own.example.com/public_html"}
	dom := ownDomains()

	cmd, err := ValidateAny("curl https://own.example.com/wp-cron.php", dr, dom, "")
	if err != nil || cmd.Kind != KindHTTPTrigger {
		t.Fatalf("curl should route to http-trigger: cmd=%v err=%v", cmd, err)
	}

	cmd, err = ValidateAny("php /home/u/domains/own.example.com/public_html/wp-cron.php", dr, dom, "")
	if err != nil {
		t.Fatalf("php should validate via wp/php path: %v", err)
	}
	if cmd.Kind == KindHTTPTrigger {
		t.Fatalf("php must not be http-trigger")
	}

	// A curl to a foreign host must be REJECTED, not silently accepted by
	// the wp/php path (which would reject it for a different reason).
	if _, err := ValidateAny("curl https://evil.com/x", dr, dom, ""); err == nil {
		t.Fatalf("foreign-host curl must be rejected by ValidateAny")
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", "169.254.1.1", // link-local incl. cloud metadata
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.0.1", "192.168.255.255",
		"fc00::1", "fd12:3456::1", // ULA
		"fe80::1",                 // link-local v6
		"0.0.0.0", "::",           // unspecified
		"224.0.0.1", "ff02::1",    // multicast
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "9.9.9.9",
		"172.15.255.255", "172.32.0.1", // just outside RFC1918
		"203.0.113.7",
		"2606:4700:4700::1111", "2001:4860:4860::8888", // public v6
	}
	for _, s := range blocked {
		if !IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be BLOCKED", s)
		}
	}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be ALLOWED", s)
		}
	}
	if !IsBlockedIP(nil) {
		t.Errorf("nil IP must be blocked")
	}
}
