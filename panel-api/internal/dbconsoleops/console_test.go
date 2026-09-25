package dbconsoleops

import (
	"context"
	"errors"
	"testing"
)

// issueTrace records every side effect an Issue call makes, in order.
type issueTrace struct {
	steps      []string
	shadowErr  error
	pgErr      error
	privErr    error
	mintErr    error
	gotUser    string
	gotDBID    string
	gotDBName  string
	gotEngine  string
	pmaToken   string
	adminToken string
}

type traceShadow struct{ t *issueTrace }

func (s traceShadow) EnsureShadow(_ context.Context, userID string) error {
	s.t.steps = append(s.t.steps, "shadow:"+userID)
	return s.t.shadowErr
}

type tracePgShadow struct{ t *issueTrace }

func (s tracePgShadow) EnsurePgShadow(_ context.Context, userID string) error {
	s.t.steps = append(s.t.steps, "pg_shadow:"+userID)
	return s.t.pgErr
}

type tracePrivShadow struct{ t *issueTrace }

func (s tracePrivShadow) EnsureShadow(_ context.Context, userID string) error {
	s.t.steps = append(s.t.steps, "priv_shadow:"+userID)
	return s.t.privErr
}

type tracePMA struct{ t *issueTrace }

func (m tracePMA) MintToken(_ context.Context, userID, databaseID, dbName string) (string, error) {
	m.t.steps = append(m.t.steps, "mint_pma")
	m.t.gotUser, m.t.gotDBID, m.t.gotDBName = userID, databaseID, dbName
	if m.t.mintErr != nil {
		return "", m.t.mintErr
	}
	return m.t.pmaToken, nil
}

type traceAdminer struct{ t *issueTrace }

func (m traceAdminer) MintAdminerToken(_ context.Context, userID, databaseID, engine string) (string, error) {
	m.t.steps = append(m.t.steps, "mint_adminer")
	m.t.gotUser, m.t.gotDBID, m.t.gotEngine = userID, databaseID, engine
	if m.t.mintErr != nil {
		return "", m.t.mintErr
	}
	return m.t.adminToken, nil
}

func fullDeps(t *issueTrace) IssueDeps {
	return IssueDeps{
		Shadow:           traceShadow{t},
		PgShadow:         tracePgShadow{t},
		PrivilegedShadow: tracePrivShadow{t},
		PhpMyAdmin:       tracePMA{t},
		Adminer:          traceAdminer{t},
	}
}

func stepsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestIssue_ScopeEngineConsoleMatrix drives every scope × engine × console the
// doors request and pins the account selected, the scope encoded into the token,
// the redirect built, and that nothing is provisioned or minted on a rejection
// (JAB-348 AC1/AC3).
func TestIssue_ScopeEngineConsoleMatrix(t *testing.T) {
	const base = "https://console.example.com"
	cases := []struct {
		name        string
		req         IssueRequest
		wantErr     error
		wantSteps   []string
		wantEngine  string
		wantConsole Console
		wantDBID    string
		wantDBName  string
		wantURL     func(pma, adm string) string
	}{
		{name: "tenant mariadb opens phpMyAdmin by default",
			req:       IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", DBName: "shop", Engine: "mariadb", BaseURL: base},
			wantSteps: []string{"shadow:u1", "mint_pma"}, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin,
			wantDBID: "db1", wantDBName: "shop",
			wantURL: func(pma, _ string) string { return PhpMyAdminRedirect(base, pma, "shop") }},
		{name: "empty engine normalizes to mariadb",
			req:       IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", DBName: "shop", Engine: "  ", BaseURL: base},
			wantSteps: []string{"shadow:u1", "mint_pma"}, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin,
			wantDBID: "db1", wantDBName: "shop",
			wantURL: func(pma, _ string) string { return PhpMyAdminRedirect(base, pma, "shop") }},
		{name: "tenant postgres opens Adminer by default",
			req:       IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db2", DBName: "pg", Engine: "postgres", BaseURL: base},
			wantSteps: []string{"pg_shadow:u1", "mint_adminer"}, wantEngine: "postgres", wantConsole: ConsoleAdminer,
			wantDBID: "db2",
			wantURL:  func(_, adm string) string { return AdminerRedirect(base, adm, "pg", "postgres") }},
		{name: "tenant mariadb in Adminer uses the mariadb shadow",
			req:       IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", DBName: "shop", Engine: "mariadb", Console: ConsoleAdminer, BaseURL: base},
			wantSteps: []string{"shadow:u1", "mint_adminer"}, wantEngine: "mariadb", wantConsole: ConsoleAdminer,
			wantDBID: "db1",
			wantURL:  func(_, adm string) string { return AdminerRedirect(base, adm, "shop", "mariadb") }},
		{name: "phpMyAdmin cannot open a postgres database",
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db2", DBName: "pg", Engine: "postgres", Console: ConsolePhpMyAdmin, BaseURL: base},
			wantErr: ErrConsoleEngine, wantEngine: "postgres", wantConsole: ConsolePhpMyAdmin},
		{name: "unknown engine",
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "sqlite", BaseURL: base},
			wantErr: ErrInvalidEngine, wantEngine: "sqlite"},
		{name: "unknown console",
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb", Console: "pgweb", BaseURL: base},
			wantErr: ErrConsoleEngine, wantEngine: "mariadb", wantConsole: "pgweb"},
		{name: "unknown scope",
			req:     IssueRequest{UserID: "u1", DatabaseID: "db1", Engine: "mariadb", BaseURL: base},
			wantErr: ErrScope, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin},
		{name: "database scope without a database",
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", Engine: "mariadb", BaseURL: base},
			wantErr: ErrScope, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin},
		{name: "database scope cannot carry the admin-all id",
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: AdminAllDatabaseID, Engine: "mariadb", BaseURL: base},
			wantErr: ErrScope, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin},
		{name: "admin-all mariadb signs in as the privileged account",
			req:       IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", DatabaseID: "db1", DBName: "shop", Engine: "mariadb", BaseURL: base},
			wantSteps: []string{"priv_shadow:admin1", "mint_pma"}, wantEngine: "mariadb", wantConsole: ConsolePhpMyAdmin,
			wantDBID: AdminAllDatabaseID, wantDBName: "",
			wantURL: func(pma, _ string) string { return PhpMyAdminRedirect(base, pma, "") }},
		{name: "admin-all postgres uses the installer's superuser",
			req:       IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "postgres", BaseURL: base},
			wantSteps: []string{"mint_adminer"}, wantEngine: "postgres", wantConsole: ConsoleAdminer,
			wantDBID: AdminAllDatabaseID,
			wantURL:  func(_, adm string) string { return AdminerRedirect(base, adm, "", "postgres") }},
		{name: "admin-all opens only the engine's own console",
			req:     IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "mariadb", Console: ConsoleAdminer, BaseURL: base},
			wantErr: ErrConsoleEngine, wantEngine: "mariadb", wantConsole: ConsoleAdminer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &issueTrace{pmaToken: "PMA-TOKEN", adminToken: "ADM-TOKEN"}
			res, err := Issue(context.Background(), fullDeps(tr), tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(tr.steps) != 0 {
					t.Fatalf("a rejection must provision and mint nothing, got %v", tr.steps)
				}
				if Outcome(err) != "" {
					t.Fatalf("Outcome(%v) = %q, want \"\" for a pre-issuance rejection", err, Outcome(err))
				}
			} else if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if !stepsEqual(tr.steps, tc.wantSteps) {
				t.Fatalf("steps = %v, want %v", tr.steps, tc.wantSteps)
			}
			if res.Engine != tc.wantEngine || res.Console != tc.wantConsole {
				t.Fatalf("engine/console = %q/%q, want %q/%q", res.Engine, res.Console, tc.wantEngine, tc.wantConsole)
			}
			if tc.wantErr != nil {
				if res.LoginURL != "" || res.HashPrefix != "" {
					t.Fatalf("a rejection must return no login, got %+v", res)
				}
				return
			}
			if tr.gotUser != tc.req.UserID || tr.gotDBID != tc.wantDBID || tr.gotDBName != tc.wantDBName {
				t.Fatalf("minted user/db/name = %q/%q/%q, want %q/%q/%q",
					tr.gotUser, tr.gotDBID, tr.gotDBName, tc.req.UserID, tc.wantDBID, tc.wantDBName)
			}
			if tc.wantConsole == ConsoleAdminer && tr.gotEngine != tc.wantEngine {
				t.Fatalf("Adminer minted engine %q, want %q", tr.gotEngine, tc.wantEngine)
			}
			if want := tc.wantURL(tr.pmaToken, tr.adminToken); res.LoginURL != want {
				t.Fatalf("login URL = %q, want %q", res.LoginURL, want)
			}
			token := tr.pmaToken
			if tc.wantConsole == ConsoleAdminer {
				token = tr.adminToken
			}
			if res.HashPrefix != TokenAuditPrefix(token) {
				t.Fatalf("hash prefix = %q, want the minted token's prefix", res.HashPrefix)
			}
			if Outcome(err) != OutcomeIssued {
				t.Fatalf("Outcome(nil) = %q, want %q", Outcome(err), OutcomeIssued)
			}
		})
	}
}

