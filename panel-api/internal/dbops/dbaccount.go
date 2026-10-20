package dbops

import (
	"context"
	"errors"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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
	// ErrAccountNotOwned: another panel user's row, or more than one row,
	// names the account.
	ErrAccountNotOwned = errors.New("dbops: a database user with this name belongs to another account")
)

// AccountRows is the slice of the database-user repository the checks need.
type AccountRows interface {
	CountByEngineAndUsername(ctx context.Context, engine, username string) (int64, error)
}

// AccountOwners is the slice of the database-user repository ImportedAccount
// needs.
type AccountOwners interface {
	ListByEngineAndUsername(ctx context.Context, engine, username string) ([]models.DatabaseUser, error)
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

// ImportedAccount says how an import into panel user ownerID may use an
// account name read from the imported files. A nil row: no row names it, so
// the import may only create it, with create_only, which the agent refuses for
// an account the engine already has, and then records it as ownerID's. A row:
// the only row naming it, which is ownerID's, so the import may set its
// password. A sign-in account's name, and one another panel user's row names,
// are refused.
func ImportedAccount(ctx context.Context, rows AccountOwners, engine, name, ownerID string) (*models.DatabaseUser, error) {
	if SignInAccountName(name) {
		return nil, ErrAccountNameReserved
	}
	found, err := rows.ListByEngineAndUsername(ctx, accountEngine(engine), name)
	if err != nil {
		return nil, err
	}
	switch {
	case len(found) == 0:
		return nil, nil
	case len(found) == 1 && found[0].UserID == ownerID:
		return &found[0], nil
	}
	return nil, ErrAccountNotOwned
}
