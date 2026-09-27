package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-408: a switchover reported `done` while Stalwart still served the old
// certificate on :993 (a stale deploy hook on .60). ssl.panel.mail_served
// tells the panel whether the certificate just issued for the mail hostname
// is the one Stalwart serves on IMAPS and SMTPS, before the name is applied.

func newServedTestCert(t *testing.T, name string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// servedFixture is a lineage for host, the panel mail lineage record, and
// one TLS listener per mail port.
type servedFixture struct {
	host   string
	issued tls.Certificate
	other  tls.Certificate
	// record is the panel mail lineage record's content; "-" leaves it out.
	record string
	// serve picks the certificate a port answers with for an SNI name.
	serve func(port int, sni string) tls.Certificate
}

func setupMailServed(t *testing.T, f *servedFixture) {
	t.Helper()
	root := t.TempDir()
	issued, issuedPEM := newServedTestCert(t, f.host)
	other, _ := newServedTestCert(t, "mail.old.example.com")
	f.issued, f.other = issued, other
	if f.serve == nil {
		f.serve = func(int, string) tls.Certificate { return f.issued }
	}

	dir := filepath.Join(root, "live", f.host)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), issuedPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	recordFile := filepath.Join(root, "panel-mail.lineage")
	if f.record != "-" {
		if err := os.WriteFile(recordFile, []byte(f.record+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	addrs := map[int]string{}
	for _, port := range []int{993, 465} {
		port := port
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				c := f.serve(port, hello.ServerName)
				return &c, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					_ = c.(*tls.Conn).Handshake()
					c.Close()
				}()
			}
		}()
		addrs[port] = ln.Addr().String()
	}

	prevRoot, prevRecord, prevAddr, prevWait, prevPoll := sslLERoot, mailServedLineageFile, mailServedAddr, mailServedWait, mailServedPoll
	sslLERoot, mailServedLineageFile = root, recordFile
	mailServedAddr = func(port int) string { return addrs[port] }
	mailServedWait, mailServedPoll = 500*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		sslLERoot, mailServedLineageFile, mailServedAddr, mailServedWait, mailServedPoll = prevRoot, prevRecord, prevAddr, prevWait, prevPoll
	})
}

func callMailServed(t *testing.T, host string) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"hostname": host})
	out, err := sslPanelMailServedHandler(context.Background(), raw)
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

func TestSSLPanelMailServed_MatchOnBothPorts(t *testing.T) {
	f := &servedFixture{host: "mx.example.net", record: "mx.example.net"}
	var sni atomic.Value
	f.serve = func(_ int, name string) tls.Certificate {
		sni.Store(name)
		if name != "mx.example.net" {
			return f.other
		}
		return f.issued
	}
	setupMailServed(t, f)

	resp, err := callMailServed(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("resp = %v, want ok", resp)
	}
	if got, _ := sni.Load().(string); got != "mx.example.net" {
		t.Fatalf("SNI = %q, want the mail hostname: without SNI Stalwart serves its default certificate", got)
	}
	ports, _ := resp["ports"].([]any)
	if len(ports) != 2 {
		t.Fatalf("ports = %v, want 993 and 465", resp["ports"])
	}
}

func TestSSLPanelMailServed_OldCertificateOnOnePort(t *testing.T) {
	f := &servedFixture{host: "mx.example.net", record: "mx.example.net"}
	f.serve = func(port int, _ string) tls.Certificate {
		if port == 993 {
			return f.other
		}
		return f.issued
	}
	setupMailServed(t, f)

	resp, err := callMailServed(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	reason, _ := resp["reason"].(string)
	if resp["ok"] != false || !strings.Contains(reason, "993") || strings.Contains(reason, "465") {
		t.Fatalf("resp = %v, want not ok naming port 993 only", resp)
	}
}

// Stalwart is restarted at the end of the deploy hook, so the new
// certificate can appear a moment after the verb starts.
func TestSSLPanelMailServed_WaitsForStalwart(t *testing.T) {
	f := &servedFixture{host: "mx.example.net", record: "mx.example.net"}
	var start atomic.Value
	start.Store(time.Now())
	f.serve = func(int, string) tls.Certificate {
		if time.Since(start.Load().(time.Time)) < 150*time.Millisecond {
			return f.other
		}
		return f.issued
	}
	setupMailServed(t, f)
	start.Store(time.Now())

	resp, err := callMailServed(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("resp = %v, want ok once Stalwart serves the new certificate", resp)
	}
}

// A deploy hook from before JAB-390 never records the lineage, and one that
// deployed another lineage records that one. Either way the certificate did
// not reach Stalwart through the current hook; say so without probing.
func TestSSLPanelMailServed_LineageRecordMustNameTheHost(t *testing.T) {
	for name, record := range map[string]string{"missing": "-", "other lineage": "mail.mx.example.com"} {
		t.Run(name, func(t *testing.T) {
			f := &servedFixture{host: "mx.example.net", record: record}
			var probed atomic.Bool
			f.serve = func(int, string) tls.Certificate {
				probed.Store(true)
				return f.issued
			}
			setupMailServed(t, f)

			resp, err := callMailServed(t, "mx.example.net")
			if err != nil {
				t.Fatal(err)
			}
			reason, _ := resp["reason"].(string)
			if resp["ok"] != false || !strings.Contains(reason, "jabali update") {
				t.Fatalf("resp = %v, want not ok pointing at jabali update", resp)
			}
			if probed.Load() {
				t.Fatal("a stale lineage record needs no port probe")
			}
		})
	}
}

func TestSSLPanelMailServed_PortNotListening(t *testing.T) {
	f := &servedFixture{host: "mx.example.net", record: "mx.example.net"}
	setupMailServed(t, f)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := closed.Addr().String()
	closed.Close()
	live := mailServedAddr
	mailServedAddr = func(port int) string {
		if port == 465 {
			return dead
		}
		return live(port)
	}

	resp, err := callMailServed(t, "mx.example.net")
	if err != nil {
		t.Fatal(err)
	}
	reason, _ := resp["reason"].(string)
	if resp["ok"] != false || !strings.Contains(reason, "465") {
		t.Fatalf("resp = %v, want not ok naming port 465", resp)
	}
}

func TestSSLPanelMailServed_RejectsAnInvalidHostname(t *testing.T) {
	f := &servedFixture{host: "mx.example.net", record: "mx.example.net"}
	setupMailServed(t, f)
	for _, bad := range []string{"", "../etc", "mx.example.net/x", "-mx.example.net", "mx example.net"} {
		_, err := callMailServed(t, bad)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", bad, err)
		}
	}
}

func TestSSLPanelMailServed_Registered(t *testing.T) {
	if _, ok := Default.handlers["ssl.panel.mail_served"]; !ok {
		t.Fatal("ssl.panel.mail_served is not registered")
	}
}
