package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDBSizeHandler_EngineRouting(t *testing.T) {
	orig := dbSizeExec
	defer func() { dbSizeExec = orig }()

	var gotName, gotArgs string
	dbSizeExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, strings.Join(args, " ")
		return []byte("123456\n"), nil
	}
	call := func(engine string) dbSizeResponse {
		raw, _ := json.Marshal(map[string]string{"db_name": "mydb", "engine": engine})
		out, err := dbSizeHandler(context.Background(), raw)
		if err != nil {
			t.Fatalf("handler err: %v", err)
		}
		return out.(dbSizeResponse)
	}

	t.Run("postgres uses pg_database_size", func(t *testing.T) {
		resp := call("postgres")
		if gotName != "sudo" || !strings.Contains(gotArgs, "psql") || !strings.Contains(gotArgs, "pg_database_size('mydb')") {
			t.Errorf("postgres path wrong: %s %s", gotName, gotArgs)
		}
		if resp.SizeBytes != 123456 {
			t.Errorf("size = %d, want 123456", resp.SizeBytes)
		}
	})

	t.Run("mariadb uses information_schema", func(t *testing.T) {
		resp := call("mariadb")
		if gotName != "mysql" || !strings.Contains(gotArgs, "information_schema.tables") {
			t.Errorf("mariadb path wrong: %s %s", gotName, gotArgs)
		}
		if resp.SizeBytes != 123456 {
			t.Errorf("size = %d, want 123456", resp.SizeBytes)
		}
	})

	t.Run("empty engine defaults to mariadb (pre-#1005 panel)", func(t *testing.T) {
		call("")
		if gotName != "mysql" {
			t.Errorf("empty engine must use the MariaDB path, got %s", gotName)
		}
	})
}

func TestDBSizeHandler_DegradesToZero(t *testing.T) {
	orig := dbSizeExec
	defer func() { dbSizeExec = orig }()
	dbSizeExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	raw, _ := json.Marshal(map[string]string{"db_name": "mydb", "engine": "postgres"})
	out, err := dbSizeHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler must not error on a failed size query: %v", err)
	}
	if out.(dbSizeResponse).SizeBytes != 0 {
		t.Errorf("expected 0 B on exec error, got %d", out.(dbSizeResponse).SizeBytes)
	}
}

func TestParseSizeOutput(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"123\n", 123}, {"  456  ", 456}, {"", 0}, {"notanumber", 0}, {"0", 0},
	}
	for _, c := range cases {
		if got := parseSizeOutput([]byte(c.in)); got != c.want {
			t.Errorf("parseSizeOutput(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDBSizeHandler_InvalidName(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"db_name": "bad name!", "engine": "postgres"})
	if _, err := dbSizeHandler(context.Background(), raw); err == nil {
		t.Error("expected an error for an invalid database name")
	}
}
