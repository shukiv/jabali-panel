// Package dbtuning is the curated DB-config allowlist shared by
// panel-api (validation, PUT) and panel-agent (render + apply).
// ADR-0098: no raw editor — every tunable is a known, range-checked
// key. Anything outside this registry is rejected.
package dbtuning

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind drives validation + rendering.
type Kind string

const (
	KindInt   Kind = "int"   // plain integer (range-checked)
	KindBytes Kind = "bytes" // integer with a MariaDB size suffix allowed (K/M/G) — stored/rendered verbatim
	KindBool  Kind = "bool"  // "0"/"1" (MariaDB) or "on"/"off" (Postgres)
	KindFloat Kind = "float" // decimal
)

// Param is one curated tunable.
type Param struct {
	Name            string
	Engine          string // "mariadb" | "postgres"
	Kind            Kind
	Min             float64 // for int/float/bytes(numeric part); ignored for bool
	Max             float64
	Unit            string // human hint only ("MB", "s", "")
	RestartRequired bool   // true → applying needs a service restart, not just reload
	Default         string
	Help            string
}

// registry — ADR-0098 v1 key set. Adding a tunable is one entry here.
var registry = []Param{
	// ---- MariaDB (rendered into zz-jabali-tuning.cnf [mysqld]) ----
	{"max_connections", "mariadb", KindInt, 10, 100000, "", true, "151", "Max simultaneous client connections."},
	{"innodb_buffer_pool_size", "mariadb", KindBytes, 5 << 20, 1 << 43, "bytes", true, "134217728", "InnoDB cache. ~60-70% of RAM on a dedicated DB host."},
	{"innodb_log_file_size", "mariadb", KindBytes, 1 << 20, 1 << 40, "bytes", true, "100663296", "InnoDB redo log size. Restart required."},
	{"max_allowed_packet", "mariadb", KindBytes, 1 << 10, 1 << 30, "bytes", false, "16777216", "Largest single packet / row."},
	{"table_open_cache", "mariadb", KindInt, 1, 1000000, "", true, "2000", "Open-table cache slots."},
	{"tmp_table_size", "mariadb", KindBytes, 1 << 10, 1 << 32, "bytes", false, "16777216", "In-memory temp table ceiling."},
	{"slow_query_log", "mariadb", KindBool, 0, 1, "", false, "0", "Enable the slow query log."},
	{"long_query_time", "mariadb", KindFloat, 0, 3600, "s", false, "10", "Slow-query threshold (seconds)."},

	// ---- PostgreSQL (applied via ALTER SYSTEM SET) ----
	{"max_connections", "postgres", KindInt, 10, 100000, "", true, "100", "Max client connections. Restart required."},
	{"shared_buffers", "postgres", KindBytes, 1 << 20, 1 << 43, "bytes", true, "134217728", "Shared memory for caching. ~25% RAM. Restart required."},
	{"work_mem", "postgres", KindBytes, 64 << 10, 1 << 34, "bytes", false, "4194304", "Per-sort/hash memory."},
	{"maintenance_work_mem", "postgres", KindBytes, 1 << 20, 1 << 34, "bytes", false, "67108864", "VACUUM/CREATE INDEX memory."},
	{"effective_cache_size", "postgres", KindBytes, 1 << 20, 1 << 43, "bytes", false, "4294967296", "Planner hint: OS+PG cache. ~50-75% RAM."},
	{"wal_buffers", "postgres", KindBytes, 32 << 10, 1 << 30, "bytes", true, "4194304", "WAL shared buffers. Restart required."},
	{"random_page_cost", "postgres", KindFloat, 0.1, 1000, "", false, "4", "Planner cost of a random page read (SSD ≈ 1.1)."},
}

