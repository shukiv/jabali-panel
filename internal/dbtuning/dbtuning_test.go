package dbtuning

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name, engine, param, value string
		wantErr                    bool
	}{
		{"unknown key", "mariadb", "evil_key", "1", true},
		{"wrong engine", "postgres", "innodb_buffer_pool_size", "1M", true},
		{"int ok", "mariadb", "max_connections", "200", false},
		{"int below min", "mariadb", "max_connections", "1", true},
		{"int above max", "mariadb", "max_connections", "999999999", true},
		{"int not a number", "mariadb", "max_connections", "lots", true},
		{"bytes plain ok", "mariadb", "innodb_buffer_pool_size", "536870912", false},
		{"bytes suffix ok", "mariadb", "innodb_buffer_pool_size", "512M", false},
		{"bytes below min", "mariadb", "innodb_buffer_pool_size", "1K", true},
		{"bool maria ok", "mariadb", "slow_query_log", "1", false},
		{"bool maria bad", "mariadb", "slow_query_log", "true", true},
		{"bool pg ok", "postgres", "max_connections", "150", false},
		{"float ok", "mariadb", "long_query_time", "2.5", false},
		{"float oob", "mariadb", "long_query_time", "99999", true},
		{"empty value", "mariadb", "max_connections", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.engine, tc.param, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%s,%s,%q) err=%v want wantErr=%v",
					tc.engine, tc.param, tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestRenderMariaDBDropInDeterministic(t *testing.T) {
	in := map[string]string{"max_connections": "200", "innodb_buffer_pool_size": "512M"}
	a := RenderMariaDBDropIn(in)
	b := RenderMariaDBDropIn(in)
	if a != b {
		t.Fatal("render not deterministic — drift detection would thrash")
	}
	if !strings.Contains(a, "[mysqld]") ||
		!strings.Contains(a, "max_connections = 200") ||
		!strings.Contains(a, "innodb_buffer_pool_size = 512M") {
		t.Fatalf("unexpected drop-in body:\n%s", a)
	}
	// Keys must be sorted so identical input → identical bytes.
	if strings.Index(a, "innodb_buffer_pool_size") > strings.Index(a, "max_connections") {
		t.Fatal("keys not sorted — byte-identical guarantee broken")
	}
}

func TestPostgresStatementsQuoting(t *testing.T) {
	stmts := PostgresStatements(map[string]string{"work_mem": "4MB", "max_connections": "150"})
	if len(stmts) != 2 {
		t.Fatalf("want 2 statements, got %d", len(stmts))
	}
	// sorted: max_connections before work_mem
	if !strings.HasPrefix(stmts[0], "ALTER SYSTEM SET max_connections = '150'") {
		t.Fatalf("unexpected/unsorted: %q", stmts[0])
	}
}

// TestPostgresBytesCarryUnit pins GH #968: memory GUCs must reach ALTER SYSTEM
// with an explicit byte unit. Without it, effective_cache_size's 4 GiB default
// (4294967296, unit-less) is read as 8 kB blocks and overflows the int32 GUC
// ceiling, so the whole apply is rejected and rolled back.
func TestPostgresBytesCarryUnit(t *testing.T) {
	stmts := PostgresStatements(map[string]string{
		"effective_cache_size": "4294967296", // 4 GiB default, plain bytes
		"shared_buffers":       "4G",          // MariaDB-style suffix Postgres rejects
		"max_connections":      "150",         // KindInt — must stay verbatim
	})
	got := strings.Join(stmts, "\n")
	// bytes tunables normalised to an explicit "<n>B" literal
	if !strings.Contains(got, "ALTER SYSTEM SET effective_cache_size = '4294967296B'") {
		t.Errorf("effective_cache_size not rendered with a byte unit:\n%s", got)
	}
	if !strings.Contains(got, "ALTER SYSTEM SET shared_buffers = '4294967296B'") {
		t.Errorf("shared_buffers 4G not normalised to bytes:\n%s", got)
	}
	// the old, broken form must not survive
	if strings.Contains(got, "effective_cache_size = '4294967296'") {
		t.Errorf("unit-less byte value still emitted (the #968 bug):\n%s", got)
	}
	// non-bytes params untouched
	if !strings.Contains(got, "ALTER SYSTEM SET max_connections = '150'") {
		t.Errorf("max_connections should stay verbatim:\n%s", got)
	}
}

func TestRestartRequired(t *testing.T) {
	if !RestartRequired("mariadb", map[string]string{"max_connections": "200"}) {
		t.Fatal("max_connections is restart-required")
	}
	if RestartRequired("mariadb", map[string]string{"slow_query_log": "1"}) {
		t.Fatal("slow_query_log is NOT restart-required")
	}
}

func TestListEngineScoped(t *testing.T) {
	for _, p := range List("mariadb") {
		if p.Engine != "mariadb" {
			t.Fatalf("List(mariadb) leaked %s key %s", p.Engine, p.Name)
		}
	}
	if len(List("postgres")) == 0 {
		t.Fatal("postgres allowlist empty")
	}
}

func TestParseBytesUnits(t *testing.T) {
	ok := map[string]float64{
		"256": 256, "256B": 256, "8MB": 8 << 20, "4096kB": 4096 << 10,
		"4GB": 4 << 30, "512M": 512 << 20, "1G": 1 << 30, "2T": 2 << 40,
		"128 MB": 128 << 20,
	}
	for in, want := range ok {
		got, err := parseBytes(in)
		if err != nil || got != want {
			t.Fatalf("parseBytes(%q)=%v,%v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MB", "8XB", "abc", "8 9 M"} {
		if _, err := parseBytes(bad); err == nil {
			t.Fatalf("parseBytes(%q) should error", bad)
		}
	}
	// The regression: pg byte keys with two-letter units must validate.
	if err := Validate("postgres", "work_mem", "8MB"); err != nil {
		t.Fatalf("work_mem=8MB must be valid: %v", err)
	}
	if err := Validate("mariadb", "innodb_buffer_pool_size", "512M"); err != nil {
		t.Fatalf("innodb_buffer_pool_size=512M must be valid: %v", err)
	}
}
