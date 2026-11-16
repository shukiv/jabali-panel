package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// JAB-390: at a shared-mail-hostname switchover the engine points Bulwark's
// JMAP_SERVER_URL at the new panel mail hostname (it must equal the name
// every webmail vhost's sub_filter rewrites) and restarts jabali-webmail.
// install.sh renders the same value from the DB (#1881), so the two agree.

const jmapEnvFixture = "HOSTNAME=127.0.0.1\n# comment\nJMAP_SERVER_URL=https://mail.mx.jabali-panel.com\nALLOW_CUSTOM_JMAP_ENDPOINT=true\n" +
	`JMAP_SERVERS="[{\"id\":\"jabali\",\"label\":\"Mail\",\"url\":\"https://mail.mx.jabali-panel.com\"}]"` + "\nLOGIN_SHOW_TOTP=false\n"

func wireJMAPEnv(t *testing.T, content *string) (string, *int, *error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bulwark.env")
	if content != nil {
		if err := os.WriteFile(path, []byte(*content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	origFile, origCtl := webmailJMAPEnvFile, runSystemctl
	webmailJMAPEnvFile = path
	restarts := 0
	var restartErr error
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 2 && args[0] == "restart" && args[1] == "jabali-webmail.service" {
			restarts++
			return nil, restartErr
		}
		t.Errorf("unexpected systemctl %v", args)
		return nil, nil
	}
	t.Cleanup(func() { webmailJMAPEnvFile, runSystemctl = origFile, origCtl })
	return path, &restarts, &restartErr
}

func applyJMAPURL(t *testing.T, host string) (webmailJMAPURLApplyResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(webmailJMAPURLApplyParams{MailHostname: host})
	out, err := webmailJMAPURLApplyHandler(context.Background(), raw)
	if err != nil {
		return webmailJMAPURLApplyResponse{}, err
	}
	return out.(webmailJMAPURLApplyResponse), nil
}

func TestWebmailJMAPURLApply_RewritesInPlaceAndRestarts(t *testing.T) {
	c := jmapEnvFixture
	path, restarts, _ := wireJMAPEnv(t, &c)

	resp, err := applyJMAPURL(t, "MX.Example.NET")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "HOSTNAME=127.0.0.1\n# comment\nJMAP_SERVER_URL=https://mx.example.net\nALLOW_CUSTOM_JMAP_ENDPOINT=true\n" +
		`JMAP_SERVERS="[{\"id\":\"jabali\",\"label\":\"Mail\",\"url\":\"https://mx.example.net\"}]"` + "\nLOGIN_SHOW_TOTP=false\n"
	if string(got) != want {
		t.Errorf("bulwark.env:\n%s\nwant:\n%s", got, want)
	}
	if !resp.Changed || *restarts != 1 {
		t.Errorf("changed=%v restarts=%d, want true/1", resp.Changed, *restarts)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 kept", fi.Mode().Perm())
	}

	// Idempotent: the same name again writes nothing and restarts nothing.
	resp, err = applyJMAPURL(t, "mx.example.net")
	if err != nil || resp.Changed || *restarts != 1 {
		t.Errorf("second apply: err=%v changed=%v restarts=%d, want nil/false/1", err, resp.Changed, *restarts)
	}
}

func TestWebmailJMAPURLApply_AppendsMissingKey(t *testing.T) {
	c := "HOSTNAME=127.0.0.1\nPORT=3000"
	path, _, _ := wireJMAPEnv(t, &c)
	if _, err := applyJMAPURL(t, "mx.example.net"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "HOSTNAME=127.0.0.1\nPORT=3000\nJMAP_SERVER_URL=https://mx.example.net\n" +
		`JMAP_SERVERS="[{\"id\":\"jabali\",\"label\":\"Mail\",\"url\":\"https://mx.example.net\"}]"` + "\n"
	if string(got) != want {
		t.Errorf("bulwark.env:\n%s\nwant:\n%s", got, want)
	}
}

// GH #2518: an env file rendered before the sign-in server list existed has
// JMAP_SERVER_URL only; the switchover adds the list after it.
func TestWebmailJMAPURLApply_AddsTheServerListToAnOlderFile(t *testing.T) {
	c := "HOSTNAME=127.0.0.1\nJMAP_SERVER_URL=https://mail.old.example\nLOGIN_SHOW_TOTP=false\n"
	path, restarts, _ := wireJMAPEnv(t, &c)
	if _, err := applyJMAPURL(t, "mx.example.net"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "HOSTNAME=127.0.0.1\nJMAP_SERVER_URL=https://mx.example.net\nLOGIN_SHOW_TOTP=false\n" +
		`JMAP_SERVERS="[{\"id\":\"jabali\",\"label\":\"Mail\",\"url\":\"https://mx.example.net\"}]"` + "\n"
	if string(got) != want || *restarts != 1 {
		t.Errorf("bulwark.env (restarts %d):\n%s\nwant:\n%s", *restarts, got, want)
	}
}

// GH #2518: install.sh renders the template's JMAP_SERVERS line and the
// switchover writes jmapServersLine; both must give the same line, or a
// switchover then `jabali update` would restart webmail for nothing. The
// value, unquoted as systemd's EnvironmentFile does, must be a list Bulwark
// keeps: one entry with an id it accepts, at JMAP_SERVER_URL.
func TestJMAPServersLine_MatchesTheTemplateAndBulwarkTakesIt(t *testing.T) {
	raw, err := os.ReadFile("../../../install/bulwark/bulwark.env.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	const host = "mail.example.net"
	// install.sh's _install_bulwark_env renders with sed, as here.
	rendered := strings.ReplaceAll(string(raw), "${JABALI_MAIL_HOSTNAME}", host)
	var tmplLine, urlLine string
	for _, line := range strings.Split(rendered, "\n") {
		switch {
		case strings.HasPrefix(line, jmapServersKey):
			tmplLine = line
		case strings.HasPrefix(line, jmapServerURLKey):
			urlLine = line
		}
	}
	if tmplLine == "" {
		t.Fatal("the template has no JMAP_SERVERS line: the login page shows a JMAP Server field again")
	}
	if got := jmapServersLine(host); got != tmplLine {
		t.Errorf("jmapServersLine = %s\ntemplate renders  %s", got, tmplLine)
	}

	value := strings.TrimPrefix(tmplLine, jmapServersKey)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		t.Fatalf("JMAP_SERVERS is not one double-quoted value: %s", value)
	}
	unquoted := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\$`, `$`, "\\`", "`").Replace(value[1 : len(value)-1])
	var servers []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal([]byte(unquoted), &servers); err != nil {
		t.Fatalf("JMAP_SERVERS is not JSON after systemd's unquoting: %v\n%s", err, unquoted)
	}
	// Bulwark (lib/admin/jmap-servers.ts) drops an entry without a label, an
	// http(s) URL, or an id outside ^[a-z0-9][a-z0-9_-]{0,63}$; a list of more
	// than one shows a server picker.
	idOK := regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_-]{0,63}$`)
	if len(servers) != 1 || !idOK.MatchString(servers[0].ID) || servers[0].Label == "" {
		t.Fatalf("JMAP_SERVERS = %+v, want one entry Bulwark keeps", servers)
	}
	if want := strings.TrimPrefix(urlLine, jmapServerURLKey); servers[0].URL != want {
		t.Errorf("the server list's URL is %q, JMAP_SERVER_URL is %q: the sub_filter rewrites only the panel mail hostname", servers[0].URL, want)
	}
}

func TestWebmailJMAPURLApply_NoWebmailInstalledIsANoop(t *testing.T) {
	_, restarts, _ := wireJMAPEnv(t, nil)
	resp, err := applyJMAPURL(t, "mx.example.net")
	if err != nil || resp.Changed || *restarts != 0 {
		t.Errorf("err=%v changed=%v restarts=%d, want nil/false/0", err, resp.Changed, *restarts)
	}
}

func TestWebmailJMAPURLApply_RestartFailureIsReported(t *testing.T) {
	c := jmapEnvFixture
	_, _, restartErr := wireJMAPEnv(t, &c)
	*restartErr = errors.New("unit failed")
	if _, err := applyJMAPURL(t, "mx.example.net"); err == nil {
		t.Error("a failed jabali-webmail restart must be reported so the caller retries")
	}
}

func TestWebmailJMAPURLApply_RefusesUnsafeNames(t *testing.T) {
	for _, h := range []string{
		"",
		"mx.example.net\nNODE_TLS_REJECT_UNAUTHORIZED=1",
		"mx.example.net/evil",
		"https://mx.example.net",
		"mx.example.net evil",
		"localhost",
		"*.example.net",
	} {
		c := jmapEnvFixture
		path, restarts, _ := wireJMAPEnv(t, &c)
		_, err := applyJMAPURL(t, h)
		assertInvalidArgument(t, err, "mail_hostname "+h)
		if got, _ := os.ReadFile(path); string(got) != jmapEnvFixture || *restarts != 0 {
			t.Errorf("%q: a refused name must leave bulwark.env and the service alone", h)
		}
	}
}
