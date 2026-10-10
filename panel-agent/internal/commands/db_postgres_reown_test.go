package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #2004: databases restored before GH #1993 keep what the restore created
// owned by postgres. db.postgres.reown_superuser_objects hands it to the
// database's user, or to its holder role when it has none.

func callPgReown(t *testing.T, dbs map[string]string) (dbPgReownResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"databases": dbs})
	out, err := dbPgReownHandler(context.Background(), raw)
	if err != nil {
		return dbPgReownResponse{}, err
	}
	return out.(dbPgReownResponse), nil
}

// reownWorld answers the database list with existing and every psql script
// with script.
func reownWorld(t *testing.T, existing []string, script string) *pgWorld {
	t.Helper()
	return newPgWorld(t, func(line string) (string, bool) {
		if strings.Contains(line, "SELECT datname FROM pg_database") {
			return strings.Join(existing, "\n") + "\n", false
		}
		if strings.HasSuffix(line, "-f -") {
			return script, false
		}
		return "", false
	})
}

func TestPgReown_Refusals(t *testing.T) {
	for name, raw := range map[string]string{
		"no list":            `{}`,
		"the postgres db":    `{"databases":{"postgres":"alice_app"}}`,
		"a template":         `{"databases":{"template1":""}}`,
		"a bad db name":      `{"databases":{"a\"b":"alice_app"}}`,
		"a bad role name":    `{"databases":{"alice_shop":"x;DROP"}}`,
		"a quote in a role":  `{"databases":{"alice_shop":"a'b"}}`,
		"a space in db name": `{"databases":{"alice shop":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := reownWorld(t, []string{"alice_shop"}, pgReownCountMarker+" 1\n"+pgReownDoneMarker+"\n")
			_, err := dbPgReownHandler(context.Background(), json.RawMessage(raw))
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(w.lines) != 0 {
				t.Errorf("ran %q", w.lines)
			}
		})
	}
}

func TestPgReown_HandsSuperuserObjectsToTheDatabaseUser(t *testing.T) {
	w := reownWorld(t, []string{"postgres", "alice_shop"},
		pgReownCountMarker+" 3\n"+pgReownEventTriggerMarker+" 0\n"+pgReownLeftBegin+"\n"+pgReownLeftEnd+"\n"+pgReownDoneMarker+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.Reowned, map[string]int{"alice_shop": 3}) || len(resp.Failed) != 0 {
		t.Errorf("response = %+v", resp)
	}
	i := w.script(t, `\set role 'alice_app'`, "BEGIN;", "COMMIT;", `\gexec`)
	if i < 0 {
		t.Fatalf("no reown script for alice_app; ran %q", w.lines)
	}
	if !strings.Contains(w.lines[i], "-d alice_shop") {
		t.Errorf("reown ran in %q, want alice_shop", w.lines[i])
	}
	s := w.stdin(t, i)
	// Nothing moves unless the role has no server-wide rights.
	if !strings.Contains(s, "NOT (rolsuper OR rolcreaterole OR rolreplication OR rolbypassrls)") || !strings.Contains(s, `\if :role_plain`) {
		t.Errorf("the script doesn't check the role:\n%s", s)
	}
	for _, want := range []string{"ALTER SCHEMA", "MATERIALIZED VIEW", "ALTER ROUTINE", "DOMAIN", "ALTER LARGE OBJECT",
		"ALTER STATISTICS", "ALTER COLLATION", "ALTER CONVERSION", "ALTER OPERATOR", "TEXT SEARCH CONFIGURATION", "TEXT SEARCH DICTIONARY"} {
		if !strings.Contains(s, want) {
			t.Errorf("the script doesn't hand over %s", want)
		}
	}
	if w.script(t, "CREATE ROLE") >= 0 {
		t.Error("a holder was created for a database that has a user")
	}
}

func TestPgReown_NothingSuperuserOwnedChangesNothing(t *testing.T) {
	w := reownWorld(t, []string{"alice_shop", "bob_blog"}, pgReownCountMarker+" 0\n"+pgReownEventTriggerMarker+" 0\n"+pgReownLeftBegin+"\n"+pgReownLeftEnd+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app", "bob_blog": ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || len(resp.Failed) != 0 {
		t.Errorf("response = %+v", resp)
	}
	if w.script(t, "BEGIN;") >= 0 || w.script(t, "CREATE ROLE") >= 0 {
		t.Errorf("changed something with nothing to change: %q", w.lines)
	}
}

func TestPgReown_DatabaseWithoutAUserGoesToItsHolder(t *testing.T) {
	w := reownWorld(t, []string{"bob_blog"}, pgReownCountMarker+" 2\n"+pgReownEventTriggerMarker+" 0\n"+pgReownDoneMarker+"\n")
	resp, err := callPgReown(t, map[string]string{"bob_blog": ""})
	if err != nil {
		t.Fatal(err)
	}
	holder := pgHolderRole("bob_blog")
	if resp.Reowned["bob_blog"] != 2 {
		t.Errorf("response = %+v", resp)
	}
	ensure := w.script(t, `\set holder '`+holder+`'`, `CREATE ROLE :"holder" WITH `+pgHolderAttrs)
	reown := w.script(t, `\set role '`+holder+`'`, "BEGIN;")
	if ensure < 0 || reown < 0 || ensure > reown {
		t.Errorf("want the holder ensured, then the objects handed to it; ran %q", w.lines)
	}
}

func TestPgReown_RefusedRoleMovesNothing(t *testing.T) {
	reownWorld(t, []string{"alice_shop"}, pgReownCountMarker+" 4\n"+pgReownEventTriggerMarker+" 0\n"+pgReownRefusedMarker+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || !strings.Contains(resp.Failed["alice_shop"], "alice_app") {
		t.Errorf("response = %+v", resp)
	}
}

func TestPgReown_ScriptFailureIsReported(t *testing.T) {
	newPgWorld(t, func(line string) (string, bool) {
		if strings.Contains(line, "SELECT datname FROM pg_database") {
			return "alice_shop\n", false
		}
		return pgReownCountMarker + " 1\n" + pgReownEventTriggerMarker + " 0\n", strings.HasSuffix(line, "-f -")
	})
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || resp.Failed["alice_shop"] == "" {
		t.Errorf("response = %+v", resp)
	}
}

func TestPgReown_DatabaseGoneFromTheServerIsSkipped(t *testing.T) {
	w := reownWorld(t, []string{"alice_shop"}, pgReownCountMarker+" 1\n"+pgReownDoneMarker+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_gone": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || len(resp.Failed) != 0 {
		t.Errorf("response = %+v", resp)
	}
	if w.ran("-d alice_gone") >= 0 {
		t.Errorf("connected to a database that isn't there: %q", w.lines)
	}
}

func TestPgReown_ReportsWhatStays(t *testing.T) {
	reownWorld(t, []string{"alice_shop"},
		pgReownCountMarker+" 0\n"+pgReownEventTriggerMarker+" 0\n"+pgReownLeftBegin+"\nshell(text) (language c)\n"+pgReownLeftEnd+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.Left, map[string][]string{"alice_shop": {"shell(text) (language c)"}}) {
		t.Errorf("left = %+v", resp.Left)
	}
}

func TestPgReown_EventTriggersLeaveTheDatabaseAlone(t *testing.T) {
	w := reownWorld(t, []string{"alice_shop"},
		pgReownCountMarker+" 5\n"+pgReownEventTriggerMarker+" 1\n"+pgReownDoneMarker+"\n")
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || !strings.Contains(resp.Failed["alice_shop"], "event trigger") {
		t.Errorf("response = %+v", resp)
	}
	if w.script(t, "BEGIN;") >= 0 {
		t.Error("handed objects over in a database with an event trigger")
	}
}

// Every script runs with pg_catalog as the only schema it looks names up in.
func TestPgReown_ScriptsPinTheSearchPath(t *testing.T) {
	w := reownWorld(t, []string{"alice_shop"}, pgReownCountMarker+" 1\n"+pgReownEventTriggerMarker+" 0\n"+pgReownDoneMarker+"\n")
	if _, err := callPgReown(t, map[string]string{"alice_shop": ""}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, l := range w.lines {
		if strings.HasSuffix(l, "-f -") {
			n++
			if s := w.stdin(t, i); !strings.HasPrefix(s, pgPinSearchPath) {
				t.Errorf("script %d doesn't start by pinning the search path:\n%s", i, s)
			}
		}
	}
	if n != 3 { // count, holder, reown
		t.Errorf("ran %d scripts: %q", n, w.lines)
	}
}

// An event trigger created between the count and the hand-over stops it too.
func TestPgReown_EventTriggerAtHandOverStopsIt(t *testing.T) {
	newPgWorld(t, func(line string) (string, bool) {
		if strings.Contains(line, "SELECT datname FROM pg_database") {
			return "alice_shop\n", false
		}
		return pgReownCountMarker + " 2\n" + pgReownEventTriggerMarker + " 0\n" + pgReownEventTriggerStop + "\n", false
	})
	resp, err := callPgReown(t, map[string]string{"alice_shop": "alice_app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reowned) != 0 || !strings.Contains(resp.Failed["alice_shop"], "event trigger") {
		t.Errorf("response = %+v", resp)
	}
}

// The panel's side of the db.postgres.reown_superuser_objects contract (GH
// #2004): the request the panel sends decodes into this handler's params with
// every field kept, and the response the panel reads is what this handler
// returns. The panel's reconciler decodes the same fixtures.
func TestPgReown_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"db_postgres_reown_request.json", &dbPgReownParams{}},
		{"db_postgres_reown_response.json", &dbPgReownResponse{}},
	} {
		raw, err := os.ReadFile(dir + c.file)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.into); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		again, _ := json.Marshal(c.into)
		var got, want any
		_ = json.Unmarshal(again, &got)
		_ = json.Unmarshal(raw, &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the agent drops or renames fields:\nwant %s\ngot  %s", c.file, raw, again)
		}
	}
}
