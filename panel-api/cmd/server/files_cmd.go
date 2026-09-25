// `jabali files` (Gitea #567). Operator-side mirror of the tenant scoped file
// manager (api/files.go): list/read/download, mkdir, write, rename, move, copy,
// chmod, delete, archive, extract, upload, stat.
//
// Faithful by construction: every op resolves --user to (user_id, linux
// username) and calls the SAME `files.*` agent verb with the SAME params the
// REST handler sends. Path normalization, the per-user home jail, quota, and
// upload limits are all enforced **inside the agent** (the config-generation /
// fs trust boundary), so the CLI inherits the exact same scoping — it cannot
// reach outside the tenant's tree any more than the GUI can. Mutations are
// CLI-audited with the target tenant as subject user (#537).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/filesops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/uploadintake"
)

const filesAgentTimeout = 2 * time.Minute

// cliResolveUploadLimits reads upload_max_size_mb and resolves the CLI limits.
// An unreadable setting selects the module default, as on the File Manager.
func cliResolveUploadLimits(ctx context.Context) uploadintake.Limits {
	var mb uint32
	if s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx); err == nil && s != nil {
		mb = s.UploadMaxSizeMB
	}
	return cliUploadLimits(mb)
}

// cliUploadLimits is the CLI per-upload cap and per-owner staging budget: the
// File Manager's, from the same Upload Intake module (JAB-365). There is no
// 100 MiB clamp any more: it bounded the CLI's in-memory os.ReadFile (GH #661),
// not the agent — files.ingest moves the staged file by path with no size cap —
// and the CLI now streams the file into staging.
func cliUploadLimits(configuredMB uint32) uploadintake.Limits {
	return uploadintake.LimitsFor(configuredMB)
}

// resolveFilesUser resolves the target tenant and asserts a linux account.
func resolveFilesUser(ctx context.Context, ref string) (*models.User, error) {
	u, err := resolveUser(ctx, ref)
	if err != nil {
		return nil, err
	}
	if u.Username == nil || *u.Username == "" {
		return nil, fmt.Errorf("user %s has no linux account — file manager is per-tenant", u.Email)
	}
	return u, nil
}

func filesAgentCall(ctx context.Context, verb string, params any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, filesAgentTimeout)
	defer cancel()
	return sharedAgent.Call(cctx, verb, params)
}

// cliScope builds the filesops.Scope for a resolved tenant user. The CLI is
// always tenant-scoped, so AdminRoot stays false — the admin File Manager
// (GH #1184) is a REST-only surface.
func cliScope(u *models.User) filesops.Scope {
	return filesops.Scope{UserID: u.ID, Username: *u.Username}
}

func newFilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "Scoped tenant file manager (list/read/mkdir/move/chmod/archive/…) — same policy as the GUI",
	}
	cmd.AddCommand(
		newFilesListCmd(), newFilesReadCmd(), newFilesDownloadCmd(), newFilesStatCmd(),
		newFilesMkdirCmd(), newFilesWriteCmd(), newFilesRenameCmd(), newFilesMoveCmd(),
		newFilesCopyCmd(), newFilesChmodCmd(), newFilesDeleteCmd(), newFilesArchiveCmd(),
		newFilesExtractCmd(), newFilesUploadCmd(),
	)
	return cmd
}

// userFlag adds the shared --user flag and resolves it in RunE wrappers.
func addUserFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "user", "", "target tenant (email|username|id, required)")
}

func newFilesListCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "list <path>", Short: "List a directory", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			u, err := resolveFilesUser(ctx, user)
			if err != nil {
				return err
			}
			raw, err := filesAgentCall(ctx, filesops.MethodList, filesops.List(cliScope(u), args[0]))
			if err != nil {
				return err
			}
			return renderFilesList(raw, jsonOutput)
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

