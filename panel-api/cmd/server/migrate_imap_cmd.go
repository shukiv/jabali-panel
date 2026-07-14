package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/imap"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// imapAccount is one remote IMAP login + its local destination, from
// either the single-account flags or a --csv row.
type imapAccount struct {
	Host     string
	Port     int
	STARTTLS bool
	User     string // remote login (full address, e.g. old@gmail.com)
	Password string // app-password (never logged)
	To       string // local target mailbox (must already exist)
}

// newMigrateImapCmd is the operator entrypoint for GH #390 (Google
// Workspace) / #374 (M365) mail migration, phase A: pull a remote IMAP
// account into a staging Maildir and push it into the target Stalwart
// mailbox via the shared migration.import_mailboxes agent step. Single
// account or a --csv batch. App-passwords are read from stdin or a
// file — never a flag — so they don't leak into `ps` or shell history.
func newMigrateImapCmd() *cobra.Command {
	var host, user, to, passwordFile string
	var port int
	var starttls, passwordStdin, allowPrivate, probeOnly bool
	var csvPath string

	cmd := &cobra.Command{
		Use:   "imap",
		Short: "Migrate a remote IMAP mailbox into a jabali mailbox (GH #390/#374)",
		Long: `Migrate mail over IMAP from Google Workspace / Microsoft 365 (or any
IMAP server) into an existing jabali mailbox. App-password auth.

Single account (password read from stdin):

  printf '%s' "$APP_PASSWORD" | jabali migrate imap \
    --host imap.gmail.com --user old@company.com \
    --to new@example.com --password-stdin

Batch from a CSV (header row: host,user,password,to[,port,starttls]):

  jabali migrate imap --csv accounts.csv

Preview the remote folder layout without migrating anything:

  printf '%s' "$APP_PASSWORD" | jabali migrate imap \
    --host imap.gmail.com --user old@company.com --to x --password-stdin --probe

The target mailbox must already exist (create it via the admin UI or
'jabali user' first); the agent ensures the matching Stalwart account.
Message de-duplication is on Message-ID, so re-running resumes safely.
The remote mailbox is opened read-only (BODY.PEEK) — source flags are
never touched.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			accounts, err := collectIMAPAccounts(cmd.InOrStdin(), csvArgs{
				csvPath:       csvPath,
				host:          host,
				port:          port,
				starttls:      starttls,
				user:          user,
				to:            to,
				passwordStdin: passwordStdin,
				passwordFile:  passwordFile,
			})
			if err != nil {
				return err
			}

			settings := repository.NewServerSettingsRepository(sharedDB)
			stagingBase := migrate.MigrationsRoot(ctx, settings)
			jobs := repository.NewMigrationJobRepository(sharedDB)

			var failures int
			for _, acct := range accounts {
				if probeOnly {
					if err := runIMAPProbe(ctx, cmd.OutOrStdout(), acct, allowPrivate); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ probe %s: %v\n", acct.User, err)
						failures++
					}
					continue
				}
				if err := runIMAPMigrate(ctx, cmd.OutOrStdout(), jobs, stagingBase, acct, allowPrivate); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s → %s: %v\n", acct.User, acct.To, err)
					failures++
				}
			}
			if failures > 0 {
				return fmt.Errorf("%d of %d IMAP account(s) failed", failures, len(accounts))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "remote IMAP host (e.g. imap.gmail.com)")
	cmd.Flags().IntVar(&port, "port", 0, "remote IMAP port (0 = 993 implicit-TLS, or 143 with --starttls)")
	cmd.Flags().BoolVar(&starttls, "starttls", false, "use STARTTLS on 143 instead of implicit TLS on 993")
	cmd.Flags().StringVar(&user, "user", "", "remote IMAP login (full address)")
	cmd.Flags().StringVar(&to, "to", "", "destination jabali mailbox (must already exist)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the app-password from stdin (single account)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the app-password from a file (single account)")
	cmd.Flags().StringVar(&csvPath, "csv", "", "batch: CSV with header host,user,password,to[,port,starttls]")
	cmd.Flags().BoolVar(&allowPrivate, "allow-private", false, "permit RFC1918/loopback targets (migrating from a LAN server)")
	cmd.Flags().BoolVar(&probeOnly, "probe", false, "list the remote folder layout and exit without migrating")
	return cmd
}

// csvArgs bundles the flag inputs so collectIMAPAccounts stays a pure,
// testable function (no cobra dependency).
type csvArgs struct {
	csvPath       string
	host          string
	port          int
	starttls      bool
	user          string
	to            string
	passwordStdin bool
	passwordFile  string
}

// collectIMAPAccounts resolves the account list from either --csv or
// the single-account flags. Exactly one mode must be selected. The
// single-account password comes from stdin or a file, never a flag.
func collectIMAPAccounts(stdin io.Reader, in csvArgs) ([]imapAccount, error) {
	if in.csvPath != "" {
		if in.host != "" || in.user != "" || in.passwordStdin || in.passwordFile != "" {
			return nil, errors.New("--csv is mutually exclusive with --host/--user/--password-stdin/--password-file")
		}
		f, err := os.Open(in.csvPath)
		if err != nil {
			return nil, fmt.Errorf("--csv: %w", err)
		}
		defer f.Close()
		return parseIMAPCSV(f)
	}

	if in.host == "" || in.user == "" || in.to == "" {
		return nil, errors.New("--host, --user and --to are required (or pass --csv)")
	}
	if in.passwordStdin == (in.passwordFile != "") {
		return nil, errors.New("exactly one of --password-stdin or --password-file is required")
	}
	var pw string
	var err error
	if in.passwordStdin {
		pw, err = readSecretLine(stdin)
	} else {
		pw, err = readSecretFile(in.passwordFile)
	}
	if err != nil {
		return nil, err
	}
	if pw == "" {
		return nil, errors.New("empty app-password")
	}
	return []imapAccount{{
		Host: in.host, Port: in.port, STARTTLS: in.starttls,
		User: in.user, Password: pw, To: in.to,
	}}, nil
}

// readSecretLine reads the whole input and trims a single trailing
// newline (so `printf '%s'` and `echo` both work). It intentionally
// does not trim other whitespace — an app-password may contain spaces
// (Google formats them as "abcd efgh ijkl mnop").
func readSecretLine(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--password-file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// parseIMAPCSV reads a header-mapped CSV. Required columns: host, user,
// password, to. Optional: port, starttls. Column order is free.
func parseIMAPCSV(r io.Reader) ([]imapAccount, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // tolerate optional trailing columns

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("csv header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, req := range []string{"host", "user", "password", "to"} {
		if _, ok := col[req]; !ok {
			return nil, fmt.Errorf("csv: missing required column %q (need host,user,password,to)", req)
		}
	}

	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	var out []imapAccount
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("csv line %d: %w", line, err)
		}
		host, ruser, pw, to := get(rec, "host"), get(rec, "user"), get(rec, "password"), get(rec, "to")
		if host == "" && ruser == "" && pw == "" && to == "" {
			continue // blank row
		}
		if host == "" || ruser == "" || pw == "" || to == "" {
			return nil, fmt.Errorf("csv line %d: host, user, password and to are all required", line)
		}
		acct := imapAccount{Host: host, User: ruser, Password: pw, To: to}
		if p := get(rec, "port"); p != "" {
			n, perr := strconv.Atoi(p)
			if perr != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("csv line %d: invalid port %q", line, p)
			}
			acct.Port = n
		}
		if s := get(rec, "starttls"); s != "" {
			acct.STARTTLS = s == "1" || strings.EqualFold(s, "true") || strings.EqualFold(s, "yes")
		}
		out = append(out, acct)
	}
	if len(out) == 0 {
		return nil, errors.New("csv: no account rows")
	}
	return out, nil
}

func (a imapAccount) probeConfig(allowPrivate bool) imap.ProbeConfig {
	return imap.ProbeConfig{
		Host: a.Host, Port: a.Port, STARTTLS: a.STARTTLS,
		Username: a.User, Password: a.Password, AllowPrivate: allowPrivate,
	}
}

// runIMAPProbe enumerates the remote folders and prints the role map so
// the operator can sanity-check before a real run.
func runIMAPProbe(ctx context.Context, out io.Writer, acct imapAccount, allowPrivate bool) error {
	res, err := imap.Probe(ctx, acct.probeConfig(allowPrivate))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  %s — %d folder(s):\n", acct.User, len(res.Folders))
	for _, f := range res.Folders {
		role := f.Role
		if role == "" {
			role = "-"
		}
		sel := ""
		if !f.Selectable {
			sel = " (container, skipped)"
		}
		fmt.Fprintf(out, "    %-32s role=%-8s%s\n", f.Name, role, sel)
	}
	return nil
}

// runIMAPMigrate creates/reuses the migration_jobs row, runs the
// fetch+import for one account, and records the terminal state.
func runIMAPMigrate(ctx context.Context, out io.Writer, jobs repository.MigrationJobRepository, stagingBase string, acct imapAccount, allowPrivate bool) error {
	jobID, err := ensureIMAPJob(ctx, jobs, acct)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  → migrating %s → %s (job %s)\n", acct.User, acct.To, jobID)

	if err := jobs.UpdateState(ctx, jobID, models.MigrationStateRestoring, nil); err != nil {
		return fmt.Errorf("mark job running: %w", err)
	}

	res, imErr := imap.Import(ctx, imap.ImportConfig{
		Account:     acct.probeConfig(allowPrivate),
		TargetEmail: acct.To,
		JobID:       jobID,
		StagingBase: stagingBase,
	}, sharedAgent)
	if imErr != nil {
		msg := imErr.Error()
		_ = jobs.UpdateState(ctx, jobID, models.MigrationStateFailed, &msg)
		return imErr
	}

	if err := jobs.UpdateState(ctx, jobID, models.MigrationStateDone, nil); err != nil {
		return fmt.Errorf("mark job done: %w", err)
	}
	fmt.Fprintf(out, "  ✓ %s: %d message(s), %d byte(s) imported\n", acct.To, res.MessagesImported, res.BytesImported)
	if len(res.Skipped) > 0 {
		fmt.Fprintf(out, "    skipped: %s\n", strings.Join(res.Skipped, ", "))
	}
	return nil
}

// ensureIMAPJob resumes an existing job for (host, user, imap) or
// creates a fresh one. The natural key (source_host, source_user,
// source_kind) makes re-runs idempotent.
func ensureIMAPJob(ctx context.Context, jobs repository.MigrationJobRepository, acct imapAccount) (string, error) {
	if ex, _ := jobs.FindBySource(ctx, models.MigrationSourceIMAP, acct.Host, acct.User); ex != nil {
		return ex.ID, nil
	}
	row := &models.MigrationJob{
		ID:         ids.NewULID(),
		SourceKind: models.MigrationSourceIMAP,
		SourceHost: acct.Host,
		SourceUser: acct.User,
		State:      models.MigrationStatePending,
	}
	if err := jobs.Create(ctx, row); err != nil {
		return "", fmt.Errorf("create migration job: %w", err)
	}
	return row.ID, nil
}
