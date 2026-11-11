package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// domainEmailEnableParams is the request shape for domain.email_enable.
//
// Called when the panel flips domains.email_enabled from 0 -> 1. The agent:
//  1. Generates an Ed25519 DKIM keypair for the domain if one doesn't
//     already exist at /etc/jabali-panel/dkim/<domain>.key.
//  2. Enables + starts jabali-stalwart + jabali-webmail (idempotent
//     on re-runs — they may already be active from a prior domain).
//  3. Reloads Stalwart so the SQL directory picks up email_enabled = 1.
//  4. Gives the Stalwart domain its Ed25519 and RSA DKIM signatures
//     (ensureDomainDKIM).
//  5. Returns both DKIM public keys so the panel can publish the
//     jabali._domainkey and jabalirsa._domainkey TXT records.
type domainEmailEnableParams struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
}

// domainEmailEnableResponse carries the DKIM material the panel needs to
// publish via PowerDNS (M4). The panel also persists dkim_selector +
// dkim_public_key to the domains row so DNS re-publication after a
// backup restore doesn't require regenerating the key.
type domainEmailEnableResponse struct {
	Ok            bool   `json:"ok"`
	DKIMSelector  string `json:"dkim_selector"`
	DKIMPublicKey string `json:"dkim_public_key"`

	// The RSA key signing alongside the Ed25519 one (GH #2475). The public
	// key is "" when the domain's RSA signature exists but its key file
	// can't be read; the panel then keeps the key it already published.
	DKIMRSASelector  string `json:"dkim_rsa_selector"`
	DKIMRSAPublicKey string `json:"dkim_rsa_public_key"`
}

// dkimSelector is a single hardcoded selector for v1 per ADR-0043.
// Rotation in M6.1 rolls to `jabali-YYYY-MM` but v1 is one-shot.
const dkimSelector = "jabali"

// dkimKeyDir is the per-domain key storage path (ADR-0043). The panel
// runs as `jabali`; the directory is 0750 jabali:jabali. Stalwart is
// granted read access via supplementary group membership.
const dkimKeyDir = "/etc/jabali-panel/dkim"

// dkimKeyDirFunc is overridable for tests. `runSystemctl` is shared with
// the php_ext handlers (defined in php_ext_shell.go) — reused directly
// so all systemctl invocations share one test-swappable seam.
var dkimKeyDirFunc = func() string {
	if d := os.Getenv("JABALI_DKIM_KEY_DIR"); d != "" {
		return d
	}
	return dkimKeyDir
}

func domainEmailEnableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p domainEmailEnableParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.DomainID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain_id required"}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}

	// Generate or reuse the DKIM key for this domain. Reuse is the
	// common path on a re-enable after disable — the private key stays
	// on disk (ADR-0043) so we don't rotate on every toggle.
	keyPath := filepath.Join(dkimKeyDirFunc(), p.DomainName+".key")
	publicTXT, err := ensureDKIMKey(keyPath)
	if err != nil {
		return nil, err
	}

	// Enable + start the mail stack. Idempotent: `systemctl enable --now`
	// is a no-op if the unit is already active + enabled.
	if out, err := runSystemctl(ctx, "enable", "--now", "jabali-stalwart.service"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl enable jabali-stalwart: %v (%s)", err, bytesTrim(out)),
		}
	}
	if out, err := runSystemctl(ctx, "enable", "--now", "jabali-webmail.service"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl enable jabali-webmail: %v (%s)", err, bytesTrim(out)),
		}
	}

	// Reload Stalwart so it re-reads any config picked up from disk.
	// A SIGHUP-equivalent reload is enough; we don't stop any unit.
	if out, err := runSystemctl(ctx, "reload", "jabali-stalwart.service"); err != nil {
		if !isReloadNotSupportedErr(out) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("systemctl reload jabali-stalwart: %v (%s)", err, bytesTrim(out)),
			}
		}
	}

	// Seed Stalwart's registry with a Domain and its DKIM signatures so
	// inbound SMTP is routed for this domain and outbound is signed.
	//
	// On a re-enable after disable (domain.email_disable destroyed the
	// Domain) the registry has no record and both are created. A Domain that
	// already exists is kept, but it still gets any signature it lacks: a
	// Domain created by another path (account registry sync) has none, and
	// one from before GH #2475 has the Ed25519 signature only. The panel DB
	// is the source of truth for whether email SHOULD be enabled; the
	// registry is Stalwart's synchronized copy.
	domainID, err := domainIDByName(ctx, p.DomainName)
	if err != nil {
		return nil, err
	}
	if domainID == "" {
		if domainID, err = createDomain(ctx, p.DomainName); err != nil {
			return nil, err
		}
	}
	sigs, err := ensureDomainDKIM(ctx, p.DomainName, domainID, dkimPublishedKeys{})
	if err != nil {
		return nil, err
	}

	return domainEmailEnableResponse{
		Ok:               true,
		DKIMSelector:     dkimSelector,
		DKIMPublicKey:    publicTXT,
		DKIMRSASelector:  dkimRSASelector,
		DKIMRSAPublicKey: sigs.RSAPublicKey,
	}, nil
}

