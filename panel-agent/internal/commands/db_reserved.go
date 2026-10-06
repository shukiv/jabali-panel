package commands

import (
	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dbreserve"
)

// The database verbs never touch this server's own databases and database
// accounts (internal/dbreserve), whatever the caller sends: the panel checks
// names first, and this holds when one gets past it — an admin-only name typed
// into the CLI, or a backup restore driven by an uploaded file (GH #1993).

// reservedDatabaseRefusal is the error for a database name the verbs refuse,
// or nil.
func reservedDatabaseRefusal(name string) error {
	if dbreserve.Database(name) {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "refused: " + name + " is one of this server's own databases"}
	}
	return nil
}

// reservedDBUserRefusal is the error for a database user name the verbs
// refuse, or nil.
func reservedDBUserRefusal(name string) error {
	if dbreserve.User(name) {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "refused: " + name + " is one of this server's own database accounts"}
	}
	return nil
}
