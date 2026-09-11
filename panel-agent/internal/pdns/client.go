// Package pdns is a typed SQL client for the PowerDNS backend schema.
// It talks directly to the jabali_pdns database that install.sh
// provisioned. One package-level global *Client is initialised at
// agent startup by ReadEnvAndConnect(); handlers look it up via the
// Default() accessor.
package pdns

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
)

type Client struct {
	db *sql.DB
}

var (
	defaultMu sync.RWMutex
	defaultCl *Client
)

// Default returns the agent-wide client, or nil if ReadEnvAndConnect
// hasn't been called or failed. Handlers check for nil and return a
// friendly error instead of panicking.
func Default() *Client {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultCl
}

// SetDefault is called from the agent bootstrap once we have a client.
func SetDefault(c *Client) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultCl = c
}

// ReadEnvAndConnect parses /etc/jabali-panel/pdns.env, opens a MySQL
// connection, and returns a ready Client. Returns (nil, err) cleanly
// when the env file is missing so the agent boots even on dev boxes
// without PowerDNS.
func ReadEnvAndConnect() (*Client, error) {
	envPath := os.Getenv("JABALI_PDNS_ENV_FILE")
	if envPath == "" {
		envPath = "/etc/jabali-panel/pdns.env"
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envPath, err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), "\"")
	}
	name, user, pass := env["PDNS_DB_NAME"], env["PDNS_DB_USER"], env["PDNS_DB_PASSWORD"]
	if name == "" || user == "" || pass == "" {
		return nil, fmt.Errorf("%s missing PDNS_DB_NAME/USER/PASSWORD", envPath)
	}
	// M25 Step 6: dial MariaDB over its Debian-default Unix socket. The
	// agent already runs on the same host as MariaDB by definition (it's
	// the host-mutation daemon); a TCP loopback round-trip was overhead
	// rather than capability. Format is the native go-sql-driver/mysql
	// `user:pass@unix(/path)/db?...` form.
	dsn := fmt.Sprintf("%s:%s@unix(/var/run/mysqld/mysqld.sock)/%s?parseTime=true&charset=utf8mb4",
		user, pass, name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Client{db: db}, nil
}

// Close releases the connection pool. Tests use it; production holds
// the client for the process lifetime.
func (c *Client) Close() error {
	return c.db.Close()
}

// Record mirrors the fields we write. Serial is bumped per zone push
// and handled outside this struct.
type Record struct {
	Name     string
	Type     string
	Content  string
	TTL      int
	Priority int
	Disabled bool
}

// UpsertZoneOptions carries per-zone metadata updated alongside the
// record set. Zero values are meaningful: empty slice clears the
// metadata, which is the right behavior when an operator removes ns2.
type UpsertZoneOptions struct {
	AllowAXFRFrom []string // IPs permitted to pull the zone (ns2_ipv4 when set)
	AlsoNotify    []string // NOTIFY targets on zone change (usually ns2_ipv4)
}

