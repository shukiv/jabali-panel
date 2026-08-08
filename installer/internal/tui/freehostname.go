package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Free-hostname flow in the installer TUI (JAB-213). A box with no domain can
// get a public <ip>.jabalihosted.com hostname + TLS. The installer itself does
// the register/claim HTTPS (the box's observed public IP becomes the label),
// writes the relay credential, and hands install.sh a normal JABALI_HOSTNAME —
// so no bash free-hostname path is exercised on the TUI install.

// fhAPI is the service base; overridable for tests via the env var.
func fhAPI() string {
	if v := strings.TrimSpace(os.Getenv("JABALI_HOSTNAME_API")); v != "" {
		return v
	}
	return "https://api.jabalihosted.com"
}

const fhTokenFile = "/etc/jabali-panel/hostname.env"

// fhStage is the sub-state of the hostname screen.
type fhStage int

const (
	fhChoice  fhStage = iota // pick free vs own
	fhEmail                  // enter email
	fhCode                   // enter the 6-digit code
	fhWorking                // an HTTP call is in flight
)

// fhModel holds the free-hostname screen state.
type fhModel struct {
	stage  fhStage
	choice int // 0 = free, 1 = own
	email  textinput.Model
	code   textinput.Model
	busy   string // what we're waiting on (for the spinner line)
	errMsg string
	fqdn   string // set on success
	token  string
	label  string
	// tokenFile is the path the credential is written to; a var for tests.
	tokenFile string
}

func newFHModel() fhModel {
	e := textinput.New()
	e.Placeholder = "you@example.com"
	e.Prompt = ""
	e.CharLimit = 254
	c := textinput.New()
	c.Placeholder = "123456"
	c.Prompt = ""
	c.CharLimit = 6
	return fhModel{stage: fhChoice, email: e, code: c, tokenFile: fhTokenFile}
}

// --- async messages ---

type fhCodeSentMsg struct{}
type fhClaimedMsg struct {
	fqdn, label, token string
}
type fhErrMsg struct{ msg string }

func (m fhModel) httpPost(path, body string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, fhAPI()+path, bytes.NewReader([]byte(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, out, nil
}

func (m fhModel) registerCmd(email string) tea.Cmd {
	return func() tea.Msg {
		status, body, err := m.httpPost("/v1/register", fmt.Sprintf(`{"email":%q}`, email))
		if err != nil {
			return fhErrMsg{"hostname service unreachable — pick 'Enter my own'"}
		}
		switch status {
		case 200:
			return fhCodeSentMsg{}
		case 429:
			return fhErrMsg{"a code was just sent — wait a minute"}
		default:
			return fhErrMsg{"could not send a code: " + fhField(body, "message")}
		}
	}
}

func (m fhModel) claimCmd(email, code string) tea.Cmd {
	return func() tea.Msg {
		status, body, err := m.httpPost("/v1/claim", fmt.Sprintf(`{"email":%q,"code":%q}`, email, code))
		if err != nil {
			return fhErrMsg{"hostname service unreachable"}
		}
		if status != 200 {
			reason := fhField(body, "message")
			if reason == "" {
				reason = fmt.Sprintf("HTTP %d", status)
			}
			return fhErrMsg{"claim failed: " + reason}
		}
		var r struct{ Fqdn, Label, Token string }
		if json.Unmarshal(body, &r) != nil || r.Fqdn == "" || r.Token == "" {
			return fhErrMsg{"malformed claim response"}
		}
		return fhClaimedMsg{fqdn: r.Fqdn, label: r.Label, token: r.Token}
	}
}

// Charset allowlists for every value written into hostname.env — see
// writeCredential.
var (
	fhTokenRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	fhLabelRegex = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
	fhFQDNRegex  = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
	fhEmailRegex = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$`)
)

// writeCredential persists the token 0600 so install.sh's slice step enables
// the heartbeat and the cert issues. Best-effort; the hostname still works.
func (m fhModel) writeCredential() error {
	// Validate before persisting: hostname.env is read by root-run scripts
	// (heartbeat timer, certbot renew hooks), so a value carrying shell
	// metacharacters or a newline — from a compromised or MITM'd hosted
	// service — must never reach the file. Reject rather than sanitize.
	if !fhTokenRegex.MatchString(m.token) {
		return fmt.Errorf("claim response token has an unexpected format")
	}
	if !fhLabelRegex.MatchString(m.label) {
		return fmt.Errorf("claim response label has an unexpected format")
	}
	if !fhFQDNRegex.MatchString(m.fqdn) {
		return fmt.Errorf("claim response fqdn has an unexpected format")
	}
	if !fhEmailRegex.MatchString(m.email.Value()) {
		return fmt.Errorf("email has an unexpected format")
	}
	content := fmt.Sprintf("LABEL=%s\nFQDN=%s\nEMAIL=%s\nTOKEN=%s\nAPI=%s\n",
		m.label, m.fqdn, m.email.Value(), m.token, fhAPI())
	if err := os.MkdirAll(filepath.Dir(m.tokenFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.tokenFile, []byte(content), 0o600)
}

func fhField(body []byte, key string) string {
	var mp map[string]any
	if json.Unmarshal(body, &mp) != nil {
		return ""
	}
	if v, ok := mp[key].(string); ok {
		return v
	}
	return ""
}
