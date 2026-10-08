package backup

import "sort"

// BundleSummary is what a restore's preflight needs to know about an account
// backup's metadata bundle (GH #1993). It holds nothing secret: the bundle
// itself carries password hashes, so the agent hands the panel this instead,
// and the panel can show it in the browser.
type BundleSummary struct {
	// PHPVersions are the PHP versions of the backup's pools.
	PHPVersions []string `json:"php_versions"`
	// PHPExtensions are the extensions the source server had enabled for
	// each of those versions; nil when the backup doesn't record them.
	PHPExtensions map[string][]string `json:"php_extensions,omitempty"`
	// PostgresDatabases names the backup's PostgreSQL databases.
	PostgresDatabases []string `json:"postgres_databases"`
	// PostgresUsers counts its PostgreSQL database users.
	PostgresUsers int `json:"postgres_users"`
	// Mailboxes and Forwarders count the backup's mail.
	Mailboxes  int `json:"mailboxes"`
	Forwarders int `json:"forwarders"`
	// DNSRecords counts the custom DNS records of its domains.
	DNSRecords int `json:"dns_records"`
	// DockerApps names the account's own Docker apps (server-level ones are
	// never restored from a file).
	DockerApps []string `json:"docker_apps"`
}

// summaryListMax bounds each list in a summary: a bundle is the uploader's
// file, and the preflight only needs to name a few.
const summaryListMax = 50

// summaryNameMax bounds each name in a summary.
const summaryNameMax = 64

// SummarizeMetadata is m's BundleSummary.
func SummarizeMetadata(m *AccountMetadata) BundleSummary {
	s := BundleSummary{PHPVersions: []string{}, PostgresDatabases: []string{}, DockerApps: []string{}}
	if m == nil {
		return s
	}
	versions := map[string]bool{}
	for _, p := range m.PHPPools {
		versions[summaryClip(p.PHPVersion)] = true
	}
	s.PHPVersions = summaryCap(summaryKeys(versions))
	if m.PHPExtensions != nil {
		s.PHPExtensions = map[string][]string{}
		for v, exts := range m.PHPExtensions {
			if !versions[summaryClip(v)] || len(s.PHPExtensions) >= summaryListMax {
				continue
			}
			set := map[string]bool{}
			for _, e := range exts {
				set[summaryClip(e)] = true
			}
			s.PHPExtensions[summaryClip(v)] = summaryCap(summaryKeys(set))
		}
	}
	pgIDs, pgNames := map[string]bool{}, map[string]bool{}
	for _, db := range m.Databases {
		if db.Engine == "postgres" {
			s.PostgresDatabases = append(s.PostgresDatabases, summaryClip(db.Name))
			if db.ID != "" {
				pgIDs[db.ID] = true
			}
			if db.Name != "" {
				pgNames[db.Name] = true
			}
		}
	}
	sort.Strings(s.PostgresDatabases)
	s.PostgresDatabases = summaryCap(s.PostgresDatabases)
	for _, du := range m.DatabaseUsers {
		pg := du.Engine == "postgres"
		// Bundles made before the engine was recorded: a user granted on a
		// PostgreSQL database is a PostgreSQL user, as a restore reads it.
		for _, g := range du.Grants {
			pg = pg || (du.Engine == "" && (pgIDs[g.DatabaseID] || pgNames[g.DatabaseName]))
		}
		if pg {
			s.PostgresUsers++
		}
	}
	for _, d := range m.Domains {
		s.Mailboxes += len(d.Mailboxes)
		s.Forwarders += len(d.Forwarders)
		s.DNSRecords += len(d.DNSRecords)
	}
	for _, a := range m.DockerApps {
		if a.ServerLevel {
			continue
		}
		name := a.InstanceSlug
		if name == "" {
			name = a.Slug
		}
		s.DockerApps = append(s.DockerApps, summaryClip(name))
	}
	sort.Strings(s.DockerApps)
	s.DockerApps = summaryCap(s.DockerApps)
	return s
}

func summaryClip(s string) string {
	if len(s) > summaryNameMax {
		return s[:summaryNameMax]
	}
	return s
}

func summaryCap(xs []string) []string {
	if len(xs) > summaryListMax {
		return xs[:summaryListMax]
	}
	return xs
}

func summaryKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