// upsertZoneCore is the common logic shared by UpsertZone and
// UpsertZoneWithMeta. It handles zone lookup/creation and record
// upsert. The caller is responsible for the transaction.
func (c *Client) upsertZoneCore(tx *sql.Tx, name string, records []Record) (int64, error) {
	// Find or create the zone row. PowerDNS's unique key is `name`.
	var zoneID int64
	err := tx.QueryRow(`SELECT id FROM domains WHERE name = ?`, name).Scan(&zoneID)
	if err == sql.ErrNoRows {
		res, err := tx.Exec(
			`INSERT INTO domains (name, type) VALUES (?, 'NATIVE')`, name)
		if err != nil {
			return 0, fmt.Errorf("insert zone: %w", err)
		}
		zoneID, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, fmt.Errorf("select zone: %w", err)
	}

	// Wipe and rewrite the record set. Cheap because a typical zone
	// has <50 records. Skipping this for "smart" partial updates would
	// drift PowerDNS from panel DB if records were dropped client-side.
	if _, err := tx.Exec(`DELETE FROM records WHERE domain_id = ?`, zoneID); err != nil {
		return 0, fmt.Errorf("clear records: %w", err)
	}
	ins, err := tx.Prepare(`INSERT INTO records
		(domain_id, name, type, content, ttl, prio, disabled, auth)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	for _, r := range records {
		disabled := 0
		if r.Disabled {
			disabled = 1
		}
		if _, err := ins.Exec(zoneID, r.Name, r.Type, r.Content, r.TTL, r.Priority, disabled); err != nil {
			return 0, fmt.Errorf("insert %s %s: %w", r.Name, r.Type, err)
		}
	}
	return zoneID, nil
}

// setZoneMetadata replaces the given kind's rows for the zone. Empty
// list clears the kind entirely. Runs inside a caller-provided tx so
// the record write and metadata write are atomic.
func (c *Client) setZoneMetadata(tx *sql.Tx, zoneID int64, kind string, contents []string) error {
	if _, err := tx.Exec(`DELETE FROM domainmetadata WHERE domain_id = ? AND kind = ?`, zoneID, kind); err != nil {
		return fmt.Errorf("clear metadata %s: %w", kind, err)
	}
	if len(contents) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO domainmetadata (domain_id, kind, content) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, content := range contents {
		if content == "" {
			continue
		}
		if _, err := stmt.Exec(zoneID, kind, content); err != nil {
			return fmt.Errorf("insert metadata %s: %w", kind, err)
		}
	}
	return nil
}

// UpsertZone replaces the zone's entire record set in a single
// transaction. Safer than partial updates: the operator-facing flow
// always sends the full desired state, and we never leave PowerDNS in
// a half-applied state mid-push.
func (c *Client) UpsertZone(name string, records []Record) (int64, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back on early return

	zoneID, err := c.upsertZoneCore(tx, name, records)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return zoneID, nil
}

// UpsertZoneWithMeta is UpsertZone + metadata, in the same txn.
func (c *Client) UpsertZoneWithMeta(name string, records []Record, opts UpsertZoneOptions) (int64, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back on early return

	zoneID, err := c.upsertZoneCore(tx, name, records)
	if err != nil {
		return 0, err
	}

	if err := c.setZoneMetadata(tx, zoneID, "ALLOW-AXFR-FROM", opts.AllowAXFRFrom); err != nil {
		return 0, err
	}
	if err := c.setZoneMetadata(tx, zoneID, "ALSO-NOTIFY", opts.AlsoNotify); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return zoneID, nil
}

// zoneDeleteStep is one statement in the ordered teardown of a PowerDNS zone.
// Child tables (records, domainmetadata, comments, cryptokeys) all key on
// domain_id and MUST be cleared before the parent `domains` row, since the
// gmysql backend does not necessarily cascade the delete (GH #1620).
type zoneDeleteStep struct {
	table string
	sql   string
}

// zoneDeletePlan returns the ordered DELETE statements for tearing down a zone
// by name. present says which OPTIONAL child tables exist; records and
// domainmetadata are mandatory in every gmysql schema so they always lead.
// Every statement binds exactly one arg — the zone name. Children resolve their
// domain_id through a subquery (defensive: sweeps every same-name `domains` row,
// though the standard schema keeps `name` unique), and the parent `domains`
// delete is always last since the children reference it.
func zoneDeletePlan(present map[string]bool) []zoneDeleteStep {
	const childWhere = ` WHERE domain_id IN (SELECT id FROM domains WHERE name = ?)`
	steps := []zoneDeleteStep{
		{"records", `DELETE FROM records` + childWhere},
		{"domainmetadata", `DELETE FROM domainmetadata` + childWhere},
	}
	if present["comments"] {
		steps = append(steps, zoneDeleteStep{"comments", `DELETE FROM comments` + childWhere})
	}
	if present["cryptokeys"] {
		steps = append(steps, zoneDeleteStep{"cryptokeys", `DELETE FROM cryptokeys` + childWhere})
	}
	steps = append(steps, zoneDeleteStep{"domains", `DELETE FROM domains WHERE name = ?`})
	return steps
}

// presentOptionalTables reports which optional PowerDNS child tables exist in
// the connected schema. records + domainmetadata are mandatory in every gmysql
// schema, so they are not probed. Probing up front (rather than tolerating a
// mid-transaction "table doesn't exist") keeps DeleteZone's transaction free of
// conditional error handling and portable to strict SQL modes.
func (c *Client) presentOptionalTables() (map[string]bool, error) {
	present := map[string]bool{}
	rows, err := c.db.Query(
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = DATABASE() AND table_name IN ('comments','cryptokeys')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		present[t] = true
	}
	return present, rows.Err()
}

// DeleteZone removes a zone and every child row that keys on its domain_id —
// records, domainmetadata, and (when present) comments and cryptokeys — then
// the `domains` row itself, all in one transaction. The old "delete only
// domains" left records + domainmetadata orphaned when the backend didn't
// cascade the delete: duplicate records on re-add and continued resolution off
// the stale rows (GH #1620). Explicit child deletes make teardown independent
// of whether the schema enforces ON DELETE CASCADE. Idempotent — a missing zone
// deletes nothing and returns nil.
//
// cryptokeys is normally managed by `pdnsutil` (see dnssec.go), not raw SQL; we
// still sweep it here because the zone is being destroyed outright, so
// pdnsutil's view of it is moot.
func (c *Client) DeleteZone(name string) error {
	present, err := c.presentOptionalTables()
	if err != nil {
		return fmt.Errorf("probe pdns tables: %w", err)
	}
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed
	for _, s := range zoneDeletePlan(present) {
		if _, err := tx.Exec(s.sql, name); err != nil {
			return fmt.Errorf("delete %s: %w", s.table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// --- Orphan sweep (GH #1620) ------------------------------------------------
//
// DeleteZone (the #1629 fix) tears a zone's child rows down before its `domains`
// row, so a zone delete no longer strands children. That is forward-only: a box
// that deleted a domain BEFORE #1629 still carries the orphaned child rows —
// records with no parent `domains` row. They can keep being served (from the
// backend or its caches) until removed, and collide as "duplicate records" when
// the domain is re-added. ReapOrphans is the one-time cleanup DeleteZone does
// not do.

// orphanWhere selects child rows whose parent `domains` row is gone. A
// self-contained subquery, no bound args. A NULL domain_id yields NULL (not
// TRUE) under NOT IN, so a null-parented row is left untouched — the safe
// direction. Callers MUST refuse to run this when the domains table is empty
// (see DomainsCount): an empty subquery makes NOT IN true for EVERY row.
const orphanWhere = ` WHERE domain_id NOT IN (SELECT id FROM domains)`

// orphanSweepStep is one statement in the orphan cleanup. Both SQL strings are
// compile-time literals (table names cannot be bound parameters); nothing here
// is built from runtime or caller data, so there is no injection surface.
type orphanSweepStep struct {
	table     string
	deleteSQL string
	countSQL  string
}

// orphanSweepPlan is zoneDeletePlan's counterpart with two deliberate
// differences: the predicate is the orphan subquery (not a name-scoped one),
// and there is NO `DELETE FROM domains` step — orphans are defined by the
// absence of that parent, so the `domains` table is only ever read, never
// written. present gates the optional tables exactly as zoneDeletePlan does.
func orphanSweepPlan(present map[string]bool) []orphanSweepStep {
	steps := []orphanSweepStep{
		{"records", `DELETE FROM records` + orphanWhere, `SELECT COUNT(*) FROM records` + orphanWhere},
		{"domainmetadata", `DELETE FROM domainmetadata` + orphanWhere, `SELECT COUNT(*) FROM domainmetadata` + orphanWhere},
	}
	if present["comments"] {
		steps = append(steps, orphanSweepStep{"comments", `DELETE FROM comments` + orphanWhere, `SELECT COUNT(*) FROM comments` + orphanWhere})
	}
	if present["cryptokeys"] {
		steps = append(steps, orphanSweepStep{"cryptokeys", `DELETE FROM cryptokeys` + orphanWhere, `SELECT COUNT(*) FROM cryptokeys` + orphanWhere})
	}
	return steps
}

// DomainsCount returns the number of rows in the pdns `domains` table. The
// orphan sweep refuses to run when this is zero: an empty domains table turns
// the orphan predicate true for every record, so a misconfigured or
// mid-provision connection could otherwise wipe an entire backend. This is the
// DNS analogue of php.pool.reap-orphans refusing a nil keep-list.
func (c *Client) DomainsCount() (int, error) {
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM domains`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count domains: %w", err)
	}
	return n, nil
}

