package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// sslCertInfoParams asks about the certbot lineage <cert_name>. SANs, when
// given, is the set the caller needs covered; the response reports whether the
// on-disk cert covers all of them.
type sslCertInfoParams struct {
	CertName string   `json:"cert_name"`
	SANs     []string `json:"sans"`
}

// sslCertInfoResponse describes the leaf cert currently on the lineage. When no
// cert exists (or it cannot be read), Exists is false and every other field is
// zero — that is a normal answer ("nothing issued yet"), not an error.
//
// JAB-407: the panel uses this after a DNS-01 issuance call times out its
// socket read — certbot runs detached agent-side and may have finished — to
// tell a freshly issued cert (record success, no backoff) from a stale one left
// by a previous issuance (the renewal path reaches the same code): it compares
// NotBefore against the attempt start. So this command is a read-only probe; it
// never issues, renews, or mutates anything.
type sslCertInfoResponse struct {
	Exists     bool     `json:"exists"`
	NotBefore  string   `json:"not_before,omitempty"`
	NotAfter   string   `json:"not_after,omitempty"`
	Serial     string   `json:"serial,omitempty"`
	DNSNames   []string `json:"dns_names,omitempty"`
	CoversSANs bool     `json:"covers_sans"`
	CertPath   string   `json:"cert_path,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
}

func sslCertInfoHandler(_ context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p sslCertInfoParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	// Same lineage-name validation as ssl.issue_dns01: a plain FQDN label, no
	// wildcard, no path separators — so cert_name can never climb out of
	// <sslLERoot>/live/ into an arbitrary file read.
	if !sslDomainRegex.MatchString(p.CertName) || strings.Contains(p.CertName, "*") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid cert_name %q: must be a plain name with no wildcard", p.CertName),
		}
	}

	cert, err := certbot.ReadLineageCert(sslLERoot, p.CertName)
	if err != nil {
		// No lineage / unreadable PEM → "nothing issued", not an error.
		return sslCertInfoResponse{Exists: false}, nil
	}

	covers := true
	have := make(map[string]struct{}, len(cert.DNSNames))
	for _, n := range cert.DNSNames {
		have[n] = struct{}{}
	}
	for _, want := range p.SANs {
		if _, ok := have[want]; !ok {
			covers = false
			break
		}
	}

	return sslCertInfoResponse{
		Exists:     true,
		NotBefore:  cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:   cert.NotAfter.UTC().Format(time.RFC3339),
		Serial:     cert.SerialNumber.Text(16),
		DNSNames:   cert.DNSNames,
		CoversSANs: covers,
		CertPath:   fmt.Sprintf("%s/live/%s/fullchain.pem", sslLERoot, p.CertName),
		KeyPath:    fmt.Sprintf("%s/live/%s/privkey.pem", sslLERoot, p.CertName),
	}, nil
}

func init() {
	Default.Register("ssl.cert_info", sslCertInfoHandler)
}
