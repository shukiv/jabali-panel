package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeyops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeys"
)

func sshKeyRepoFromDB() repository.SSHKeyRepository {
	return repository.NewSSHKeyRepository(sharedDB)
}

// cliSSHKeyScheduler is the operator CLI's sshkeyops.Scheduler: a no-op. A
// short-lived CLI process has no running reconciler to schedule, so
// authorized_keys converge on the reconciler's next ≤60s tick (the add/delete
// output tells the operator so). Wiring it explicitly keeps the required
// Scheduler dependency intentional rather than a nil.
type cliSSHKeyScheduler struct{}

func (cliSSHKeyScheduler) ScheduleUser(string) {}

func sshKeyOpsDeps() sshkeyops.Deps {
	return sshkeyops.Deps{Keys: sshKeyRepoFromDB(), Scheduler: cliSSHKeyScheduler{}}
}

func newSSHKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ssh-key",
		Aliases: []string{"sshkey"},
		Short:   "Manage user SSH authorized keys",
	}
	cmd.AddCommand(
		newSSHKeyListCmd(),
		newSSHKeyAddCmd(),
		newSSHKeyDeleteCmd(),
	)
	return cmd
}

func newSSHKeyListCmd() *cobra.Command {
	var (
		userLookup string
		all        bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List SSH keys (filtered by --user, or all without filter)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := sshKeyRepoFromDB()
			var rows []models.SSHKey
			var err error
			if all || userLookup == "" {
				rows, err = repo.List(ctx)
			} else {
				u, uerr := resolveUser(ctx, userLookup)
				if uerr != nil {
					return uerr
				}
				rows, err = repo.ListByUserID(ctx, u.ID)
			}
			if err != nil {
				return fmt.Errorf("list keys: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"keys": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("No SSH keys.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tUSER_ID\tNAME\tFINGERPRINT\tCREATED")
			for _, k := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					k.ID, k.UserID, k.Name, k.Fingerprint, k.CreatedAt.Format(time.RFC3339))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "filter by user (id|email|username); empty = all")
	cmd.Flags().BoolVar(&all, "all", false, "list every user's keys (default when --user is omitted)")
	cmd.MarkFlagsMutuallyExclusive("user", "all")
	return cmd
}

func newSSHKeyAddCmd() *cobra.Command {
	var (
		userLookup string
		name       string
		pubKey     string
		pubKeyFile string
		stdin      bool
	)
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add an SSH public key for a user",
		Long:    "Provide the public key inline (--pub-key), via file (--pub-key-file), or via stdin (--pub-key-stdin).",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			u, err := resolveUser(ctx, userLookup)
			if err != nil {
				return err
			}
			raw, err := readPubKey(pubKey, pubKeyFile, stdin)
			if err != nil {
				return err
			}
			// Validation, fingerprinting, dedup, persistence, and scheduling
			// all live in internal/sshkeyops (ADR-0083), shared with the REST
			// handler. The CLI keeps its operator-friendly error strings.
			key, err := sshkeyops.Add(ctx, sshKeyOpsDeps(), sshkeyops.AddRequest{
				UserID:    u.ID,
				Name:      name,
				PublicKey: raw,
			})
			if err != nil {
				switch {
				case errors.Is(err, sshkeys.ErrRSATooWeak):
					return fmt.Errorf("RSA key below 2048 bits — generate a stronger key")
				case errors.Is(err, sshkeys.ErrUnsupportedType):
					return fmt.Errorf("unsupported key type — use ed25519, ecdsa, or RSA ≥2048")
				case errors.Is(err, sshkeys.ErrInvalidKeyFormat):
					return fmt.Errorf("invalid public key format")
				case errors.Is(err, sshkeyops.ErrDuplicate):
					return fmt.Errorf("a key with this fingerprint already exists for this user")
				default:
					return fmt.Errorf("create key: %w", err)
				}
			}
			if jsonOutput {
				return printJSON(key)
			}
			cliAuditOK(ctx, "sshkey.add", "ssh_key", key.ID, &u.ID)
			fmt.Printf("Added SSH key %s (%s) for user %s\n  fingerprint: %s\n", key.ID, key.Name, derefStr(u.Username), key.Fingerprint)
			fmt.Println("Reconciler tick (≤60s) will write authorized_keys.")
			return nil
		},
	}
	cmd.Flags().StringVar(&userLookup, "user", "", "user (id|email|username) (required)")
	cmd.Flags().StringVar(&name, "name", "", "key label (required)")
	cmd.Flags().StringVar(&pubKey, "pub-key", "", "raw public key (e.g. 'ssh-ed25519 AAAA... user@host')")
	cmd.Flags().StringVar(&pubKeyFile, "pub-key-file", "", "path to public key file")
	cmd.Flags().BoolVar(&stdin, "pub-key-stdin", false, "read public key from stdin")
	cmd.MarkFlagsMutuallyExclusive("pub-key", "pub-key-file", "pub-key-stdin")
	_ = cmd.MarkFlagRequired("user")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newSSHKeyDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <key-id>",
		Short:   "Delete an SSH key",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			deps := sshKeyOpsDeps()
			// Unscoped lookup (OwnerID empty) — the operator CLI deletes any
			// user's key by ID; the shared owner-scope guard is for the REST
			// caller. Fetch first so the confirmation prompt can show the key.
			k, err := sshkeyops.Find(ctx, deps, sshkeyops.FindRequest{KeyID: args[0]})
			if err != nil {
				if errors.Is(err, sshkeyops.ErrNotFound) {
					return fmt.Errorf("key %q not found", args[0])
				}
				return fmt.Errorf("lookup key: %w", err)
			}
			if !force {
				fmt.Printf("Delete SSH key %s (%s, fp=%s)? [y/N]: ", k.ID, k.Name, k.Fingerprint)
				var c string
				fmt.Scanln(&c)
				if c != "y" && c != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			if err := sshkeyops.RemoveKey(ctx, deps, k); err != nil {
				return fmt.Errorf("delete key: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]string{"deleted": k.ID})
			}
			cliAuditOK(ctx, "sshkey.delete", "ssh_key", k.ID, nil)
			fmt.Printf("Deleted SSH key %s. Reconciler tick (≤60s) will rewrite authorized_keys.\n", k.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func readPubKey(inline, path string, fromStdin bool) (string, error) {
	if inline != "" {
		return strings.TrimSpace(inline), nil
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if fromStdin {
		buf, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(string(buf)), nil
	}
	return "", fmt.Errorf("one of --pub-key, --pub-key-file, --pub-key-stdin is required")
}