// List returns the allowlist for an engine (stable order).
func List(engine string) []Param {
	var out []Param
	for _, p := range registry {
		if p.Engine == engine {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func lookup(engine, name string) (Param, bool) {
	for _, p := range registry {
		if p.Engine == engine && p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// parseBytes accepts a plain integer or a K/M/G/T-suffixed value and
// returns the numeric byte count for range-checking. The original
// string is what gets rendered (MariaDB understands the suffix).
func parseBytes(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	// Split leading number from a trailing unit token. Handles both
	// MariaDB single-letter (512M, 1G) and PostgreSQL two-letter
	// (8MB, 4096kB, 4GB) byte units — the latter regressed pg config
	// (M46 smoke: "work_mem: not a number").
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numStr := strings.TrimSpace(s[:i])
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	if numStr == "" {
		return 0, fmt.Errorf("not a number")
	}
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("not a number")
	}
	mult := 1.0
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb":
		mult = 1 << 10
	case "m", "mb":
		mult = 1 << 20
	case "g", "gb":
		mult = 1 << 30
	case "t", "tb":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("bad unit %q", unit)
	}
	return n * mult, nil
}

// Validate checks (engine, name, value) against the allowlist. Returns
// a stable, user-safe error string on rejection (422 upstream).
func Validate(engine, name, value string) error {
	p, ok := lookup(engine, name)
	if !ok {
		return fmt.Errorf("unknown tunable %q for %s", name, engine)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s: value required", name)
	}
	switch p.Kind {
	case KindBool:
		if engine == "mariadb" && value != "0" && value != "1" {
			return fmt.Errorf("%s: must be 0 or 1", name)
		}
		if engine == "postgres" && value != "on" && value != "off" {
			return fmt.Errorf("%s: must be on or off", name)
		}
	case KindInt, KindFloat:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%s: not a number", name)
		}
		if f < p.Min || f > p.Max {
			return fmt.Errorf("%s: out of range [%g, %g]", name, p.Min, p.Max)
		}
	case KindBytes:
		f, err := parseBytes(value)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		if f < p.Min || f > p.Max {
			return fmt.Errorf("%s: out of range [%g, %g] bytes", name, p.Min, p.Max)
		}
	}
	return nil
}

// ValidateSet validates a full desired map; first failure wins.
func ValidateSet(engine string, kv map[string]string) error {
	for k, v := range kv {
		if err := Validate(engine, k, v); err != nil {
			return err
		}
	}
	return nil
}

// RenderMariaDBDropIn produces the managed drop-in file body. Keys are
// sorted so identical input → byte-identical output (drift detection).
func RenderMariaDBDropIn(kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by Jabali (M46, ADR-0098). DO NOT EDIT —\n")
	b.WriteString("# the reconciler rewrites this from db_tuning_settings.\n")
	b.WriteString("[mysqld]\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, kv[k])
	}
	return b.String()
}

// PostgresStatements renders one `ALTER SYSTEM SET` per key (sorted).
// Values are quoted; identifiers are allowlisted so no injection path.
//
// Bytes tunables are normalised to an explicit "<n>B" literal. A memory GUC
// with a unit-less value is read by Postgres in the parameter's base unit
// (8 kB blocks for these params), so a plain byte count like effective_cache_size's
// 4 GiB default (4294967296) is parsed as 4294967296 * 8 kB and overflows the
// int32 GUC ceiling — ALTER SYSTEM then rejects the statement and rolls the
// whole apply back, even on the untouched defaults (GH #968). The "<n>B" form
// also rescues a MariaDB-style single-letter suffix (4G) that Postgres would
// not accept.
func PostgresStatements(kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		val := kv[k]
		if p, ok := lookup("postgres", k); ok && p.Kind == KindBytes {
			if b, err := parseBytes(val); err == nil {
				val = strconv.FormatInt(int64(b), 10) + "B"
			}
		}
		v := strings.ReplaceAll(val, "'", "''")
		out = append(out, fmt.Sprintf("ALTER SYSTEM SET %s = '%s'", k, v))
	}
	return out
}

// RestartRequired reports whether any key in the set needs a full
// service restart (vs a reload).
func RestartRequired(engine string, kv map[string]string) bool {
	for k := range kv {
		if p, ok := lookup(engine, k); ok && p.RestartRequired {
			return true
		}
	}
	return false
}