// OrphanCounts returns the number of orphan rows per child table — for the
// dry-run report and to size a sweep before it runs. Only present tables are
// probed.
func (c *Client) OrphanCounts() (map[string]int, error) {
	present, err := c.presentOptionalTables()
	if err != nil {
		return nil, fmt.Errorf("probe pdns tables: %w", err)
	}
	out := map[string]int{}
	for _, s := range orphanSweepPlan(present) {
		var n int
		if err := c.db.QueryRow(s.countSQL).Scan(&n); err != nil {
			return nil, fmt.Errorf("count orphan %s: %w", s.table, err)
		}
		out[s.table] = n
	}
	return out, nil
}

// OrphanRecordNames returns the DISTINCT names of orphan rows in `records` — the
// names whose cached answers must be purged after a sweep. Deleting the row does
// not evict the pdns Auth packet cache or the recursor's forward cache, which
// would keep serving the stale answer for cache-ttl otherwise (the exact "keeps
// resolving after delete" symptom in GH #1620). Collect these BEFORE the delete,
// since afterwards the rows are gone.
func (c *Client) OrphanRecordNames() ([]string, error) {
	rows, err := c.db.Query(`SELECT DISTINCT name FROM records` + orphanWhere)
	if err != nil {
		return nil, fmt.Errorf("list orphan record names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n sql.NullString
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if n.Valid && n.String != "" {
			names = append(names, n.String)
		}
	}
	return names, rows.Err()
}

// ReapOrphans deletes, in one transaction, every child row whose parent
// `domains` row no longer exists (GH #1620) and returns the rows deleted per
// table. It does NOT enforce the domains-nonempty floor itself; the caller MUST
// refuse when DomainsCount() is zero. Cache purge is the caller's job too, since
// only it knows the affected names (OrphanRecordNames, collected first).
func (c *Client) ReapOrphans() (map[string]int, error) {
	present, err := c.presentOptionalTables()
	if err != nil {
		return nil, fmt.Errorf("probe pdns tables: %w", err)
	}
	tx, err := c.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed
	deleted := map[string]int{}
	for _, s := range orphanSweepPlan(present) {
		res, err := tx.Exec(s.deleteSQL)
		if err != nil {
			return nil, fmt.Errorf("delete orphan %s: %w", s.table, err)
		}
		n, _ := res.RowsAffected()
		deleted[s.table] = int(n)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return deleted, nil
}
