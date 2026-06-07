package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
)

func TestMailSANHostnames_Order(t *testing.T) {
	got := mailSANHostnames("Example.COM")
	want := []string{"mail.example.com", "autoconfig.example.com", "autodiscover.example.com", "mta-sts.example.com"}
	assert.Equal(t, want, got, "order must be stable + lowercased")
}

func TestSSLMailIssue_RejectsEmptyDomain(t *testing.T) {
	raw, _ := json.Marshal(sslMailIssueParams{Domain: "  "})
	_, err := sslMailIssueHandler(context.Background(), raw)
	require.Error(t, err)
	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestSSLMailIssue_RejectsInvalidDomain(t *testing.T) {
	raw, _ := json.Marshal(sslMailIssueParams{Domain: "../etc/passwd"})
	_, err := sslMailIssueHandler(context.Background(), raw)
	require.Error(t, err)
	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestSSLMailIssue_RequiresPublicIPWhenNotSkipped(t *testing.T) {
	raw, _ := json.Marshal(sslMailIssueParams{Domain: "example.com"})
	_, err := sslMailIssueHandler(context.Background(), raw)
	require.Error(t, err)
	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestSSLMailIssue_SkipDNS_StillRequiresEmail(t *testing.T) {
	raw, _ := json.Marshal(sslMailIssueParams{
		Domain:  "example.com",
		SkipDNS: true,
	})
	_, err := sslMailIssueHandler(context.Background(), raw)
	require.Error(t, err)
	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestScanMailSANDNS_AllNonResolvableNoneMatch(t *testing.T) {
	// A bunch of guaranteed-NX hostnames. Even if a flaky network
	// returns some glue address, none of these can ever equal the
	// canary publicIP below, so no row may report Matches=true.
	sans := []string{
		"mail.this-domain-cannot-exist-jabali-test.invalid",
		"autoconfig.this-domain-cannot-exist-jabali-test.invalid",
		"autodiscover.this-domain-cannot-exist-jabali-test.invalid",
		"mta-sts.this-domain-cannot-exist-jabali-test.invalid",
	}
	dns := scanMailSANDNS(context.Background(), sans, "203.0.113.42")
	require.Len(t, dns, len(sans))
	for _, h := range sans {
		assert.False(t, dns[h].Matches, "%s must not match the canary IP", h)
	}
}

func TestSelectEffectiveSANs_MailOnlyWhenAuxPointElsewhere(t *testing.T) {
	// GH #132 shape: mail.<d> points at us, the three autoconfig
	// helpers point at a separate webmail host. The cert must drop
	// the non-matching aux names instead of bundling them.
	all := mailSANHostnames("example.com")
	dns := map[string]dnsScan{
		all[0]: {Hostname: all[0], Matches: true},
		all[1]: {Hostname: all[1], Matches: false},
		all[2]: {Hostname: all[2], Matches: false},
		all[3]: {Hostname: all[3], Matches: false},
	}
	got := selectEffectiveSANs(all, dns)
	assert.Equal(t, []string{"mail.example.com"}, got)
}

func TestSelectEffectiveSANs_AllMatchKeepsEverything(t *testing.T) {
	all := mailSANHostnames("example.com")
	dns := map[string]dnsScan{}
	for _, h := range all {
		dns[h] = dnsScan{Hostname: h, Matches: true}
	}
	got := selectEffectiveSANs(all, dns)
	assert.Equal(t, all, got, "all-resolving SANs must all be requested")
}

func TestSelectEffectiveSANs_PartialMatchKeepsOrder(t *testing.T) {
	all := mailSANHostnames("example.com")
	dns := map[string]dnsScan{
		all[0]: {Hostname: all[0], Matches: true},
		all[1]: {Hostname: all[1], Matches: false},
		all[2]: {Hostname: all[2], Matches: true},
		all[3]: {Hostname: all[3], Matches: false},
	}
	got := selectEffectiveSANs(all, dns)
	assert.Equal(t, []string{"mail.example.com", "autodiscover.example.com"}, got)
}
