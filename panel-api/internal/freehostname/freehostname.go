// Package freehostname is the panel side of the free jabalihosted.com
// hostname service (JAB-213): the credential a box stores when it claims a
// name, and the calls the panel makes to the service with it.
package freehostname

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// EnvPath is the box credential written at claim time (TUI installer,
// install.sh, or the agent's hostname.free.apply): LABEL, FQDN, EMAIL, TOKEN
// and API lines, 0600 root. A var so tests can redirect it.
var EnvPath = "/etc/jabali-panel/hostname.env"

// ErrNoCredential: this server has not claimed a free hostname.
var ErrNoCredential = errors.New("freehostname: this server has no free hostname")

// Credential is the parsed, validated credential. The token is a bearer
// secret: never log it or put it in an error.
type Credential struct {
	Label string
	FQDN  string
	Token string
	API   string
}

var (
	fqdnRe  = regexp.MustCompile(`^([a-z0-9-]{1,63})\.` + regexp.QuoteMeta(models.FreeHostnameBase) + `$`)
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

// ReadCredential reads the credential at path WITHOUT executing it — the
// file holds values the hosted service returned, and its readers run as
// root — taking the first line for each key, as the shell readers do.
// Every value is checked before use; ErrNoCredential when the file is absent.
func ReadCredential(path string) (*Credential, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, fmt.Errorf("freehostname: read %s: %w", path, err)
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		if _, seen := vals[k]; !seen {
			vals[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("freehostname: read %s: %w", path, err)
	}

	m := fqdnRe.FindStringSubmatch(vals["FQDN"])
	if m == nil {
		return nil, fmt.Errorf("freehostname: %s: FQDN is not a <label>.%s name", path, models.FreeHostnameBase)
	}
	if !tokenRe.MatchString(vals["TOKEN"]) {
		return nil, fmt.Errorf("freehostname: %s: TOKEN missing or malformed", path)
	}
	api := vals["API"]
	u, err := url.Parse(api)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(api, " \t\r\n") {
		return nil, fmt.Errorf("freehostname: %s: API must be an https URL", path)
	}
	return &Credential{Label: m[1], FQDN: vals["FQDN"], Token: vals["TOKEN"], API: strings.TrimRight(api, "/")}, nil
}

// HTTPClient returns the client for every call to the service. It dials
// IPv4 only: the service derives a label from the caller's IPv4 address and
// refuses an IPv6 caller, and its API name also answers AAAA, so a
// dual-stack box would otherwise reach it over IPv6. Proxy, TLS and pool
// settings are http.DefaultTransport's; TLS verification stays on.
func HTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp4", addr)
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: t}
}

// PresentChallenge adds an ACME DNS-01 value at _acme-challenge.<label>.
// The service resolves the label from the token alone, so a box can only
// ever write under its own name. Values are added, not replaced: an apex +
// wildcard pair needs two at once.
func (c *Credential) PresentChallenge(ctx context.Context, hc *http.Client, txt string) error {
	return c.post(ctx, hc, "/v1/acme/present", map[string]string{"token": c.Token, "txt": txt})
}

// ClearChallenge removes every challenge value under the box's label.
func (c *Credential) ClearChallenge(ctx context.Context, hc *http.Client) error {
	return c.post(ctx, hc, "/v1/acme/cleanup", map[string]string{"token": c.Token})
}

func (c *Credential) post(ctx context.Context, hc *http.Client, path string, in any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.API+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("hostname service %s: %w", path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(out, &e)
	reason := e.Message
	if reason == "" {
		reason = e.Error
	}
	if reason == "" {
		reason = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("hostname service %s: HTTP %d: %s", path, resp.StatusCode, reason)
}
