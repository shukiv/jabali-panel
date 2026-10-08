package dbops

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// regrantTimeout bounds each db.postgres.grant a restore makes.
const regrantTimeout = 60 * time.Second

// RegrantRestoredPostgres grants each PostgreSQL database user the panel has
// on the account's restored databases its access again (GH #1993). A
// restore loads a PostgreSQL database as a new database, whose objects its
// holder role owns: the first database user granted on it takes them over.
// The users go in PGGrantedRoles order, so the first granted user owns the
// restored objects, as after a restore on the Databases page.
//
// names are the PostgreSQL databases the agent says it loaded; a name that
// isn't one of the account's PostgreSQL databases is skipped. errs names each
// grant that failed. notes names each database no user is granted on: its
// objects stay with its holder until one is.
func RegrantRestoredPostgres(ctx context.Context, agent AgentCaller, dbs repository.DatabaseRepository, grants repository.DatabaseUserGrantRepository, users repository.DatabaseUserRepository, accountID string, names []string) (errs, notes []string) {
	if agent == nil || dbs == nil || accountID == "" || len(names) == 0 {
		return nil, nil
	}
	rows, _, err := dbs.ListByUserID(ctx, accountID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return []string{fmt.Sprintf("PostgreSQL databases: granting their users again failed: listing the account's databases: %v", err)}, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, d := range rows {
		if d.Engine != "postgres" || !want[d.Name] {
			continue
		}
		_, roles := PGGrantedRoles(ctx, grants, users, d.ID)
		if len(roles) == 0 {
			notes = append(notes, fmt.Sprintf("db %s (postgres): no database user is granted on it, so its restored tables belong to a role that can't sign in; the first user you grant on it under Databases takes them over", d.Name))
			continue
		}
		for _, role := range roles {
			callCtx, cancel := context.WithTimeout(ctx, regrantTimeout)
			_, err := agent.Call(callCtx, "db.postgres.grant", map[string]any{"db_name": d.Name, "role": role})
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("db %s (postgres): granting %s on it again failed: %v", d.Name, role, err))
			}
		}
	}
	return errs, notes
}
