package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// webmailRedirectFixture is jabali-default.conf as install.sh's
// install_nginx_default_vhost writes it: the :80 redirect, the :443 default
// block with the multi-line /webmail redirects, and the GH#135 landing vhost
// with the one-line ones. Only the four /webmail targets may change.
func webmailRedirectFixture(mailHost string) string {
	return `server {
    listen 80 default_server;
    server_name _;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl default_server;
    server_name _;
    include /etc/nginx/sites-available/includes/phpmyadmin.conf;

    location = /webmail {
        return 301 https://` + mailHost + `/;
    }
    location = /webmail/ {
        return 301 https://` + mailHost + `/;
    }

    location / {
        include /etc/nginx/jabali-catchall.conf;
    }
}

server {
    listen 443 ssl;
    server_name panel.example.com;
    root  /var/www/panel.example.com;
    include /etc/nginx/sites-available/includes/phpmyadmin.conf;
    location = /webmail  { return 301 https://` + mailHost + `/; }
    location = /webmail/ { return 301 https://` + mailHost + `/; }
    location /old { return 301 https://legacy.example.com/; }
}
`
}

func withWebmailRedirectVhost(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jabali-default.conf")
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := webmailRedirectVhostPath
	webmailRedirectVhostPath = path
	t.Cleanup(func() { webmailRedirectVhostPath = prev })
	return path
}

// recordRedirectExec records every command the verb runs; fail names the command
// (by its first word) that exits non-zero.
func recordRedirectExec(t *testing.T, fail string) *[]string {
	t.Helper()
	var ran []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		if name == fail {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &ran
}

func callWebmailRedirect(t *testing.T, host string) (nginxWebmailRedirectResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"mail_hostname": host})
	out, err := nginxWebmailRedirectApplyHandler(context.Background(), raw)
	if err != nil {
		return nginxWebmailRedirectResponse{}, err
	}
	b, _ := json.Marshal(out)
	var resp nginxWebmailRedirectResponse
	if uerr := json.Unmarshal(b, &resp); uerr != nil {
		t.Fatal(uerr)
	}
	return resp, nil
}

// JAB-390: after a switchover the /webmail redirects in jabali-default.conf
// must follow the new panel mail hostname without waiting for the next
// `jabali update` to re-render the file.
func TestNginxWebmailRedirect_RewritesTheFourTargets(t *testing.T) {
	path := withWebmailRedirectVhost(t, webmailRedirectFixture("mail.panel.example.com"))
	ran := recordRedirectExec(t, "")

	resp, err := callWebmailRedirect(t, "Mail.Example.NET")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Rewritten || resp.Replacements != 4 {
		t.Fatalf("resp = %+v, want rewritten with 4 replacements", resp)
	}
	got, _ := os.ReadFile(path)
	if want := webmailRedirectFixture("mail.example.net"); string(got) != want {
		t.Fatalf("only the four /webmail targets may change; got:\n%s", got)
	}
	if strings.Join(*ran, "|") != "nginx -t|systemctl reload nginx" {
		t.Fatalf("commands = %v, want nginx -t then reload", *ran)
	}
}

func TestNginxWebmailRedirect_NoChurnWhenCurrent(t *testing.T) {
	path := withWebmailRedirectVhost(t, webmailRedirectFixture("mail.example.net"))
	before, _ := os.ReadFile(path)
	ran := recordRedirectExec(t, "")

	resp, err := callWebmailRedirect(t, "mail.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rewritten {
		t.Fatalf("resp = %+v, want no rewrite", resp)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) || len(*ran) != 0 {
		t.Fatalf("a current file must be left alone with no reload; commands = %v", *ran)
	}
}

// A box without the default vhost, or one whose default vhost has no
// /webmail redirects, has nothing to keep current; that is not an error, so
// the reconciler does not warn every tick.
func TestNginxWebmailRedirect_NothingToKeepCurrent(t *testing.T) {
	for reason, contents := range map[string]string{
		"no default vhost":     "",
		"no webmail redirects": "server {\n    listen 80 default_server;\n    return 301 https://$host$request_uri;\n}\n",
	} {
		t.Run(reason, func(t *testing.T) {
			withWebmailRedirectVhost(t, contents)
			ran := recordRedirectExec(t, "")
			resp, err := callWebmailRedirect(t, "mail.example.net")
			if err != nil {
				t.Fatal(err)
			}
			if resp.Rewritten || resp.Reason != reason || len(*ran) != 0 {
				t.Fatalf("resp = %+v commands = %v, want a no-op with a reason", resp, *ran)
			}
		})
	}
}

func TestNginxWebmailRedirect_NginxTestFailureRollsBack(t *testing.T) {
	path := withWebmailRedirectVhost(t, webmailRedirectFixture("mail.panel.example.com"))
	before, _ := os.ReadFile(path)
	ran := recordRedirectExec(t, "nginx")

	if _, err := callWebmailRedirect(t, "mail.example.net"); err == nil {
		t.Fatal("an nginx -t failure must be returned")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("file not rolled back after nginx -t failure:\n%s", after)
	}
	for _, c := range *ran {
		if strings.HasPrefix(c, "systemctl") {
			t.Fatalf("nginx must not be reloaded after a failed test: %v", *ran)
		}
	}
}

func TestNginxWebmailRedirect_RejectsAnInvalidHostname(t *testing.T) {
	path := withWebmailRedirectVhost(t, webmailRedirectFixture("mail.panel.example.com"))
	before, _ := os.ReadFile(path)
	recordRedirectExec(t, "")
	for _, bad := range []string{"", "mail.example.net/; return 200 x", "mail example.net", "https://mail.example.net", "*.example.net", "-bad.example.net"} {
		_, err := callWebmailRedirect(t, bad)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", bad, err)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a refused hostname must not touch the file")
	}
}
