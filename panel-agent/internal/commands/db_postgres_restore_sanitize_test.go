package commands

import (
	"strings"
	"testing"
)

func sanitize(t *testing.T, in string) string {
	t.Helper()
	var sb strings.Builder
	if err := sanitizePgPlainDump(strings.NewReader(in), &sb); err != nil {
		t.Fatalf("sanitizePgPlainDump: %v", err)
	}
	return sb.String()
}

func TestSanitize_DropsOwnershipAndPrivilegeStatements(t *testing.T) {
	in := strings.Join([]string{
		"SET statement_timeout = 0;",
		"SET search_path = public;",
		"CREATE TABLE public.widgets (id integer NOT NULL, name text);",
		"ALTER TABLE public.widgets OWNER TO postgres;",
		"ALTER SCHEMA public OWNER TO pg_database_owner;",
		"GRANT ALL ON TABLE public.widgets TO some_role;",
		"REVOKE ALL ON SCHEMA public FROM PUBLIC;",
		"SET SESSION AUTHORIZATION 'postgres';",
		"RESET SESSION AUTHORIZATION;",
		"SET ROLE postgres;",
		"CREATE INDEX widgets_name_idx ON public.widgets (name);",
		"",
	}, "\n")
	got := sanitize(t, in)

	for _, dropped := range []string{"OWNER TO", "GRANT ALL", "REVOKE ALL", "SESSION AUTHORIZATION", "SET ROLE postgres"} {
		if strings.Contains(got, dropped) {
			t.Errorf("expected %q to be stripped, still present:\n%s", dropped, got)
		}
	}
	for _, kept := range []string{"statement_timeout", "search_path", "CREATE TABLE public.widgets", "CREATE INDEX widgets_name_idx"} {
		if !strings.Contains(got, kept) {
			t.Errorf("expected %q to be preserved, missing:\n%s", kept, got)
		}
	}
}

func TestSanitize_PreservesCopyDataLookalikes(t *testing.T) {
	// A COPY data block whose ROWS contain text that looks like droppable
	// statements MUST pass through verbatim — dropping it would corrupt data.
	in := strings.Join([]string{
		"COPY public.audit (id, msg) FROM stdin;",
		"1\tALTER TABLE x OWNER TO postgres;",
		"2\tGRANT ALL ON y TO z;",
		"3\tSET ROLE postgres;",
		`\.`,
		"ALTER TABLE public.audit OWNER TO postgres;", // this one (real stmt) drops
		"",
	}, "\n")
	got := sanitize(t, in)

	if !strings.Contains(got, "1\tALTER TABLE x OWNER TO postgres;") ||
		!strings.Contains(got, "2\tGRANT ALL ON y TO z;") ||
		!strings.Contains(got, "3\tSET ROLE postgres;") {
		t.Errorf("COPY data rows were altered:\n%s", got)
	}
	// The real ALTER OWNER after the block must be gone.
	if strings.Contains(got, "ALTER TABLE public.audit OWNER TO postgres;") {
		t.Errorf("real ALTER OWNER after COPY block not stripped:\n%s", got)
	}
}

func TestSanitize_PreservesDollarQuotedFunctionBody(t *testing.T) {
	// A function body legitimately containing GRANT / OWNER TO must survive.
	in := strings.Join([]string{
		"CREATE FUNCTION public.f() RETURNS void AS $func$",
		"BEGIN",
		"  -- GRANT ALL ON t TO r;",
		"  EXECUTE 'ALTER TABLE t OWNER TO r';",
		"END;",
		"$func$ LANGUAGE plpgsql;",
		"ALTER FUNCTION public.f() OWNER TO postgres;", // real, drops
		"",
	}, "\n")
	got := sanitize(t, in)

	for _, kept := range []string{
		"-- GRANT ALL ON t TO r;",
		"EXECUTE 'ALTER TABLE t OWNER TO r';",
		"$func$ LANGUAGE plpgsql;",
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("function body line dropped: %q\n%s", kept, got)
		}
	}
	if strings.Contains(got, "ALTER FUNCTION public.f() OWNER TO postgres;") {
		t.Errorf("real ALTER FUNCTION OWNER not stripped:\n%s", got)
	}
}

func TestSanitize_DollarDollarBody(t *testing.T) {
	in := strings.Join([]string{
		"CREATE FUNCTION g() RETURNS int AS $$",
		"  SELECT 1; -- GRANT nonsense here",
		"$$ LANGUAGE sql;",
		"",
	}, "\n")
	got := sanitize(t, in)
	if !strings.Contains(got, "SELECT 1; -- GRANT nonsense here") {
		t.Errorf("$$-quoted body corrupted:\n%s", got)
	}
}

func TestSanitize_KeepsMultiLineLookalike(t *testing.T) {
	// A line that matches a prefix but does NOT terminate the statement (no
	// trailing ';') must be kept — never half-cut a multi-line statement.
	in := "GRANT ALL ON TABLE x\n  TO some_role;\n"
	got := sanitize(t, in)
	if !strings.Contains(got, "GRANT ALL ON TABLE x") {
		t.Errorf("multi-line GRANT first line wrongly dropped:\n%s", got)
	}
}

func TestSanitize_PanelStyleDumpUnchanged(t *testing.T) {
	// A panel-style dump (already --no-owner --no-privileges) has nothing to
	// strip and must pass through byte-for-byte.
	in := strings.Join([]string{
		"SET statement_timeout = 0;",
		"CREATE TABLE public.t (id integer);",
		"COPY public.t (id) FROM stdin;",
		"1",
		"2",
		`\.`,
		"CREATE INDEX t_id ON public.t (id);",
		"",
	}, "\n")
	got := sanitize(t, in)
	if got != in {
		t.Errorf("panel-style dump was modified:\nwant:\n%s\ngot:\n%s", in, got)
	}
}
