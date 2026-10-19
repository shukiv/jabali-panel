package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/panelcert"
)

// ssl.panel.mail_selfsign keeps the panel's mail certificate,
// /etc/jabali/tls/panel-mail.{crt,key}, self-signed while Let's Encrypt is
// off for the panel (GH #2166): the mail certificate follows the "Use
// Let's Encrypt for the panel" toggle like the hostname one does
// (ssl.panel.selfsign).
//
// panelcert.CheckNames decides, as the panel does before dispatching:
//   - a live CA certificate (Let's Encrypt or a custom one) is kept until it
//     expires;
//   - a current self-signed certificate for the names is left alone, with
//     no reload and no Stalwart restart;
//   - anything else (missing, expired, other names) is replaced by a
//     self-signed certificate for the names, with its own key. The
//     hostname certificate's key is never shared with Stalwart.
//
// Webmail on the mail name serves the file after an nginx reload. Stalwart
// serves it on IMAPS and submission once it is pushed into its certificate
// registry and restarted, in the deploy hook's order (jabali-panel-cert.sh,
// kind=mail). Before this, a panel that never had a Let's Encrypt mail
// certificate left Stalwart on its rcgen fallback (SAN localhost), which
// mail clients reject by name.
//
// The certbot lineage record (panel-mail.lineage) is left alone: a later
// Let's Encrypt renewal of that lineage still deploys over this certificate.
type sslPanelMailSelfSignParams struct {
	// Names are the certificate's DNS names, the first its CommonName
	// (panelcert.MailNames).
	Names []string `json:"names"`
}

type sslPanelMailSelfSignResponse struct {
	Regenerated bool   `json:"regenerated"`
	Reason      string `json:"reason,omitempty"`
	CertPath    string `json:"cert_path"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// panelMailSelfSignMaxNames bounds the names: MailNames yields at most two.
const panelMailSelfSignMaxNames = 4

// Overridable in tests.
var (
	panelMailSelfSignCertPath = panelcert.DefaultMailCertPath
	panelMailSelfSignKeyPath  = "/etc/jabali/tls/panel-mail.key"
	// panelMailSelfSignReloadFn applies the new certificate to nginx and
	// Stalwart. Seam so unit tests don't spawn systemctl.
	panelMailSelfSignReloadFn = defaultPanelMailSelfSignReload
	// panelMailPushCertPath pushes panel-mail.{crt,key} into Stalwart.
	panelMailPushCertPath = "/usr/local/bin/jabali-stalwart-push-cert"
	// panelMailStalwartUser and panelMailStalwartBinary are what
	// jabali-stalwart.service runs: its User= and its ExecStart binary.
	panelMailStalwartUser   = "jabali-mail"
	panelMailStalwartBinary = "/usr/local/bin/stalwart"
)

// panelMailSelfSignMu serializes ssl.panel.mail_selfsign: the reconciler and
// the admin's "Regenerate self-signed" action can both dispatch it, and two
// runs at once could write one run's cert next to the other's key.
var panelMailSelfSignMu sync.Mutex

// defaultPanelMailSelfSignReload reloads nginx for webmail, then pushes the
// certificate into Stalwart and restarts it: Stalwart loads its certificates
// only at startup, so a push after the restart would sit unloaded. The
// context is detached from the caller's so a caller giving up can't leave
// the push done and the restart not.
//
// Stalwart is left alone on a box without the mail server (GH #2166).
func defaultPanelMailSelfSignReload(_ context.Context) {
	bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = execCommandContext(bg, "systemctl", "reload", "nginx").Run()
	if !panelMailStalwartInstalled() {
		return
	}
	_ = execCommandContext(bg, "systemctl", "start", "jabali-stalwart").Run()
	if _, err := os.Stat(panelMailPushCertPath); err == nil {
		_ = execCommandContext(bg, panelMailPushCertPath).Run()
	}
	_ = execCommandContext(bg, "systemctl", "restart", "jabali-stalwart").Run()
}

// panelMailStalwartInstalled reports whether this box has the mail server:
// the user jabali-stalwart.service runs as and its binary. The unit file
// alone proves nothing, since `jabali update` installs it on every box. On a
// box without the mail module, starting the unit fails at step USER, and
// each restart of the loop that follows sends a "Stalwart is down" alert.
func panelMailStalwartInstalled() bool {
	if _, err := user.Lookup(panelMailStalwartUser); err != nil {
		return false
	}
	_, err := os.Stat(panelMailStalwartBinary)
	return err == nil
}

func sslPanelMailSelfSignHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslPanelMailSelfSignParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	names, err := panelMailSelfSignNames(p.Names)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	panelMailSelfSignMu.Lock()
	defer panelMailSelfSignMu.Unlock()

	data, err := os.ReadFile(panelMailSelfSignCertPath)
	if err != nil {
		data = nil
	}
	switch v := panelcert.CheckNames(data, names[0], names, time.Now()); v.Action {
	case panelcert.KeepCA:
		return sslPanelMailSelfSignResponse{
			Reason:    "preserved: cert issued by " + v.Issuer,
			CertPath:  panelMailSelfSignCertPath,
			ExpiresAt: v.NotAfter.UTC().Format(time.RFC3339),
		}, nil
	case panelcert.UpToDate:
		return sslPanelMailSelfSignResponse{
			Reason:    v.Reason,
			CertPath:  panelMailSelfSignCertPath,
			ExpiresAt: v.NotAfter.UTC().Format(time.RFC3339),
		}, nil
	}

	expiresAt, err := generatePanelSelfSignedCert(names[0], names, nil, panelMailSelfSignCertPath, panelMailSelfSignKeyPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to generate panel mail self-signed cert: %v", err),
		}
	}
	// Ownership + mode as the deploy hook installs the Let's Encrypt mail
	// certificate: 0640 root:jabali for both files.
	applyPanelMailFileOwnership(panelMailSelfSignCertPath)
	applyPanelMailFileOwnership(panelMailSelfSignKeyPath)

	panelMailSelfSignReloadFn(ctx)

	return sslPanelMailSelfSignResponse{
		Regenerated: true,
		CertPath:    panelMailSelfSignCertPath,
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// panelMailSelfSignNames validates the requested names: one to
// panelMailSelfSignMaxNames hostnames, lowercased, without duplicates.
func panelMailSelfSignNames(in []string) ([]string, error) {
	if len(in) == 0 || len(in) > panelMailSelfSignMaxNames {
		return nil, fmt.Errorf("names: want 1 to %d hostnames, got %d", panelMailSelfSignMaxNames, len(in))
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, n := range in {
		n = strings.ToLower(strings.TrimSpace(n))
		if !sslDomainRegex.MatchString(n) {
			return nil, fmt.Errorf("invalid hostname %q", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate hostname %q", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// applyPanelMailFileOwnership sets path to 0640 root:jabali (best-effort:
// the jabali group may not exist in a container or CI).
func applyPanelMailFileOwnership(path string) {
	_ = os.Chmod(path, 0o640)
	grp, err := user.LookupGroup("jabali")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return
	}
	_ = os.Chown(path, 0, gid)
}

func init() {
	Default.Register("ssl.panel.mail_selfsign", sslPanelMailSelfSignHandler)
}
