package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-408: after a switchover from one custom mail hostname to another (or
// back to mail.<hostname>), the old custom name's certbot lineage is no
// longer deployed anywhere, but certbot keeps renewing it; once its DNS is
// removed every renewal run fails. ssl.panel.lineage_delete removes it,
// refusing any lineage still in use.

type lineageFixture struct {
	root     string
	nginxDir string
}

func setupLineageDelete(t *testing.T, record string, lineages ...string) *lineageFixture {
	t.Helper()
	f := &lineageFixture{root: t.TempDir(), nginxDir: t.TempDir()}
	for _, name := range lineages {
		if err := os.MkdirAll(filepath.Join(f.root, "live", name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.root, "renewal"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "renewal", name+".conf"), []byte("cert = x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recordFile := filepath.Join(f.root, "panel-mail.lineage")
	if err := os.WriteFile(recordFile, []byte(record+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No certbot on PATH: cleanupCertbotLineage falls back to removing the
	// renewal conf, which is what makes certbot stop renewing the lineage.
	t.Setenv("PATH", t.TempDir())

	prevRoot, prevRecord, prevDirs := sslLERoot, mailServedLineageFile, lineageDeleteNginxDirs
	sslLERoot, mailServedLineageFile, lineageDeleteNginxDirs = f.root, recordFile, []string{f.nginxDir}
	t.Cleanup(func() { sslLERoot, mailServedLineageFile, lineageDeleteNginxDirs = prevRoot, prevRecord, prevDirs })
	return f
}

func (f *lineageFixture) renewalExists(name string) bool {
	_, err := os.Stat(filepath.Join(f.root, "renewal", name+".conf"))
	return err == nil
}

func callLineageDelete(t *testing.T, name string) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"name": name})
	out, err := sslPanelLineageDeleteHandler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, nil
}

func TestSSLPanelLineageDelete_RemovesAnUnusedLineage(t *testing.T) {
	f := setupLineageDelete(t, "mx2.example.net", "mx.example.net", "mx2.example.net")

	resp, err := callLineageDelete(t, "MX.Example.NET")
	if err != nil {
		t.Fatal(err)
	}
	if resp["deleted"] != true {
		t.Fatalf("resp = %v, want deleted", resp)
	}
	if f.renewalExists("mx.example.net") {
		t.Fatal("the renewal conf is still there, so certbot keeps renewing the lineage")
	}
	if !f.renewalExists("mx2.example.net") {
		t.Fatal("another lineage was touched")
	}
}

func TestSSLPanelLineageDelete_RefusesTheCurrentPanelMailLineage(t *testing.T) {
	f := setupLineageDelete(t, "mx.example.net", "mx.example.net")

	resp, err := callLineageDelete(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp["deleted"] != false || !f.renewalExists("mx.example.net") {
		t.Fatalf("resp = %v: the lineage the panel mail certificate comes from must be kept", resp)
	}
}

// A lineage an nginx vhost still points at would take that vhost (and every
// later nginx -t) down with it.
func TestSSLPanelLineageDelete_RefusesALineageNginxReferences(t *testing.T) {
	f := setupLineageDelete(t, "mx2.example.net", "mx.example.net")
	conf := "server {\n    ssl_certificate /etc/letsencrypt/live/mx.example.net/fullchain.pem;\n}\n"
	if err := os.WriteFile(filepath.Join(f.nginxDir, "tenant.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := callLineageDelete(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	reason, _ := resp["reason"].(string)
	if resp["deleted"] != false || !f.renewalExists("mx.example.net") || !strings.Contains(reason, "tenant.conf") {
		t.Fatalf("resp = %v: a lineage nginx references must be kept, naming the file", resp)
	}
}

// A longer lineage name that merely starts with the same text is not a
// reference to this one.
func TestSSLPanelLineageDelete_OtherLineageReferenceIsNotAMatch(t *testing.T) {
	f := setupLineageDelete(t, "mx2.example.net", "mx.example.net")
	conf := "ssl_certificate /etc/letsencrypt/live/mx.example.net.au/fullchain.pem;\n"
	if err := os.WriteFile(filepath.Join(f.nginxDir, "other.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := callLineageDelete(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp["deleted"] != true || f.renewalExists("mx.example.net") {
		t.Fatalf("resp = %v, want deleted", resp)
	}
}

func TestSSLPanelLineageDelete_NoLineageIsANoOp(t *testing.T) {
	setupLineageDelete(t, "mx2.example.net")

	resp, err := callLineageDelete(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp["deleted"] != false {
		t.Fatalf("resp = %v, want a no-op", resp)
	}
}

func TestSSLPanelLineageDelete_RejectsAnInvalidName(t *testing.T) {
	f := setupLineageDelete(t, "mx2.example.net", "mx.example.net")
	for _, bad := range []string{"", "../renewal/mx.example.net", "mx.example.net/..", "-mx.example.net", "mx example.net", "mx.example.net;rm"} {
		_, err := callLineageDelete(t, bad)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", bad, err)
		}
	}
	if !f.renewalExists("mx.example.net") {
		t.Fatal("a refused name must not touch any lineage")
	}
}

func TestSSLPanelLineageDelete_Registered(t *testing.T) {
	if _, ok := Default.handlers["ssl.panel.lineage_delete"]; !ok {
		t.Fatal("ssl.panel.lineage_delete is not registered")
	}
}
