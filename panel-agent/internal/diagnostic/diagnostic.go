// Package diagnostic collects host status, redacts secrets, and packs the
// result as a tar archive. Encryption + delivery live in the `enclosed`
// package — this file only knows how to gather + redact + tar.
package diagnostic

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
)

// Bundle is the redacted, ready-to-encrypt blob.
type Bundle struct {
	TarBytes       []byte
	FileCount      int
	RedactionCount int
	GeneratedAt    time.Time
}

// servicesToCollect is the fixed list of systemd units we journal-tail
// for every report. Hard-coded so a malicious request can't widen the
// surface.
var servicesToCollect = []string{
	"jabali-panel.service",
	"jabali-agent.service",
	"jabali-stalwart.service",
	"jabali-webmail.service",
	"jabali-kratos.service",
	"pdns.service",
	"pdns-recursor.service",
	"mariadb.service",
	"redis-server.service",
	"nginx.service",
	"certbot.service",
	"certbot.timer",
}

// letsencryptLogPath is the canonical certbot log on Debian/Ubuntu.
// Tailed (not full-read) because successful renewals append every run
// and the file grows unbounded between rotations.
const letsencryptLogPath = "/var/log/letsencrypt/letsencrypt.log"

// letsencryptLogTailLines is the number of trailing lines included
// in the bundle. Enough to capture the most recent issuance attempt
// + a few prior renewals for context, bounded so the bundle stays
// emailable.
const letsencryptLogTailLines = 500

// crowdsecLogPath / crowdsecLogTailLines: CrowdSec's main log. Included so
// support can see WHY an IP was banned (scenario decisions + WAF anomaly
// hits) when a user reports being locked out of everything (CrowdSec bans
// are IP-scoped and, via M43 unified trust, block web + ssh at once).
const crowdsecLogPath = "/var/log/crowdsec.log"
const crowdsecLogTailLines = 300

type collectedFile struct {
	Name string
	Body []byte
}

// Build runs the collector + redactor + tar packer.
func Build(ctx context.Context) (Bundle, error) {
	files := collect(ctx)
	totalRedactions := 0
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for _, f := range files {
		body, n := Redact(f.Body)
		totalRedactions += n
		hdr := &tar.Header{
			Name:    f.Name,
			Size:    int64(len(body)),
			Mode:    0o644,
			ModTime: time.Now().UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return Bundle{}, fmt.Errorf("tar header %s: %w", f.Name, err)
		}
		if _, err := tw.Write(body); err != nil {
			return Bundle{}, fmt.Errorf("tar body %s: %w", f.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return Bundle{}, fmt.Errorf("tar close: %w", err)
	}
	return Bundle{
		TarBytes:       tarBuf.Bytes(),
		FileCount:      len(files),
		RedactionCount: totalRedactions,
		GeneratedAt:    time.Now().UTC(),
	}, nil
}

func collect(ctx context.Context) []collectedFile {
	files := []collectedFile{
		{"00-uname.txt", runOrErr(ctx, "uname", "-a")},
		{"01-os-release.txt", catFileOrErr("/etc/os-release")},
		{"02-uptime.txt", runOrErr(ctx, "uptime")},
		{"03-free.txt", runOrErr(ctx, "free", "-h")},
		{"04-df.txt", runOrErr(ctx, "df", "-h")},
		{"05-git-head.txt", runOrErr(ctx, "sudo", "-u", "jabali", "git", "-C", "/opt/jabali-panel", "rev-parse", "HEAD")},
		{"06-git-status.txt", runOrErr(ctx, "sudo", "-u", "jabali", "git", "-C", "/opt/jabali-panel", "status", "--porcelain")},
		{"07-ss-tnlp.txt", runOrErr(ctx, "ss", "-tnlp")},
		{"08-iptables-input.txt", runOrErr(ctx, "iptables", "-L", "INPUT", "-n")},
		{"09-dpkg-list.txt", runOrErr(ctx, "dpkg-query", "-W", "-f=${Package} ${Version}\n")},
		{"10-letsencrypt.log", tailFileOrErr(ctx, letsencryptLogPath, letsencryptLogTailLines)},
		// CrowdSec IP-ban forensics (lockout investigations): active
		// decisions = who is banned + scenario + expiry; alerts = what
		// triggered each ban; the log tail gives surrounding context.
		{"11-crowdsec-decisions.txt", runOrErr(ctx, "cscli", "decisions", "list")},
		{"12-crowdsec-alerts.txt", runOrErr(ctx, "cscli", "alerts", "list", "--limit", "50")},
		{"13-crowdsec.log", tailFileOrErr(ctx, crowdsecLogPath, crowdsecLogTailLines)},
	}
	for _, svc := range servicesToCollect {
		base := strings.TrimSuffix(svc, ".service")
		files = append(files,
			collectedFile{
				Name: fmt.Sprintf("svc/%s.is-active.txt", base),
				Body: runOrErr(ctx, "systemctl", "is-active", svc),
			},
			collectedFile{
				Name: fmt.Sprintf("svc/%s.status.txt", base),
				Body: runOrErr(ctx, "systemctl", "status", svc, "--no-pager", "-n", "0"),
			},
			collectedFile{
				Name: fmt.Sprintf("svc/%s.journal.txt", base),
				Body: runOrErr(ctx, "journalctl", "-u", svc, "-n", "200", "--no-pager"),
			},
		)
	}
	return files
}

func runOrErr(ctx context.Context, name string, args ...string) []byte {
	out, err := execCommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return []byte(fmt.Sprintf("ERROR running %s %s: %v\n--- partial output ---\n%s",
			name, strings.Join(args, " "), err, string(out)))
	}
	return out
}

func catFileOrErr(path string) []byte {
	out, err := execCommand("cat", path).Output()
	if err != nil {
		return []byte(fmt.Sprintf("ERROR reading %s: %v", path, err))
	}
	return out
}

// tailFileOrErr reads the final n lines of path. Used for log files
// that grow unbounded between rotations (letsencrypt.log).
func tailFileOrErr(ctx context.Context, path string, n int) []byte {
	out, err := execCommandContext(ctx, "tail", "-n", fmt.Sprintf("%d", n), path).Output()
	if err != nil {
		return []byte(fmt.Sprintf("ERROR tailing %s: %v", path, err))
	}
	return out
}
