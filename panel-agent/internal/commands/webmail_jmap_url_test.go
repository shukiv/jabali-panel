package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// JAB-390: at a shared-mail-hostname switchover the engine points Bulwark's
// JMAP_SERVER_URL at the new panel mail hostname (it must equal the name
// every webmail vhost's sub_filter rewrites) and restarts jabali-webmail.
// install.sh renders the same value from the DB (#1881), so the two agree.

const jmapEnvFixture = "HOSTNAME=127.0.0.1\n# comment\nJMAP_SERVER_URL=https://mail.mx.jabali-panel.com\nALLOW_CUSTOM_JMAP_ENDPOINT=true\nLOGIN_SHOW_TOTP=false\n"

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
	want := "HOSTNAME=127.0.0.1\n# comment\nJMAP_SERVER_URL=https://mx.example.net\nALLOW_CUSTOM_JMAP_ENDPOINT=true\nLOGIN_SHOW_TOTP=false\n"
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
	if string(got) != "HOSTNAME=127.0.0.1\nPORT=3000\nJMAP_SERVER_URL=https://mx.example.net\n" {
		t.Errorf("bulwark.env:\n%s", got)
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
