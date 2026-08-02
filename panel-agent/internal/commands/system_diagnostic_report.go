package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/diagnostic"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/enclosed"
)

// defaultEnclosedBaseURL points at the operator-controlled enclosed.cc-
// compatible note-sharing server. Hard-coded so the agent ships with the
// right endpoint baked in. Override via JABALI_ENCLOSED_URL for dev.
const defaultEnclosedBaseURL = "https://enclosed.jabali-panel.com"

func enclosedBaseURL() string {
	if v := os.Getenv("JABALI_ENCLOSED_URL"); v != "" {
		return v
	}
	return defaultEnclosedBaseURL
}

// systemDiagnosticReportResponse mirrors what the panel UI renders on
// /jabali-admin/support after the operator clicks "Send Diagnostic
// Report". The URL + password are independent: the URL alone won't
// decrypt without the password. The UI offers a "Send via email" button
// that opens the operator's mail client with both fields pre-filled.
type systemDiagnosticReportResponse struct {
	URL            string `json:"url"`
	Password       string `json:"password"`
	NoteID         string `json:"note_id"`
	ByteCount      int    `json:"byte_count"`
	GeneratedAt    string `json:"generated_at"`
	RedactionCount int    `json:"redaction_count"`
	FileCount      int    `json:"file_count"`
	// ClaimCode is a short, public-safe hand-off code (JAB-XXXXXXXX) minted by
	// the operator-run support-claim service, present only when JABALI_CLAIM_URL
	// is configured and registration succeeded. Empty => share url+password.
	ClaimCode string `json:"claim_code,omitempty"`
}

func systemDiagnosticReportHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	bundle, err := diagnostic.Build(ctx)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	hostname, _ := os.Hostname()
	fileName := fmt.Sprintf("jabali-diagnostic-%s-%s.tar",
		safeHostname(hostname),
		bundle.GeneratedAt.Format("2006-01-02-150405"))

	client := enclosed.NewClient(enclosedBaseURL())
	// 7-day TTL — long enough for the team to read at their leisure,
	// short enough that stale ciphertext rotates out without manual
	// cleanup.
	const ttlSeconds = 7 * 24 * 3600
	res, err := client.UploadFile(ctx, fileName, "application/x-tar", bundle.TarBytes, ttlSeconds)
	if err != nil {
		// upstream_unavailable (not internal): GH #465 follow-up — the panel
		// maps this to an actionable "server can't reach enclosed" message
		// instead of the generic "agent error" that left the reporter blind.
		return nil, &agentwire.AgentError{Code: agentwire.CodeUpstreamUnavailable, Message: fmt.Sprintf("enclosed upload: %v", err)}
	}

	out := systemDiagnosticReportResponse{
		URL:            res.URL,
		Password:       res.Password,
		NoteID:         res.NoteID,
		ByteCount:      len(bundle.TarBytes),
		GeneratedAt:    bundle.GeneratedAt.Format(time.RFC3339),
		RedactionCount: bundle.RedactionCount,
		FileCount:      bundle.FileCount,
	}

	// GH #357 follow-up: when a support-claim service is configured, register a
	// short public-safe code so the user shares "JAB-XXXXXXXX" instead of the
	// sensitive link+password. Best-effort — a failure just leaves ClaimCode
	// empty and the caller falls back to url+password.
	if base := claimBaseURL(); base != "" {
		if code, cerr := registerSupportClaim(ctx, base, claimIssueRequest{
			URL:      res.URL,
			Password: res.Password,
			Host:     hostname,
			NoteID:   res.NoteID,
			Bytes:    out.ByteCount,
			Files:    out.FileCount,
		}); cerr == nil {
			out.ClaimCode = code
		}
	}

	return out, nil
}

// safeHostname strips characters that would mess up a filename; falls
// back to "host" when the hostname is empty (containers with HOSTNAME
// unset).
func safeHostname(h string) string {
	if h == "" {
		return "host"
	}
	out := make([]rune, 0, len(h))
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

func init() {
	Default.Register("system.diagnostic_report", systemDiagnosticReportHandler)
}
