package backupmetadata

import internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"

// dropSkippedParts takes out of m the parts a restore leaves out because they
// are turned off on this server (GH #1993): with d.SkipMail every mailbox
// (with its autoresponder and shares) and forwarder, and with d.SkipPostgres
// every PostgreSQL database and database user. The restore door reports
// what it left out.
func dropSkippedParts(m *internalbackup.AccountMetadata, d Deps) {
	if d.SkipMail {
		for i := range m.Domains {
			m.Domains[i].Mailboxes = nil
			m.Domains[i].Forwarders = nil
		}
	}
	if d.SkipPostgres {
		users := m.DatabaseUsers[:0:0]
		for _, du := range m.DatabaseUsers {
			if restoredDBUserEngine(du, m.Databases) != "postgres" {
				users = append(users, du)
			}
		}
		dbs := m.Databases[:0:0]
		for _, db := range m.Databases {
			if db.Engine != "postgres" {
				dbs = append(dbs, db)
			}
		}
		m.DatabaseUsers, m.Databases = users, dbs
	}
}
