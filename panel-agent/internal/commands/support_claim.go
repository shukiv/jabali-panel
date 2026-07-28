package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// support_claim.go — client side of the operator-run support-claim service
// (see support-claim/ in the repo root). After a diagnostic bundle is uploaded
// to enclosed, the agent optionally registers the {url, password} with the
// claim service and gets back a short, public-safe code (JAB-XXXXXXXX). The
// user can then paste the code — not the sensitive link+password — anywhere,
// and the support team redeems it (authenticated) to get the bundle.

// claimBaseURL is the operator-run support-claim service. Empty (the default)
// means the diagnostic report skips claim registration and returns just the raw
// enclosed link + password — so no request ever hits a non-existent host. Set
// JABALI_CLAIM_URL on jabali-agent.service to enable it.
func claimBaseURL() string {
	return os.Getenv("JABALI_CLAIM_URL")
}

type claimIssueRequest struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	Host     string `json:"host,omitempty"`
	NoteID   string `json:"note_id,omitempty"`
	Bytes    int    `json:"byte_count,omitempty"`
	Files    int    `json:"file_count,omitempty"`
}

// registerSupportClaim POSTs the bundle's {url, password, meta} to the claim
// service and returns the short public code. Best-effort: any failure returns an
// error the caller treats as "no code — fall back to link + password".
func registerSupportClaim(ctx context.Context, base string, req claimIssueRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/claims", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claim service status %d", resp.StatusCode)
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Code == "" {
		return "", fmt.Errorf("claim service returned an empty code")
	}
	return out.Code, nil
}
