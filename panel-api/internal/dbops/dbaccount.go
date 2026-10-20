package dbops

import (
	"context"
	"errors"
	"strings"
)

// A database account (a MariaDB user, a PostgreSQL role) is server-wide, but
// a tenant's names are <panel username>_<name>, and panel usernames may hold
// "_": tenant "alice" asking for "x_app" and tenant "alice_x" asking for
// "app" both name alice_x_app. The checks here keep one account to one
// database_users row, so no tenant gets a row for another's account.

// The phpMyAdmin and Adminer sign-in accounts are <panel username>_mysqladmin
// and <panel username>_pgadmin. They are not database_users rows, so a tenant
// row may never carry one of those names.
const (
	mysqladminSuffix = "_mysqladmin"
	pgadminSuffix    = "_pgadmin"
)

var (
	// ErrAccountNameReserved: the name ends like a sign-in account's.
	ErrAccountNameReserved = errors.New("dbops: names ending in _mysqladmin or _pgadmin are reserved for phpMyAdmin and Adminer sign-in")
	// ErrAccountNameTaken: another database user row already names the account.
	ErrAccountNameTaken = errors.New("dbops: a database user with this name already exists")
)

// AccountRows is the slice of the database-user repository the checks need.
type AccountRows interface {
	CountByEngineAndUsername(ctx context.Context, engine, username string) (int64, error)
}

// accountEngine is the engine a database_users row stores: "" is MariaDB.
func accountEngine(engine string) string {
	if engine == "" {
		return "mariadb"
	}
	return engine
}

// SignInAccountName reports whether name ends like a phpMyAdmin or Adminer
// sign-in account's.
func SignInAccountName(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, mysqladminSuffix) || strings.HasSuffix(l, pgadminSuffix)
}

// CheckNewAccount refuses a new database user's final name when it ends like
// a sign-in account's or any row, of any panel user, already names it. The
// agent's create_only refuses an account that exists on the engine without a
// row; this refuses one a row already claims.
func CheckNewAccount(ctx context.Context, rows AccountRows, engine, name string) error {
	if SignInAccountName(name) {
		return ErrAccountNameReserved
	}
	n, err := rows.CountByEngineAndUsername(ctx, accountEngine(engine), name)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrAccountNameTaken
	}
	return nil
}

// SoleAccountRow reports whether a database_users row may change its account
// on the engine (rotate its password, drop it): it is the only row naming the
// account, and the name is not a sign-in account's. Before CheckNewAccount, a
// second tenant could get a row for an existing account; such a row may only
// be deleted, never act on the account.
func SoleAccountRow(ctx context.Context, rows AccountRows, engine, name string) (bool, error) {
	if SignInAccountName(name) {
		return false, nil
	}
	n, err := rows.CountByEngineAndUsername(ctx, accountEngine(engine), name)
	if err != nil {
		return false, err
	}
	return n <= 1, nil
}
