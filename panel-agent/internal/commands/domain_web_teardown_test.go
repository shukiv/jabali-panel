package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDomainWebTeardown_InvalidDomain(t *testing.T) {
	t.Parallel()
	for _, d := range []string{"Example.COM", "-example.com", "example-.com", "localhost", "example", ""} {
		params, _ := json.Marshal(domainWebTeardownParams{Domain: d})
		_, err := domainWebTeardownHandler(context.Background(), params)
		require.NotNil(t, err, "domain %q must be rejected", d)
		var aerr *agentwire.AgentError
		require.ErrorAs(t, err, &aerr)
		assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
	}
}

// The web teardown removes the WEB TLS material for the exact name while leaving
// the mail.<domain> lineage intact — that facet separation is the whole point of
// GH #1603 (delete the Web Domain, keep the Mail Domain). Assert on the
// self-signed dirs, which are removed by a deterministic os.RemoveAll (the
// certbot lineage path depends on certbot being on PATH, so it is not asserted).
func TestDomainWebTeardown_RemovesWebCertKeepsMail(t *testing.T) {
	selfDir := t.TempDir()
	leRoot := t.TempDir()
	prevSelf, prevLE := baseSelfSignDir, sslLERoot
	baseSelfSignDir, sslLERoot = selfDir, leRoot
	t.Cleanup(func() { baseSelfSignDir, sslLERoot = prevSelf, prevLE })

	const dom = "example.com"
	webCert := filepath.Join(selfDir, dom)
	mailCert := filepath.Join(selfDir, "mail."+dom)
	require.NoError(t, os.MkdirAll(webCert, 0o755))
	require.NoError(t, os.MkdirAll(mailCert, 0o755))

	params, _ := json.Marshal(domainWebTeardownParams{Domain: dom})
	out, err := domainWebTeardownHandler(context.Background(), params)
	require.NoError(t, err)
	resp, ok := out.(domainWebTeardownResponse)
	require.True(t, ok)
	assert.True(t, resp.TornDown)
	assert.Equal(t, dom, resp.Domain)

	assert.NoDirExists(t, webCert, "web self-signed cert must be removed")
	assert.DirExists(t, mailCert, "mail self-signed cert must be KEPT (facet-preserving)")
}
