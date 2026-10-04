package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	templ "text/template"
)

// GH #1999: a domain's front_controller rule changes only the fallback of the
// default `location /` on a PHP domain.

func gh1999Vhost(fallback string, hasPHP bool) vhostData {
	return vhostData{
		Domain:         "demo.test",
		DocRoot:        "/home/u/demo",
		HasPHP:         hasPHP,
		Username:       "u",
		FPMSocket:      "/run/php/jabali-u/fpm.sock",
		IndexDirective: "index index.php index.html;",
		IsEnabled:      true,
		SSLCertPath:    "/etc/ssl/x.crt",
		SSLKeyPath:     "/etc/ssl/x.key",
		ListenIPv4:     "1.2.3.4",
		PHPFallback:    fallback,
	}
}

// Without a rule, every vhost shape renders byte for byte what the template
// rendered before GH #1999. The reference is the same template with the
// fallback written out literally, as it was.
func TestVhost_NoFrontControllerIsByteIdentical(t *testing.T) {
	const tok = "{{.TryFilesFallback}}"
	if strings.Count(vhostTemplate, tok) != 1 {
		t.Fatalf("expected the fallback token exactly once in vhostTemplate")
	}
	ref, err := templ.New("ref").Parse(strings.Replace(vhostTemplate, tok, "/index.php?$query_string", 1))
	if err != nil {
		t.Fatal(err)
	}
	shapes := map[string]vhostData{}
	base := gh1999Vhost("", true)
	shapes["php"] = base
	noPHP := base
	noPHP.HasPHP = false
	shapes["static"] = noPHP
	cached := base
	cached.CacheEnabled = true
	cached.CacheKeyZone = "jabali_fcgi"
	cached.CacheTTL = "60s"
	shapes["php+cache"] = cached
	intercept := base
	intercept.InterceptErrors = true
	shapes["php+intercept"] = intercept
	plainHTTP := base
	plainHTTP.SSLCertPath, plainHTTP.SSLKeyPath = "", ""
	shapes["php, no cert"] = plainHTTP
	for name, vd := range shapes {
		var want bytes.Buffer
		if err := ref.Execute(&want, vd); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := mustRenderVhost(t, vd); got != want.String() {
			t.Errorf("%s: vhost without a front controller rule changed", name)
		}
	}
}

func TestVhost_FrontControllerRendersFallback(t *testing.T) {
	out := mustRenderVhost(t, gh1999Vhost("/index.php?mod=$uri&$args", true))
	if !strings.Contains(out, "try_files $uri $uri/ /index.php?mod=$uri&$args;") {
		t.Fatalf("front controller fallback not rendered:\n%s", out)
	}
	if strings.Contains(out, "/index.php?$query_string") {
		t.Error("default fallback still rendered next to the rule's")
	}
	// PHP handling is untouched: the exact front-controller location, the
	// generic .php location and the FPM socket all stay.
	for _, want := range []string{"location = /index.php {", "location ~ \\.php$ {", "fastcgi_pass unix:/run/php/jabali-u/fpm.sock;"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestVhost_InvalidFrontControllerFallsBackToDefault(t *testing.T) {
	for _, bad := range []string{
		"/index.php?a=1; return 302 https://evil.test",
		"/index.php?a=$uri}\n    location /x { proxy_pass http://127.0.0.1:8443",
		"/index.php?a=$host",
		"index.php?a=1",
		"/../x.php",
	} {
		out := mustRenderVhost(t, gh1999Vhost(bad, true))
		if !strings.Contains(out, "try_files $uri $uri/ /index.php?$query_string;") {
			t.Errorf("%q: default fallback not rendered", bad)
		}
		if strings.Contains(out, "evil.test") || strings.Contains(out, "127.0.0.1:8443") || strings.Contains(out, "$host;") {
			t.Errorf("%q reached the vhost:\n%s", bad, out)
		}
	}
}

func TestVhost_FrontControllerIgnoredWithoutPHP(t *testing.T) {
	out := mustRenderVhost(t, gh1999Vhost("/index.php?mod=$uri&$args", false))
	if strings.Contains(out, "mod=$uri") {
		t.Error("front controller rendered on a domain without PHP")
	}
	if !strings.Contains(out, "try_files $uri $uri/ =404;") {
		t.Error("static domain lost its =404 fallback")
	}
}

func TestDomainCreateParams_DecodesPHPFallback(t *testing.T) {
	var p domainCreateParams
	if err := json.Unmarshal([]byte(`{"php_fallback":"/index.php?mod=$uri&$args"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.PHPFallback != "/index.php?mod=$uri&$args" {
		t.Errorf("PHPFallback = %q", p.PHPFallback)
	}
}
