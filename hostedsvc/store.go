package hostedsvc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Store wraps the service's SQLite database. One box, one file
// (/var/lib/jabalihosted/svc.db in production); nightly dumps go off-box —
// a lost DB means every issued token is unrecoverable and every fleet cert
// dies at its next renewal.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

var (
	ErrNotFound     = errors.New("hostedsvc: not found")
	ErrCodeInvalid  = errors.New("hostedsvc: code invalid or expired")
	ErrCodeAttempts = errors.New("hostedsvc: too many attempts for this code")
	ErrEmailCap     = errors.New("hostedsvc: label cap reached for this email")
	ErrLabelRevoked = errors.New("hostedsvc: label revoked")
	ErrRateLimited  = errors.New("hostedsvc: rate limited")
)

const (
	codeTTL          = 15 * time.Minute
	codeMaxAttempts  = 8 // 6-digit code + 15 min TTL: cap kills brute force
	emailLabelCap    = 10
	registerMinGap   = 60 * time.Second // resend throttle per email
	reclaimAfterMove = 7 * 24 * time.Hour
)

const schema = `
CREATE TABLE IF NOT EXISTS labels (
  label      TEXT PRIMARY KEY,
  ipv4       TEXT NOT NULL,
  email      TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  moved_at   INTEGER,          -- source IP stopped matching at this time
  revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS ix_labels_email ON labels(email);
CREATE INDEX IF NOT EXISTS ix_labels_ipv4  ON labels(ipv4);
CREATE TABLE IF NOT EXISTS email_codes (
  email      TEXT PRIMARY KEY,
  code_hash  TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0,
  sent_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  at     INTEGER NOT NULL,
  actor  TEXT NOT NULL,      -- email, label, or "admin"
  action TEXT NOT NULL,
  detail TEXT NOT NULL
);
`

// NewStore opens (and migrates) the SQLite database at path.
func NewStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewToken mints the caller-held secret and its stored hash.
func NewToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	return raw, hashToken(raw), nil
}

// NewCode mints a 6-digit verification code.
func NewCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

// PutCode stores a fresh code for the email, enforcing the resend throttle.
func (s *Store) PutCode(email, code string) error {
	now := s.now().Unix()
	var sentAt int64
	err := s.db.QueryRow(`SELECT sent_at FROM email_codes WHERE email = ?`, email).Scan(&sentAt)
	if err == nil && now-sentAt < int64(registerMinGap.Seconds()) {
		return ErrRateLimited
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO email_codes(email, code_hash, expires_at, attempts, sent_at)
		VALUES(?,?,?,0,?)
		ON CONFLICT(email) DO UPDATE SET code_hash=excluded.code_hash,
		  expires_at=excluded.expires_at, attempts=0, sent_at=excluded.sent_at`,
		email, hashToken(code), now+int64(codeTTL.Seconds()), now)
	return err
}

// ConsumeCode verifies a code for the email. Every failed try burns an
// attempt; codeMaxAttempts failures (or expiry) invalidate the code, so a
// 6-digit space cannot be brute-forced inside the TTL.
func (s *Store) ConsumeCode(email, code string) error {
	now := s.now().Unix()
	var hash string
	var expires, attempts int64
	err := s.db.QueryRow(`SELECT code_hash, expires_at, attempts FROM email_codes WHERE email = ?`,
		email).Scan(&hash, &expires, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if now > expires || attempts >= codeMaxAttempts {
		return ErrCodeInvalid
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(hashToken(code))) != 1 {
		_, _ = s.db.Exec(`UPDATE email_codes SET attempts = attempts+1 WHERE email = ?`, email)
		if attempts+1 >= codeMaxAttempts {
			return ErrCodeAttempts
		}
		return ErrCodeInvalid
	}
	_, err = s.db.Exec(`DELETE FROM email_codes WHERE email = ?`, email)
	return err
}

// Label is one issued hostname.
type Label struct {
	Label     string
	IPv4      string
	Email     string
	CreatedAt time.Time
	LastSeen  time.Time
	Revoked   bool
}

// CreateLabel inserts a new label bound to a verified email, enforcing the
// per-email cap. Returns ErrEmailCap when the cap is hit.
func (s *Store) CreateLabel(label, ipv4, email, tokenHash string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM labels WHERE email = ? AND revoked_at IS NULL`,
		email).Scan(&n); err != nil {
		return err
	}
	if n >= emailLabelCap {
		return ErrEmailCap
	}
	now := s.now().Unix()
	_, err := s.db.Exec(`INSERT INTO labels(label, ipv4, email, token_hash, created_at, last_seen)
		VALUES(?,?,?,?,?,?)`, label, ipv4, email, tokenHash, now, now)
	if err != nil {
		return err
	}
	s.Audit(email, "label.create", label+" "+ipv4)
	return nil
}

