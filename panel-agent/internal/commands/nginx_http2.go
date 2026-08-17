package commands

import (
	"regexp"
	"strconv"
)

// HTTP/2 syntax differs by nginx version and jabali supports both sides of the
// 1.25.1 boundary (Debian bookworm 1.22 / Ubuntu jammy 1.18 / noble 1.24 are
// all <1.25.1; trixie 1.26 is newer). On <1.25.1 HTTP/2 MUST be the
// `listen ... ssl http2;` parameter — the standalone `http2 on;` directive is an
// "unknown directive" that fails `nginx -t` and rejects every reload (scar
// 2026-05-15, ADR-0077). On >=1.25.1 `listen ... http2` is merely deprecated and
// logs a warning every reload (GH #292), and `http2 on;` is the correct form.
//
// nginxHTTP2Form carries the two pieces a template needs so exactly one of them
// expresses HTTP/2 for the host's actual nginx:
//   - Param:     appended to each `listen ... ssl` line (" http2" on old, "" on new)
//   - Directive: a once-per-server-block line ("" on old, "http2 on;" on new)
type nginxHTTP2Form struct {
	Param     string
	Directive string
}

// nginxHTTP2 returns the host's HTTP/2 form by probing the live nginx version.
// Not cached: a vhost render is never hot-path, and re-probing every time keeps
// the result correct when nginx is installed after the agent starts (fresh
// install ordering) or upgraded across the 1.25.1 boundary without an agent
// restart. When nginx is absent the result is the safe portable param form.
func nginxHTTP2() nginxHTTP2Form {
	// `nginx -v` prints "nginx version: nginx/1.26.0" to stderr.
	out, _ := execCommand("nginx", "-v").CombinedOutput()
	return computeNginxHTTP2Form(string(out))
}

var nginxHTTP2VerRe = regexp.MustCompile(`nginx/(\d+)\.(\d+)\.(\d+)`)

// computeNginxHTTP2Form is the pure decision (testable). An unparseable version
// is treated as old → the portable `listen ... http2` parameter, which is valid
// on every nginx (it only warns on >=1.25.1) — never the directive that would
// hard-fail an older nginx we couldn't identify.
func computeNginxHTTP2Form(nginxVOutput string) nginxHTTP2Form {
	if nginxAtLeast1251(nginxVOutput) {
		return nginxHTTP2Form{Param: "", Directive: "http2 on;"}
	}
	return nginxHTTP2Form{Param: " http2", Directive: ""}
}

func nginxAtLeast1251(nginxVOutput string) bool {
	m := nginxHTTP2VerRe.FindStringSubmatch(nginxVOutput)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	if maj != 1 {
		return maj > 1
	}
	if min != 25 {
		return min > 25
	}
	return pat >= 1
}
