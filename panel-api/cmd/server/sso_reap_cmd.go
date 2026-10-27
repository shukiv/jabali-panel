// `jabali sso-reap` — invoked by the jabali-sso-reaper.timer systemd
// unit every 30s. Enumerates ready WordPress installs, computes their
// docroot + subdirectory paths, then dispatches the agent's
// wordpress.reap_sso_files command to sweep stranded
// jabali-sso-<nonce>.php files older than the SSO TTL.
//
// See ADR-0040 §3 (TTL enforcement) — the reaper is defence in depth
// alongside the inline TTL check in the SSO PHP template. The PHP file
// unlinks itself on success; the reaper catches the cases where the
// PHP couldn't get to the unlink (web-server crash mid-execution etc.).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newSSOReapCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sso-reap",
		Short:   "Sweep stranded jabali-sso-<nonce>.php files",
		Long:    `Enumerate WordPress install paths from the application_installs table and dispatch the agent's wordpress.reap_sso_files command. Invoked by jabali-sso-reaper.timer every 30s.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Pull every install whose webroot is plausibly hosting a
			// live SSO file. "ready" is the obvious case; "installing"
			// and "failed" cover edge windows where the file may have
			// been written before status moved on.
			//
			// app_type is filtered to the set that actually mints SSO
			// files today (mirrors ssoAgentCommandFor in the mint
			// handler). Adding a new CMS requires widening this list
			// too — otherwise stranded SSO files for that CMS sit until
			// manually removed.
			ssoAppTypes := []string{"wordpress", "drupal", "joomla"}
			var installs []models.ApplicationInstall
			if err := sharedDB.WithContext(ctx).
				Where("app_type IN ? AND status IN ?", ssoAppTypes, []string{"ready", "installing", "failed"}).
				Find(&installs).Error; err != nil {
				return fmt.Errorf("list installs: %w", err)
			}
			if len(installs) == 0 {
				fmt.Println("sso-reap: no wordpress installs found, skipping agent call")
				return nil
			}

			// Resolve each install's webroot. The path is docroot[+subdir]
			// where docroot lives on the install's domain row.
			seenDomain := map[string]string{}
			paths := make([]string, 0, len(installs))
			for _, in := range installs {
				docRoot, ok := seenDomain[in.DomainID]
				if !ok {
					var dr struct {
						DocRoot string `gorm:"column:doc_root"`
					}
					err := sharedDB.WithContext(ctx).
						Table("domains").
						Select("doc_root").
						Where("id = ?", in.DomainID).
						First(&dr).Error
					if err != nil {
						fmt.Printf("sso-reap: skipping install %s — domain %s lookup failed: %v\n", in.ID, in.DomainID, err)
						continue
					}
					docRoot = dr.DocRoot
					seenDomain[in.DomainID] = docRoot
				}
				if strings.TrimSpace(docRoot) == "" {
					continue
				}
				sub := strings.Trim(in.Subdirectory, "/")
				if sub == "" {
					paths = append(paths, docRoot)
				} else {
					paths = append(paths, path.Clean(docRoot+"/"+sub))
				}
			}
			if len(paths) == 0 {
				fmt.Println("sso-reap: no install paths resolved, skipping agent call")
				return nil
			}

			payload := map[string]any{"install_paths": paths}
			raw, err := sharedAgent.Call(ctx, "wordpress.reap_sso_files", payload)
			if err != nil {
				return fmt.Errorf("agent call: %w", err)
			}

			var resp struct {
				DeletedCount int `json:"deleted_count"`
				ScannedCount int `json:"scanned_count"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("decode reap response: %w", err)
			}
			fmt.Printf("sso-reap: scanned=%d deleted=%d paths=%d\n",
				resp.ScannedCount, resp.DeletedCount, len(paths))
			return nil
		},
	}
}