// TestIssue_FailuresMintNothingAndMapToOutcomes pins that a provisioning failure
// stops before the mint, that a mint failure returns no login, and that both map
// to their canonical audit outcome while keeping the cause.
func TestIssue_FailuresMintNothingAndMapToOutcomes(t *testing.T) {
	cause := errors.New("agent down")
	cases := []struct {
		name        string
		set         func(*issueTrace)
		req         IssueRequest
		wantErr     error
		wantSteps   []string
		wantOutcome string
	}{
		{name: "tenant mariadb shadow failure",
			set:     func(tr *issueTrace) { tr.shadowErr = cause },
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"},
			wantErr: ErrShadowProvisioning, wantSteps: []string{"shadow:u1"}, wantOutcome: OutcomeEnsureShadowFail},
		{name: "tenant postgres shadow failure",
			set:     func(tr *issueTrace) { tr.pgErr = cause },
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db2", Engine: "postgres"},
			wantErr: ErrShadowProvisioning, wantSteps: []string{"pg_shadow:u1"}, wantOutcome: OutcomeEnsureShadowFail},
		{name: "privileged account failure",
			set:     func(tr *issueTrace) { tr.privErr = cause },
			req:     IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "mariadb"},
			wantErr: ErrShadowProvisioning, wantSteps: []string{"priv_shadow:admin1"}, wantOutcome: OutcomeEnsureShadowFail},
		{name: "phpMyAdmin mint failure",
			set:     func(tr *issueTrace) { tr.mintErr = cause },
			req:     IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"},
			wantErr: ErrMint, wantSteps: []string{"shadow:u1", "mint_pma"}, wantOutcome: OutcomeMintFail},
		{name: "Adminer mint failure",
			set:     func(tr *issueTrace) { tr.mintErr = cause },
			req:     IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "postgres"},
			wantErr: ErrMint, wantSteps: []string{"mint_adminer"}, wantOutcome: OutcomeMintFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &issueTrace{pmaToken: "PMA-TOKEN", adminToken: "ADM-TOKEN"}
			tc.set(tr)
			res, err := Issue(context.Background(), fullDeps(tr), tc.req)
			if !errors.Is(err, tc.wantErr) || !errors.Is(err, cause) {
				t.Fatalf("err = %v, want %v wrapping the cause", err, tc.wantErr)
			}
			if !stepsEqual(tr.steps, tc.wantSteps) {
				t.Fatalf("steps = %v, want %v", tr.steps, tc.wantSteps)
			}
			if res.LoginURL != "" || res.HashPrefix != "" {
				t.Fatalf("a failed issuance must return no login, got %+v", res)
			}
			if got := Outcome(err); got != tc.wantOutcome {
				t.Fatalf("Outcome = %q, want %q", got, tc.wantOutcome)
			}
		})
	}

	t.Run("a mint error prints the minter's text alone", func(t *testing.T) {
		tr := &issueTrace{mintErr: cause}
		_, err := Issue(context.Background(), fullDeps(tr), IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"})
		if err == nil || err.Error() != cause.Error() {
			t.Fatalf("err text = %v, want %q", err, cause.Error())
		}
	})
}

// TestIssue_UnwiredDependencyProvisionsNothing pins that a door missing a
// dependency its request reaches fails before any side effect.
func TestIssue_UnwiredDependencyProvisionsNothing(t *testing.T) {
	cases := []struct {
		name string
		drop func(*IssueDeps)
		req  IssueRequest
	}{
		{name: "no phpMyAdmin minter",
			drop: func(d *IssueDeps) { d.PhpMyAdmin = nil },
			req:  IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"}},
		{name: "no Adminer minter",
			drop: func(d *IssueDeps) { d.Adminer = nil },
			req:  IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db2", Engine: "postgres"}},
		{name: "no tenant mariadb shadow",
			drop: func(d *IssueDeps) { d.Shadow = nil },
			req:  IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"}},
		{name: "no tenant postgres shadow",
			drop: func(d *IssueDeps) { d.PgShadow = nil },
			req:  IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db2", Engine: "postgres"}},
		{name: "no privileged account provisioner",
			drop: func(d *IssueDeps) { d.PrivilegedShadow = nil },
			req:  IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "mariadb"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &issueTrace{pmaToken: "PMA-TOKEN", adminToken: "ADM-TOKEN"}
			d := fullDeps(tr)
			tc.drop(&d)
			_, err := Issue(context.Background(), d, tc.req)
			if !errors.Is(err, ErrIssueDeps) {
				t.Fatalf("err = %v, want ErrIssueDeps", err)
			}
			if len(tr.steps) != 0 {
				t.Fatalf("an unwired issuance must provision and mint nothing, got %v", tr.steps)
			}
		})
	}
}

// A tenant door never reaches the privileged account, and the admin-all
// PostgreSQL login needs no tenant shadow: each scope wires only what it uses.
func TestIssue_ScopesNeedOnlyTheirOwnDeps(t *testing.T) {
	tr := &issueTrace{pmaToken: "PMA-TOKEN", adminToken: "ADM-TOKEN"}
	tenant := IssueDeps{Shadow: traceShadow{tr}, PgShadow: tracePgShadow{tr}, PhpMyAdmin: tracePMA{tr}, Adminer: traceAdminer{tr}}
	if _, err := Issue(context.Background(), tenant, IssueRequest{Scope: ScopeDatabase, UserID: "u1", DatabaseID: "db1", Engine: "mariadb"}); err != nil {
		t.Fatalf("tenant issuance without a privileged provisioner: %v", err)
	}
	if _, err := Issue(context.Background(), IssueDeps{Adminer: traceAdminer{tr}}, IssueRequest{Scope: ScopeAdminAll, UserID: "admin1", Engine: "postgres"}); err != nil {
		t.Fatalf("admin-all postgres issuance with only the Adminer minter: %v", err)
	}
}
