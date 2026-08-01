package repository

import (
	"strings"

	"gorm.io/gorm"
)

// ListOptions is the common request shape for paginated list queries that
// also want free-text search and single-column sort. All repositories'
// List* methods take this struct; the zero value (offset=0, limit=0, no
// search, no sort) is valid and produces the historical "give me
// everything in insertion order" behaviour — callers that want a default
// page size should fill in Limit themselves.
//
// Security: Sort is whitelist-matched against a per-repo allowlist before
// it ever hits the SQL. An unknown Sort value silently falls back to the
// repo's default ordering (usually created_at DESC). Search is always
// parameterised — the only thing the caller controls is the LIKE pattern.
type ListOptions struct {
	Offset int
	Limit  int
	Search string // free-text, matched via ILIKE across the repo's search columns
	Sort   string // column name (will be whitelist-validated per repo)
	Order  string // "asc" | "desc" — anything else coerces to "desc"
	// IsAdmin is a user-repo-only filter — when non-nil, restricts the
	// result set to users matching the flag. Other repos ignore it. Lives
	// on the shared struct only because every list flow already plumbs
	// ListOptions end-to-end and adding a parallel filter struct for one
	// repo isn't worth the duplication.
	IsAdmin *bool
	// Suspended — user-repo-only; non-nil restricts to suspended/active.
	Suspended *bool
	// OmitHeavyColumns — domain-repo-only; when true, List/ListByUserID
	// skip the wide TEXT columns no list row renders (DKIM material, mail
	// disclaimer, nginx safe-options JSON). Detail GETs and the reconciler
	// always read full rows. Other repos ignore it.
	OmitHeavyColumns bool
}

// ListCols tells applyListOptions which columns are searchable (free-text
// LIKE) and which are sortable (ORDER BY). Both lists are whitelists —
// empty string keys are impossible so SQL injection via Sort/Search names
// is structurally prevented.
type ListCols struct {
	Search      []string // columns included in the ILIKE OR-chain
	Sort        []string // columns accepted as the sort key
	DefaultSort string   // column used when Sort is empty or not in allowlist
}

// applyListOptions layers search/sort/pagination onto a base *gorm.DB
// scope. It is used by every repo's List method to keep behaviour (and
// whitelist enforcement) identical across resources.
//
// Intentional choices:
//   - Uses LIKE not ILIKE because MariaDB's default collations are
//     case-insensitive already; ILIKE would be Postgres-only.
//   - Sort direction defaults to DESC on the default column; explicit
//     Sort+Order can flip it.
//   - Empty Limit leaves the query unbounded (callers should clamp).
// maxSearchLen is the hard cap on free-text search length. 128 chars is
// ample for realistic email/name fragment searches and prevents a
// caller from forcing giant LIKE scans with a megabyte-long pattern.
const maxSearchLen = 128

// likeEscape backslash-escapes the LIKE metacharacters %, _, and the
// escape character itself, so a user search for "50% off" matches
// literally rather than acting as a wildcard. Used together with
// ESCAPE '\\' on the LIKE clause.
func likeEscape(s string) string {
	return likeReplacer.Replace(s)
}

var likeReplacer = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

func applyListOptions(base *gorm.DB, opts ListOptions, cols ListCols) *gorm.DB {
	q := base

	if s := strings.TrimSpace(opts.Search); s != "" && len(cols.Search) > 0 {
		// Cap the search length so a pathological caller can't make us
		// do 256KB-string LIKE scans on every list request. 128 chars is
		// plenty for "find user by email fragment" style searches.
		if len(s) > maxSearchLen {
			s = s[:maxSearchLen]
		}
		// Escape the LIKE wildcards (%, _) so a user searching for
		// "50% off" doesn't accidentally match everything. `\` is the
		// default LIKE escape character in MariaDB; we declare it
		// explicitly with ESCAPE '\\' per clause so the behaviour is
		// not driven by the session-level sql_mode.
		pattern := "%" + likeEscape(s) + "%"
		clauses := make([]string, 0, len(cols.Search))
		args := make([]any, 0, len(cols.Search))
		for _, c := range cols.Search {
			clauses = append(clauses, c+" LIKE ? ESCAPE '\\\\'")
			args = append(args, pattern)
		}
		q = q.Where(strings.Join(clauses, " OR "), args...)
	}

	sort := pickSort(opts.Sort, cols.Sort, cols.DefaultSort)
	order := "DESC"
	if strings.EqualFold(opts.Order, "asc") {
		order = "ASC"
	}
	if sort != "" {
		q = q.Order(sort + " " + order)
	}

	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	return q
}

// pickSort returns the requested column if it's in the allowlist, or the
// default column otherwise. Empty string means "don't order" — callers
// that pass an empty DefaultSort get that.
func pickSort(requested string, allowed []string, fallback string) string {
	r := strings.ToLower(strings.TrimSpace(requested))
	if r == "" {
		return fallback
	}
	for _, c := range allowed {
		if strings.EqualFold(c, r) {
			return c
		}
	}
	return fallback
}
