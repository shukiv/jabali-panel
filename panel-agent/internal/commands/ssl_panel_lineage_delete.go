package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssl.panel.lineage_delete removes the certbot lineage of a custom panel
// mail hostname that a switchover replaced (JAB-408).
//
// The deploy hook ignores a lineage that is not the recorded panel mail
// lineage, so an old one is harmless while its name still resolves here.
// certbot keeps renewing it, though, and once the admin removes the old
// name's DNS every renewal run fails for it. The panel calls this verb after
// a switchover completes, for the previous custom name only (never
// mail.<hostname> or the hostname, whose lineages stay in use).
//
// The lineage is kept, with a reason, when:
//   - it is the recorded panel mail lineage (the certificate in use);
//   - an nginx config still references /live/<name>/;
//   - it does not exist (a no-op).
//
// Otherwise cleanupCertbotLineage removes it (certbot delete, with the
// renewal-conf floor).
type sslPanelLineageDeleteParams struct {
	Name string `json:"name"`
}

type sslPanelLineageDeleteResponse struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

// lineageDeleteNginxDirs are searched for references to the lineage.
// sites-enabled holds symlinks into sites-available, so it is not searched.
var lineageDeleteNginxDirs = []string{"/etc/nginx/sites-available", "/etc/nginx/conf.d", "/etc/nginx/snippets"}

func init() {
	Default.Register("ssl.panel.lineage_delete", sslPanelLineageDeleteHandler)
}

func sslPanelLineageDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslPanelLineageDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !sslDomainRegex.MatchString(p.Name) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid lineage name %q", p.Name)}
	}
	if err := validateDomainNameForShell(p.Name); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid lineage name %q", p.Name)}
	}
	name := strings.ToLower(p.Name)

	if _, err := os.Stat(filepath.Join(sslLERoot, "renewal", name+".conf")); err != nil {
		return sslPanelLineageDeleteResponse{Reason: "no such lineage"}, nil
	}
	if readMailLineageRecord() == name {
		return sslPanelLineageDeleteResponse{Reason: "it is the panel mail certificate in use"}, nil
	}
	if file := nginxReferencesLineage(name); file != "" {
		return sslPanelLineageDeleteResponse{Reason: "nginx still references it in " + file}, nil
	}

	cleanupCertbotLineage(ctx, sslLERoot, name)
	if _, err := os.Stat(filepath.Join(sslLERoot, "renewal", name+".conf")); err == nil {
		return sslPanelLineageDeleteResponse{Reason: "certbot did not remove the lineage"}, nil
	}
	return sslPanelLineageDeleteResponse{Deleted: true}, nil
}

// nginxReferencesLineage returns the first nginx config file that contains
// /live/<name>/, or "".
func nginxReferencesLineage(name string) string {
	needle := []byte("/live/" + name + "/")
	found := ""
	for _, dir := range lineageDeleteNginxDirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, needle) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}
