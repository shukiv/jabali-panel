package api

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-72: nginx rewrite patterns are rendered verbatim into the vhost and run
// under PCRE (backtracking). Validate length, reject nested-quantifier ReDoS
// shapes, and reject grossly invalid regex — with a stricter cap for tenants.
func TestValidateRewritePattern(t *testing.T) {
	redos := []string{"(a+)+", "(.*)*", `(\d+)*`, "(ab+)+", "(x*)* "}
	for _, p := range redos {
		if err := validateRewritePattern(p, 256); err == nil {
			t.Errorf("expected ReDoS pattern %q to be rejected", p)
		}
	}

	if err := validateRewritePattern("([unclosed", 256); err == nil {
		t.Errorf("expected invalid regex to be rejected")
	}

	ok := []string{"^/old/(.*)$", `^/foo/(\d+)/bar$`, "^/a/b/c$", "index\\.php"}
	for _, p := range ok {
		if err := validateRewritePattern(p, 256); err != nil {
			t.Errorf("expected %q to be allowed, got %v", p, err)
		}
	}

	// Length cap: a pattern between the tenant (128) and admin (256) caps is OK
	// for admin but rejected for a tenant.
	mid := "^/" + strings.Repeat("a", 200) + "$" // ~203 chars
	if err := validateRewritePattern(mid, 256); err != nil {
		t.Errorf("203-char pattern should pass admin cap, got %v", err)
	}
	if err := validateRewritePattern(mid, 128); err == nil {
		t.Errorf("203-char pattern should fail the tenant cap")
	}
}

// GH #1652: nginx runs the rewrite MATCH under PCRE, which supports lookahead
// assertions that Go's RE2 rejects. Front-controller apps (DokuWiki, WordPress)
// use negative lookahead to exclude asset directories, e.g.
// ^/(?!lib/)(?!_media/)(?!_detail/)(?!_export/)(.*)$ — the validator must accept
// these while still rejecting genuinely malformed patterns and catastrophic
// (nested-quantifier / backreference) constructs.
func TestValidateRewritePattern_Lookahead(t *testing.T) {
	// johnnyq's exact DokuWiki pattern from the issue — negative lookahead.
	doku := `^/(?!lib/)(?!_media/)(?!_detail/)(?!_export/)(.*)$`
	if err := validateRewritePattern(doku, 256); err != nil {
		t.Errorf("DokuWiki negative-lookahead pattern should be allowed, got %v", err)
	}
	if err := validateRewritePattern(doku, 128); err != nil {
		t.Errorf("DokuWiki negative-lookahead pattern should pass the tenant cap, got %v", err)
	}

	// Positive lookahead is admitted too.
	if err := validateRewritePattern(`^/(?=foo)bar$`, 256); err != nil {
		t.Errorf("positive-lookahead pattern should be allowed, got %v", err)
	}

	// The tail is still syntax-checked: a lookahead that hides an unbalanced
	// group is rejected (this is what a bare RE2 accept-on-lookahead would mask).
	if err := validateRewritePattern(`^/(?!lib/)(unbalanced`, 256); err == nil {
		t.Errorf("unbalanced group after a lookahead must still be rejected")
	}

	// PCRE-only constructs that are NOT lookahead stay rejected: backreferences
	// (the genuinely exponential case), lookbehind, recursion, bad repetition.
	stillRejected := []string{
		`^(a)\1$`,     // backreference
		`(?<=x)y`,     // lookbehind
		`(?<!x)y`,     // negative lookbehind
		`(?R)`,        // recursion
		`a{2,3}{4,5}`, // invalid nested repetition
	}
	for _, p := range stillRejected {
		if err := validateRewritePattern(p, 256); err == nil {
			t.Errorf("expected %q to be rejected", p)
		}
	}

	// The ReDoS heuristic still fires even when a lookahead is present.
	if err := validateRewritePattern(`^/(?!x)(a+)+$`, 256); err == nil {
		t.Errorf("nested-quantifier ReDoS shape must still be rejected")
	}
}

// End-to-end: johnnyq's full DokuWiki rule (lookahead pattern + local
// replacement + last flag) passes the tenant-safe validator — the door that
// was returning "rule N: rewrite pattern is not valid regex".
func TestValidateTenantNginxRules_LookaheadRewrite(t *testing.T) {
	rules := models.NginxRules{
		{
			Type:        "rewrite",
			Pattern:     `^/(?!lib/)(?!_media/)(?!_detail/)(?!_export/)(.*)$`,
			Replacement: "/doku.php?id=$1",
			Flag:        "last",
		},
	}
	if err := validateTenantNginxRules(rules); err != nil {
		t.Errorf("DokuWiki lookahead rewrite rule should pass tenant validation, got %v", err)
	}
}