// ensureDKIMKey returns the DNS TXT value for the domain's DKIM key.
// If the key file already exists, reads + derives the public key from
// the stored seed. If it's missing, generates a fresh keypair, writes
// the private form atomically, and returns the public TXT value.
//
// The reason for read-back on already-existing keys: Stalwart reads the
// disk key on every signature, so re-deriving the public form from the
// stored seed is the only way to guarantee the DNS record we return
// matches what Stalwart is actually signing with.
func ensureDKIMKey(keyPath string) (string, error) {
	if _, err := os.Stat(keyPath); err == nil {
		seed, err := dkim.LoadEd25519(keyPath)
		if err != nil {
			return "", &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("load existing DKIM key at %s: %v", keyPath, err),
			}
		}
		// Re-derive the TXT value from the seed without regenerating —
		// one canonical public-key formatter lives in internal/dkim.
		txt, err := dkim.PublicDKIMTxtFromSeed(seed)
		if err != nil {
			return "", &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("derive DKIM TXT from existing key at %s: %v", keyPath, err),
			}
		}
		return string(txt), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stat DKIM key at %s: %v", keyPath, err),
		}
	}

	// Generate fresh.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir DKIM dir: %v", err),
		}
	}
	kp, err := dkim.GenerateEd25519()
	if err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("generate DKIM key: %v", err),
		}
	}
	if err := dkim.WritePrivate(keyPath, kp.PrivateRawBase64); err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write DKIM key: %v", err),
		}
	}
	return string(kp.PublicDKIMTxt), nil
}

// validateDomainNameForShell rejects domain names containing characters
// that would turn the DKIM keyfile path or a systemd unit arg into an
// exec injection. Panel-side internal/mailaddr already validates; this
// is defence in depth on the agent side.
func validateDomainNameForShell(name string) error {
	if name == "" {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain_name required"}
	}
	if strings.ContainsAny(name, " \t\n\r;&|<>`$\\(){}'\"!*?[]/") {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "shell metacharacter in domain_name"}
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain_name starts with '.' or '-'"}
	}
	return nil
}

func bytesTrim(b []byte) string {
	s := string(b)
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// isReloadNotSupportedErr detects the "Job type reload is not applicable"
// systemctl error. We treat that as non-fatal because Stalwart's unit
// may not declare ExecReload — the newly-enabled domain will still be
// picked up on the next SQL-directory cache miss (< 60s).
func isReloadNotSupportedErr(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "reload is not applicable") ||
		strings.Contains(s, "Refusing to reload")
}

func init() {
	Default.Register("domain.email_enable", domainEmailEnableHandler)
}