// DeleteLabel hard-deletes a label row. This is the ROLLBACK path for a claim
// that reserved the row and then failed to publish DNS — nothing was ever
// handed to the caller, so the name must become available again.
//
// RevokeLabel is deliberately NOT a substitute: a revoked name is burned by
// design (never reissued), so using it here would leak one of the 26 collision
// suffixes for that source IP on every transient DNS outage, and would still
// count against the claimer's per-email cap. Do not use this for anything a
// user has actually been given.
func (s *Store) DeleteLabel(label string) error {
	_, err := s.db.Exec(`DELETE FROM labels WHERE label = ?`, label)
	return err
}

// LabelExists reports whether a label row exists (revoked rows count: a
// revoked label's name is burned, never reissued to someone else).
func (s *Store) LabelExists(label string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM labels WHERE label = ?`, label).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LabelByToken resolves the caller's label from its bearer token. THE
// security pivot of the whole API: handlers never accept a label parameter —
// the token IS the identity, so a valid token can only ever act on its own
// label (blueprint invariant 1).
func (s *Store) LabelByToken(rawToken string) (*Label, error) {
	var l Label
	var created, seen int64
	var revoked sql.NullInt64
	err := s.db.QueryRow(`SELECT label, ipv4, email, created_at, last_seen, revoked_at
		FROM labels WHERE token_hash = ?`, hashToken(rawToken)).
		Scan(&l.Label, &l.IPv4, &l.Email, &created, &seen, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, ErrLabelRevoked
	}
	l.CreatedAt = time.Unix(created, 0)
	l.LastSeen = time.Unix(seen, 0)
	return &l, nil
}

// TouchLabel updates last_seen (heartbeat).
func (s *Store) TouchLabel(label string) error {
	_, err := s.db.Exec(`UPDATE labels SET last_seen = ? WHERE label = ?`, s.now().Unix(), label)
	return err
}

// RevokeLabel marks a label revoked (abuse desk / release).
func (s *Store) RevokeLabel(label, actor string) error {
	res, err := s.db.Exec(`UPDATE labels SET revoked_at = ? WHERE label = ? AND revoked_at IS NULL`,
		s.now().Unix(), label)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit(actor, "label.revoke", label)
	return nil
}

// MarkMoved stamps moved_at the first time a heartbeat sees the box's source
// IP stop matching its label. Idempotent by design: only the first detection
// sets the clock (WHERE moved_at IS NULL), so a box that keeps heartbeating
// the old token through a move does NOT reset the reclaim window every tick.
// Revoked rows are skipped — a burned label has no clock to start.
func (s *Store) MarkMoved(label string) error {
	_, err := s.db.Exec(`UPDATE labels SET moved_at = ?
		WHERE label = ? AND moved_at IS NULL AND revoked_at IS NULL`,
		s.now().Unix(), label)
	return err
}

// ClearMoved unstamps moved_at when the box's source IP matches its label
// again. A transient egress change (flapping NAT, a brief re-IP that reverts)
// must never lead to a reclaim, so any pending clock is cancelled the moment
// the box is seen at its own address.
func (s *Store) ClearMoved(label string) error {
	_, err := s.db.Exec(`UPDATE labels SET moved_at = NULL
		WHERE label = ? AND moved_at IS NOT NULL`, label)
	return err
}

// MovedLabelsBefore returns the labels whose source IP stopped matching at or
// before cutoff and are still live (not revoked) — the reaper's worklist,
// oldest move first.
func (s *Store) MovedLabelsBefore(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT label FROM labels
		WHERE moved_at IS NOT NULL AND moved_at <= ? AND revoked_at IS NULL
		ORDER BY moved_at`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ReclaimLabel revokes a moved-out label on the reaper's behalf. Same effect
// as RevokeLabel — the name is burned, never reissued — but audited as
// "label.reclaim" so the abuse desk can tell an automatic move-reclaim apart
// from a punitive revoke.
func (s *Store) ReclaimLabel(label string) error {
	res, err := s.db.Exec(`UPDATE labels SET revoked_at = ? WHERE label = ? AND revoked_at IS NULL`,
		s.now().Unix(), label)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit("reaper", "label.reclaim", label)
	return nil
}

// Audit best-effort appends an audit row; the service never fails an
// operation because the audit insert did.
func (s *Store) Audit(actor, action, detail string) {
	_, _ = s.db.Exec(`INSERT INTO audit(at, actor, action, detail) VALUES(?,?,?,?)`,
		s.now().Unix(), actor, action, detail)
}
