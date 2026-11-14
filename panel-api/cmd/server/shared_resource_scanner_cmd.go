// `jabali shared-resource scanner` — a Files resource's scanner (GH #2503):
// an FTP/SFTP login homed at a drop folder in the domain's directory, whose
// finished files the panel moves into the shared folder. Routes through
// scandrops, as the REST door does.
//
//	jabali shared-resource scanner status  --resource <id>
//	jabali shared-resource scanner enable  --resource <id> --login scanner --space-mb 1024 [--password <pw>]
//	jabali shared-resource scanner disable --resource <id>
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/scandrops"
)

func newSharedResourceScannerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scanner",
		Short: "A Files resource's scanner: an FTP/SFTP drop folder moved into the shared folder",
	}
	cmd.AddCommand(newScannerStatusCmd(), newScannerEnableCmd(), newScannerDisableCmd())
	return cmd
}

func scannerDepsFromDB() scandrops.Deps {
	mount, _ := limits.QuotaMountFor("/home")
	return scandrops.Deps{
		Drops:     repository.NewScanDropRepository(sharedDB),
		Resources: sharedResourceRepoFromDB(),
		Domains:   domainRepoFromDB(),
		Settings:  repository.NewServerSettingsRepository(sharedDB),
		Ftp: ftpops.Deps{
			Agent:      sharedAgent,
			Accounts:   repository.NewFtpAccountRepository(sharedDB),
			Users:      repository.NewUserRepository(sharedDB),
			Packages:   packageRepoFromDB(),
			QuotaMount: mount,
		},
	}
}

// loadScannerResource loads a shared resource and its domain for a scanner
// command.
func loadScannerResource(ctx context.Context, id string) (*models.SharedResource, *models.Domain, error) {
	if id == "" {
		return nil, nil, errors.New("--resource required")
	}
	sr, err := sharedResourceRepoFromDB().FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil, fmt.Errorf("shared resource not found: %s", id)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load shared resource: %w", err)
	}
	dom, err := domainRepoFromDB().FindByID(ctx, sr.DomainID)
	if err != nil {
		return nil, nil, fmt.Errorf("load resource domain: %w", err)
	}
	return sr, dom, nil
}

func printScanner(row *models.ScanDrop, acct *models.FtpAccount) {
	if row == nil {
		fmt.Println("scanner: off")
		return
	}
	fmt.Println("scanner:  on")
	if acct != nil {
		fmt.Printf("login:    %s (isolated: %t, FTPS: %t, SFTP: %t)\n", acct.Username, acct.Isolated, acct.FTPAccess, acct.SFTPAccess)
	} else {
		fmt.Println("login:    deleted — the next pass turns the scanner off")
	}
	fmt.Printf("folder:   %s\n", row.DropPath)
	fmt.Printf("space:    %d MB\n", row.MaxMB)
	fmt.Printf("moved:    %d\n", row.IngestedTotal)
	if row.LastIngestAt != nil {
		fmt.Printf("last:     %s\n", row.LastIngestAt.UTC().Format(time.RFC3339))
	}
	if row.LastError != "" {
		fmt.Printf("problem:  %s\n", row.LastError)
	}
}

func newScannerStatusCmd() *cobra.Command {
	var resourceID string
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show a Files resource's scanner",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			sr, _, err := loadScannerResource(ctx, resourceID)
			if err != nil {
				return err
			}
			row, acct, err := scandrops.Get(ctx, scannerDepsFromDB(), sr.ID)
			if err != nil {
				return fmt.Errorf("load scanner: %w", err)
			}
			printScanner(row, acct)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID (a Files resource)")
	return cmd
}

func newScannerEnableCmd() *cobra.Command {
	var resourceID, label, password string
	var spaceMB uint32
	cmd := &cobra.Command{
		Use:     "enable",
		Short:   "Turn a Files resource's scanner on: make its drop folder and FTP/SFTP login",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			sr, dom, err := loadScannerResource(ctx, resourceID)
			if err != nil {
				return err
			}
			generated := password == ""
			if generated {
				password = rand.Text()
			}
			row, acct, err := scandrops.Enable(ctx, scannerDepsFromDB(), scandrops.EnableInput{
				Resource: sr,
				Domain:   dom,
				Label:    label,
				Password: password,
				MaxMB:    spaceMB,
			})
			if err != nil {
				var ve *ftpops.ValidationError
				if errors.As(err, &ve) {
					return errors.New(ve.Detail)
				}
				return fmt.Errorf("enable scanner: %w", err)
			}
			cliAuditOK(ctx, "shared_resource.scanner_enable", "shared_resource", sr.ID, nil)
			printScanner(row, acct)
			if generated {
				fmt.Printf("password: %s (shown once)\n", password)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID (a Files resource)")
	cmd.Flags().StringVar(&label, "login", "scanner", "login name; the login is <owner>_<login>")
	cmd.Flags().StringVar(&password, "password", "", "login password (generated and shown once if omitted)")
	cmd.Flags().Uint32Var(&spaceMB, "space-mb", 1024, "space the scans may keep, in MB (the login's disk quota where logins are isolated)")
	return cmd
}

func newScannerDisableCmd() *cobra.Command {
	var resourceID string
	cmd := &cobra.Command{
		Use:     "disable",
		Short:   "Turn a Files resource's scanner off: remove its login (files left in the drop folder stay)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			sr, _, err := loadScannerResource(ctx, resourceID)
			if err != nil {
				return err
			}
			if err := scandrops.Disable(ctx, scannerDepsFromDB(), sr.ID); err != nil {
				return fmt.Errorf("disable scanner: %w", err)
			}
			cliAuditOK(ctx, "shared_resource.scanner_disable", "shared_resource", sr.ID, nil)
			fmt.Println("scanner: off")
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID (a Files resource)")
	return cmd
}