// renderFilesList decodes a files.list reply and writes it to stdout. Decoding
// through filesops fails closed: a malformed agent reply is an error, never an
// empty listing printed as success (JAB-340 — the old CLI `_ = json.Unmarshal`
// swallowed it). Split out from the command's RunE so that fail-closed contract
// is covered by a test without standing up DB-backed user resolution.
func renderFilesList(raw json.RawMessage, jsonOut bool) error {
	res, err := filesops.DecodeList(raw)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(res)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODE\tSIZE\tTYPE\tNAME")
	for _, e := range res.Entries {
		t := "file"
		if e.IsDir {
			t = "dir"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", e.Mode, e.Size, t, e.Name)
	}
	return w.Flush()
}

// readContent calls files.read and returns the decoded bytes.
func readContent(ctx context.Context, u *models.User, path string, limit int64) ([]byte, error) {
	raw, err := filesAgentCall(ctx, filesops.MethodRead, filesops.Read(cliScope(u), path, limit))
	if err != nil {
		return nil, err
	}
	res, err := filesops.DecodeRead(raw)
	if err != nil {
		return nil, err
	}
	// GH #660: the agent hard-caps reads (100 MiB). Never write a partial file
	// as a success — fail loudly (the shared RequireComplete rule, JAB-340) so
	// support/migration workflows don't silently produce corrupt local copies.
	if err := res.RequireComplete(); err != nil {
		return nil, err
	}
	return res.Bytes()
}

func newFilesReadCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "read <path>", Short: "Print a file's contents", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			b, err := readContent(c.Context(), u, args[0], 8*1024*1024)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(b)
			return err
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

func newFilesDownloadCmd() *cobra.Command {
	var user, out string
	cmd := &cobra.Command{
		Use: "download <path>", Short: "Download a file to a local path (--out)", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			if out == "" {
				return fmt.Errorf("--out <local-path> is required")
			}
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			b, err := readContent(c.Context(), u, args[0], 0)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, b, 0o600); err != nil {
				return err
			}
			fmt.Printf("wrote %d bytes to %s\n", len(b), out)
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&out, "out", "", "local destination path (required)")
	return cmd
}

func newFilesStatCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "stat <path>", Short: "Stat a path", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			raw, err := filesAgentCall(c.Context(), filesops.MethodStat, filesops.Stat(cliScope(u), args[0]))
			if err != nil {
				return err
			}
			// Validate the reply fails-closed before printing it: a malformed or
			// empty stat body must be an error, not an exit-0 pass-through of the
			// raw bytes. On success we print the original raw JSON so operator
			// output stays byte-for-byte stable.
			if _, err := filesops.DecodeStat(raw); err != nil {
				return err
			}
			os.Stdout.Write(raw)
			fmt.Println()
			return nil
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

func newFilesMkdirCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "mkdir <path>", Short: "Create a directory", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodMkdir, filesops.Mkdir(cliScope(u), args[0])); err != nil {
				cliAuditErr(c.Context(), "files.mkdir", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.mkdir", "user", u.ID, &u.ID)
			fmt.Printf("created %s\n", args[0])
			return nil
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

func newFilesWriteCmd() *cobra.Command {
	var user, content, from string
	cmd := &cobra.Command{
		Use: "write <path>", Short: "Write text to a file (--content or --from <local>)", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			body := content
			if from != "" {
				b, rerr := os.ReadFile(from)
				if rerr != nil {
					return rerr
				}
				body = string(b)
			} else if !c.Flags().Changed("content") {
				return fmt.Errorf("provide --content or --from <local-file>")
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodWrite, filesops.Write(cliScope(u), args[0], body)); err != nil {
				cliAuditErr(c.Context(), "files.write", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.write", "user", u.ID, &u.ID)
			fmt.Printf("wrote %d bytes to %s\n", len(body), args[0])
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&content, "content", "", "inline text content")
	cmd.Flags().StringVar(&from, "from", "", "read content from this local file")
	return cmd
}

func newFilesRenameCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "rename <path> <new-name>", Short: "Rename within the same directory", Args: cobra.ExactArgs(2), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			// JAB-340: shared with the REST handler via filesops — validates the
			// bare-name rule (now also rejecting "\", "." and "..", which this CLI
			// check missed) and derives new_path.
			newPath, err := filesops.RenameTarget(args[0], args[1])
			if err != nil {
				return err
			}
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodRename, filesops.Rename(cliScope(u), args[0], newPath)); err != nil {
				cliAuditErr(c.Context(), "files.rename", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.rename", "user", u.ID, &u.ID)
			fmt.Printf("renamed to %s\n", newPath)
			return nil
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

// moveOrCopy runs a scoped move or copy. The destination rule (reject "..",
// dest = Join(destDir, Base(path))) and the typed params are shared with the
// REST handler through filesops (JAB-340).
func moveOrCopy(c *cobra.Command, user, verb, path, destDir string) error {
	u, err := resolveFilesUser(c.Context(), user)
	if err != nil {
		return err
	}
	var (
		method string
		params any
		dest   string
	)
	switch verb {
	case "move":
		if dest, err = filesops.MoveTarget(path, destDir); err != nil {
			return err
		}
		method, params = filesops.MethodMove, filesops.Move(cliScope(u), path, dest)
	case "copy":
		if dest, err = filesops.CopyTarget(path, destDir); err != nil {
			return err
		}
		method, params = filesops.MethodCopy, filesops.Copy(cliScope(u), path, dest)
	default:
		return fmt.Errorf("unknown move/copy verb %q", verb)
	}
	action := "files." + verb
	if _, err := filesAgentCall(c.Context(), method, params); err != nil {
		cliAuditErr(c.Context(), action, "user", u.ID, &u.ID)
		return err
	}
	cliAuditOK(c.Context(), action, "user", u.ID, &u.ID)
	fmt.Printf("%s -> %s\n", path, dest)
	return nil
}

func newFilesMoveCmd() *cobra.Command {
	var user, to string
	cmd := &cobra.Command{
		Use: "move <path>", Short: "Move into a destination directory (--to)", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to <dest-dir> is required")
			}
			return moveOrCopy(c, user, "move", args[0], to)
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&to, "to", "", "destination directory (required)")
	return cmd
}

func newFilesCopyCmd() *cobra.Command {
	var user, to string
	cmd := &cobra.Command{
		Use: "copy <path>", Short: "Copy into a destination directory (--to)", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to <dest-dir> is required")
			}
			return moveOrCopy(c, user, "copy", args[0], to)
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&to, "to", "", "destination directory (required)")
	return cmd
}

func newFilesChmodCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use: "chmod <path> <octal-mode>", Short: "Change a path's permissions (e.g. 0644)", Args: cobra.ExactArgs(2), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodChmod, filesops.Chmod(cliScope(u), args[0], args[1])); err != nil {
				cliAuditErr(c.Context(), "files.chmod", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.chmod", "user", u.ID, &u.ID)
			fmt.Printf("chmod %s %s\n", args[1], args[0])
			return nil
		},
	}
	addUserFlag(cmd, &user)
	return cmd
}

func newFilesDeleteCmd() *cobra.Command {
	var user string
	var recursive, force bool
	cmd := &cobra.Command{
		Use: "delete <path>", Short: "Delete a file or directory", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("refusing to delete %q without --force", args[0])
			}
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodDelete, filesops.Delete(cliScope(u), args[0], recursive)); err != nil {
				cliAuditErr(c.Context(), "files.delete", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.delete", "user", u.ID, &u.ID)
			fmt.Printf("deleted %s\n", args[0])
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directories")
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func newFilesArchiveCmd() *cobra.Command {
	var user, out string
	cmd := &cobra.Command{
		Use: "archive <path...>", Short: "tar.gz one or more paths to a local file (--out)", Args: cobra.MinimumNArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			if out == "" {
				return fmt.Errorf("--out <local.tar.gz> is required")
			}
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			raw, err := filesAgentCall(c.Context(), filesops.MethodArchive, filesops.Archive(cliScope(u), args))
			if err != nil {
				cliAuditErr(c.Context(), "files.archive", "user", u.ID, &u.ID)
				return err
			}
			// JAB-340: DecodeArchive fails closed on a malformed reply or an empty
			// archive_path (ErrNoArchivePath), shared with the REST handler.
			res, err := filesops.DecodeArchive(raw)
			if err != nil {
				return err
			}
			defer os.Remove(res.ArchivePath)
			b, rerr := os.ReadFile(res.ArchivePath)
			if rerr != nil {
				return fmt.Errorf("read agent archive: %w", rerr)
			}
			if werr := os.WriteFile(out, b, 0o600); werr != nil {
				return werr
			}
			cliAuditOK(c.Context(), "files.archive", "user", u.ID, &u.ID)
			fmt.Printf("archived %d path(s) -> %s (%d bytes)\n", len(args), out, res.Size)
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&out, "out", "", "local .tar.gz destination (required)")
	return cmd
}

func newFilesExtractCmd() *cobra.Command {
	var user, dest string
	cmd := &cobra.Command{
		Use: "extract <archive-path>", Short: "Extract an archive inside the tenant tree (--dest)", Args: cobra.ExactArgs(1), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			if _, err := filesAgentCall(c.Context(), filesops.MethodExtract, filesops.Extract(cliScope(u), args[0], dest)); err != nil {
				cliAuditErr(c.Context(), "files.extract", "user", u.ID, &u.ID)
				return err
			}
			cliAuditOK(c.Context(), "files.extract", "user", u.ID, &u.ID)
			fmt.Printf("extracted %s\n", args[0])
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().StringVar(&dest, "dest", "", "destination directory (default: archive's directory)")
	return cmd
}

func newFilesUploadCmd() *cobra.Command {
	var user string
	var overwrite bool
	cmd := &cobra.Command{
		Use: "upload <local-file> <dest-path>", Short: "Upload a local file into the tenant tree", Args: cobra.ExactArgs(2), PreRunE: requireDBAndAgent,
		RunE: func(c *cobra.Command, args []string) error {
			u, err := resolveFilesUser(c.Context(), user)
			if err != nil {
				return err
			}
			written, err := cliUpload(c.Context(), cliUploadDeps{
				limits: cliResolveUploadLimits(c.Context()),
				call:   filesAgentCall,
			}, u.ID, *u.Username, args[0], args[1], overwrite)
			var ingestErr *cliIngestError
			if errors.As(err, &ingestErr) {
				cliAuditErr(c.Context(), "files.upload", "user", u.ID, &u.ID)
				return ingestErr.err
			}
			if err != nil {
				return err
			}
			cliAuditOK(c.Context(), "files.upload", "user", u.ID, &u.ID)
			fmt.Printf("uploaded %s -> %s (%d bytes)\n", args[0], args[1], written)
			return nil
		},
	}
	addUserFlag(cmd, &user)
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite if the destination exists")
	return cmd
}

// cliUploadDeps is the injection seam for `jabali files upload` (JAB-365): the
// resolved limits and the agent call, so tests drive cliUpload without a DB or
// an agent.
type cliUploadDeps struct {
	limits uploadintake.Limits
	call   uploadintake.CallFunc
}

// cliIngestError marks a failure of the agent ingest step — the only upload
// failure the command audits (a refused or unstaged upload changed nothing).
type cliIngestError struct{ err error }

func (e *cliIngestError) Error() string { return e.err.Error() }
func (e *cliIngestError) Unwrap() error { return e.err }

// cliUpload streams the local file into the owner's Upload Intake staging and
// ingests it at destPath, returning the bytes uploaded. The owner shares one
// staging identity, in-flight cap and byte budget with their File Manager
// uploads (JAB-365).
func cliUpload(ctx context.Context, d cliUploadDeps, ownerID, username, localPath, destPath string, overwrite bool) (int64, error) {
	fi, err := os.Stat(localPath)
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", localPath)
	}
	// Checked before staging so an oversized file is refused without
	// streaming the first max+1 bytes.
	if fi.Size() > d.limits.MaxBytes {
		return 0, cliTooLargeError(fi.Size(), d.limits.MaxBytes)
	}
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	tmpPath, written, err := uploadintake.Stage(ownerID, f, d.limits)
	switch {
	case errors.Is(err, uploadintake.ErrTooManyUploads):
		return 0, fmt.Errorf("%s already has %d uploads in flight — retry after they finish", username, uploadintake.MaxInFlight)
	case errors.Is(err, uploadintake.ErrBudgetExceeded):
		_, staged := uploadintake.Stats(ownerID)
		return 0, fmt.Errorf("this owner's upload staging budget is exceeded (%d bytes staged for %s, budget %d) — retry after their in-flight uploads finish", staged, username, d.limits.Budget)
	case errors.Is(err, uploadintake.ErrTooLarge):
		// The file grew after the size check.
		return 0, cliTooLargeError(fi.Size(), d.limits.MaxBytes)
	case err != nil:
		return 0, err
	}
	// The agent moves the staged file on success; this also covers a
	// success reply that left it behind.
	defer uploadintake.Discard(tmpPath)
	if err := uploadintake.Ingest(ctx, d.call, uploadintake.IngestParams{
		UserID: ownerID, Username: username, TmpPath: tmpPath, DestPath: destPath, Overwrite: overwrite,
	}); err != nil {
		return 0, &cliIngestError{err: err}
	}
	return written, nil
}

func cliTooLargeError(size, max int64) error {
	return fmt.Errorf("file is %d bytes; the configured upload limit is %d — use SFTP/SSH for larger files", size, max)
}
