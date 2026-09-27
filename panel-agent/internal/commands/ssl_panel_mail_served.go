package commands

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssl.panel.mail_served checks that Stalwart serves the certificate just
// issued for the panel mail hostname (JAB-408).
//
// ssl.panel.issue returning OK means certbot issued the certificate and the
// deploy hook ran. It does not mean mail clients get it: on .60 a stale
// deploy hook from before JAB-390 left :993 serving the old certificate, and
// the switchover still reported done. The panel calls this verb after the
// issue and before it applies the new name, and fails the switchover when
// the answer is no.
//
// The check has two parts:
//   - The panel mail lineage record (written by the current deploy hook) must
//     name the hostname. A hook that predates the record, or that deployed
//     another lineage, fails here with no probe.
//   - IMAPS (993) and SMTPS (465) on loopback, asked for the hostname by SNI,
//     must present the leaf certificate of live/<hostname>. The hook
//     restarts Stalwart last, so each port is polled up to mailServedWait.
//
// The ports and the address are fixed: the root agent does not probe
// ports a caller chooses.
type sslPanelMailServedParams struct {
	Hostname string `json:"hostname"`
}

type sslPanelMailServedResponse struct {
	OK       bool              `json:"ok"`
	Reason   string            `json:"reason,omitempty"`
	Expected string            `json:"expected_sha256,omitempty"`
	Ports    []mailServedCheck `json:"ports,omitempty"`
}

type mailServedCheck struct {
	Port   int    `json:"port"`
	Served string `json:"served_sha256,omitempty"`
	Match  bool   `json:"match"`
	Error  string `json:"error,omitempty"`
}

var (
	// mailServedLineageFile is the deploy hook's panel mail lineage record
	// (install/letsencrypt/jabali-panel-cert.sh).
	mailServedLineageFile = "/etc/jabali/tls/panel-mail.lineage"
	// mailServedPorts are Stalwart's implicit-TLS listeners.
	mailServedPorts = []int{993, 465}
	mailServedAddr  = func(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
	mailServedWait  = 30 * time.Second
	mailServedPoll  = time.Second
)

func init() {
	Default.Register("ssl.panel.mail_served", sslPanelMailServedHandler)
}

func sslPanelMailServedHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslPanelMailServedParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !sslDomainRegex.MatchString(p.Hostname) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid hostname %q", p.Hostname)}
	}
	host := strings.ToLower(p.Hostname)

	if recorded := readMailLineageRecord(); recorded != host {
		if recorded == "" {
			recorded = "nothing"
		}
		return sslPanelMailServedResponse{Reason: fmt.Sprintf(
			"the certificate for %s was issued, but the certificate hook recorded %s as the panel mail certificate, so it did not deploy it to the mail server; run `jabali update` to reinstall the certificate hooks",
			host, recorded)}, nil
	}

	blob, err := os.ReadFile(filepath.Join(sslLERoot, "live", host, "fullchain.pem"))
	if err != nil {
		return sslPanelMailServedResponse{Reason: fmt.Sprintf("read the issued certificate for %s: %v", host, err)}, nil
	}
	leaf, err := parseLeafCert(string(blob))
	if err != nil {
		return sslPanelMailServedResponse{Reason: fmt.Sprintf("read the issued certificate for %s: %v", host, err)}, nil
	}
	expected := certSHA256(leaf.Raw)

	resp := sslPanelMailServedResponse{OK: true, Expected: expected}
	deadline := time.Now().Add(mailServedWait)
	var failed []string
	for _, port := range mailServedPorts {
		check := pollServedCert(ctx, port, host, expected, deadline)
		resp.Ports = append(resp.Ports, check)
		if check.Match {
			continue
		}
		resp.OK = false
		if check.Error != "" {
			failed = append(failed, fmt.Sprintf("port %d: %s", port, check.Error))
		} else {
			failed = append(failed, fmt.Sprintf("port %d serves another certificate (sha256 %s)", port, shortSHA(check.Served)))
		}
	}
	if !resp.OK {
		resp.Reason = fmt.Sprintf(
			"the mail server does not serve the new certificate for %s (sha256 %s): %s; run `jabali update` if the certificate hooks are out of date",
			host, shortSHA(expected), strings.Join(failed, "; "))
	}
	return resp, nil
}

// pollServedCert asks port for host until it presents the expected
// certificate or deadline passes, and returns the last answer.
func pollServedCert(ctx context.Context, port int, host, expected string, deadline time.Time) mailServedCheck {
	for {
		check := mailServedCheck{Port: port}
		served, err := servedCertSHA256(ctx, mailServedAddr(port), host)
		if err != nil {
			check.Error = err.Error()
		} else {
			check.Served = served
			check.Match = served == expected
		}
		if check.Match || !time.Now().Before(deadline) {
			return check
		}
		select {
		case <-ctx.Done():
			return check
		case <-time.After(mailServedPoll):
		}
	}
}

// servedCertSHA256 returns the SHA-256 of the leaf certificate addr
// presents for the SNI name host. The chain is not verified: the question is
// which certificate is served, not whether it is trusted (a staging
// certificate is a valid answer).
func servedCertSHA256(ctx context.Context, addr, host string) (string, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    &tls.Config{ServerName: host, InsecureSkipVerify: true}, //nolint:gosec // identity check by fingerprint, not trust
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("no certificate presented")
	}
	return certSHA256(certs[0].Raw), nil
}

// readMailLineageRecord returns the lineage the deploy hook last deployed
// as the panel mail certificate, or "" when there is no record.
func readMailLineageRecord() string {
	f, err := os.Open(mailServedLineageFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s.Text()))
}

func certSHA256(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func shortSHA(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}
