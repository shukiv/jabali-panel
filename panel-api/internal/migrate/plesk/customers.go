package plesk

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// customers.go — Plesk customer + service-plan discovery and the PURE
// mapping to jabali packages/users (GH #429 step 5). This file does NOT
// mutate the destination: it reads the source (`plesk bin customer` /
// `plesk bin service_plan`) and produces the drafts that the restore
// step will turn into HostingPackage rows + users. Password handling +
// the actual create/upsert (the security-sensitive half) live in the
// restore step, which mints an invite/forced-reset rather than importing
// any source hash.
//
// jabali has no reseller tier (only user.IsAdmin) — a reseller-owned
// customer is flagged IsReseller so the restore step can flatten it to
// an admin-owned user (Step 6). Coded against the Plesk Obsidian CLI
// docs; not yet live-validated.

// PleskPlan is a source service plan (hosting package).
type PleskPlan struct {
	Name         string
	DiskMB       uint32
	BandwidthMB  uint32
	MaxDomains   uint32
	MaxMailboxes uint32
	MaxDatabases uint32
	Owner        string // reseller login that owns the plan, "" = admin
}

// PleskCustomer is a source customer account.
type PleskCustomer struct {
	Login       string
	ContactName string
	Email       string
	Company     string
	Suspended   bool
	Reseller    string // owning reseller login, "" = owned by admin
}

// TenantSet is the server-level discovery result: every plan + customer
// visible to the admin principal.
type TenantSet struct {
	Plans     []PleskPlan
	Customers []PleskCustomer
}

// UserDraft is the jabali user a customer maps to. NOTE: no password
// field — Plesk hashes aren't portable, so the restore step mints an
// invite / forced reset. IsReseller drives the Step-6 flatten.
type UserDraft struct {
	Username    string
	Email       string
	DisplayName string
	Suspended   bool
	IsReseller  bool
}

// DiscoverTenants enumerates the source's service plans + customers.
// Read-only; server-level (not per-subscription), so it is a Plesk
// extension rather than part of migrate.Discoverer. Best-effort: a
// per-item info failure is skipped, not fatal.
func (d *Discoverer) DiscoverTenants(ctx context.Context, raw migrate.Session) (*TenantSet, error) {
	s, ok := raw.(*session)
	if !ok {
		return nil, fmt.Errorf("DiscoverTenants: wrong session type")
	}
	ts := &TenantSet{Plans: []PleskPlan{}, Customers: []PleskCustomer{}}

	// `plesk bin service_plan` / `customer` have no list verb; enumerate the
	// names via the psa DB (`plesk db` reads bypass the customer-management
	// license gate), then pull each one's detail with `--info`. Hosting
	// plans are Templates.type='domain' (reseller plans are 'reseller').
	if out, err := s.run(ctx, d.CommandTimeout, "plesk db -Ne "+shellQuote("SELECT name FROM Templates WHERE type='domain'")); err == nil {
		for _, name := range splitLines(string(out)) {
			info, ierr := s.run(ctx, d.CommandTimeout, "plesk bin service_plan --info "+shellQuote(name))
			if ierr != nil {
				continue
			}
			ts.Plans = append(ts.Plans, parsePleskPlan(name, string(info)))
		}
	}

	if out, err := s.run(ctx, d.CommandTimeout, "plesk db -Ne "+shellQuote("SELECT login FROM clients WHERE type='customer'")); err == nil {
		for _, login := range splitLines(string(out)) {
			info, ierr := s.run(ctx, d.CommandTimeout, "plesk bin customer --info "+shellQuote(login))
			if ierr != nil {
				continue
			}
			ts.Customers = append(ts.Customers, parsePleskCustomer(login, string(info)))
		}
	}
	return ts, nil
}

func parsePleskPlan(name, info string) PleskPlan {
	m := parsePleskInfo(info)
	return PleskPlan{
		Name:         name,
		DiskMB:       parsePleskSizeMB(firstNonEmpty(m["disk space"], m["disk_space"], m["disk quota"])),
		BandwidthMB:  parsePleskSizeMB(firstNonEmpty(m["max traffic"], m["traffic"], m["max_traffic"])),
		MaxDomains:   parsePleskCount(firstNonEmpty(m["domains"], m["max domains"], m["maximum number of domains"])),
		MaxMailboxes: parsePleskCount(firstNonEmpty(m["mailboxes"], m["mailboxes number"], m["maximum number of mailboxes"])),
		MaxDatabases: parsePleskCount(firstNonEmpty(m["databases"], m["max databases"], m["maximum number of databases"])),
		Owner:        firstNonEmpty(m["service plan owner"], m["owner"], m["reseller"]),
	}
}

func parsePleskCustomer(login, info string) PleskCustomer {
	m := parsePleskInfo(info)
	suspended := truthy(m["suspended"])
	if !suspended && strings.EqualFold(strings.TrimSpace(m["status"]), "suspended") {
		suspended = true
	}
	return PleskCustomer{
		Login:       login,
		ContactName: firstNonEmpty(m["contact name"], m["personal name"], m["contact"]),
		Email:       firstNonEmpty(m["email"], m["e-mail"]),
		Company:     firstNonEmpty(m["company"], m["company name"]),
		Suspended:   suspended,
		Reseller:    firstNonEmpty(m["reseller"], m["owner"]),
	}
}

// PlanToHostingPackage is the PURE mapping from a Plesk plan to a jabali
// HostingPackage. Zero = unlimited in HostingPackage, which is also how
// an unmapped/unlimited Plesk limit lands (safe default). Docker/Python
// app allowances default to 0 (opt-in per plan on jabali; Plesk has no
// equivalent). ID + timestamps are assigned at create time, not here.
func PlanToHostingPackage(p PleskPlan) models.HostingPackage {
	return models.HostingPackage{
		Name:             p.Name,
		DiskQuotaMB:      p.DiskMB,
		BandwidthQuotaMB: p.BandwidthMB,
		MaxDomains:       p.MaxDomains,
		MaxEmailAccounts: p.MaxMailboxes,
		MaxDatabases:     p.MaxDatabases,
		// No Plesk equivalent → jabali-safe defaults (0 = not included).
		MaxDockerApps: 0,
		MaxPythonApps: 0,
	}
}

// CustomerToUser is the PURE mapping from a Plesk customer to a jabali
// user draft. No password is carried — the restore step mints an invite.
func CustomerToUser(c PleskCustomer) UserDraft {
	return UserDraft{
		Username:    sanitizeUsername(c.Login),
		Email:       strings.TrimSpace(c.Email),
		DisplayName: firstNonEmpty(c.ContactName, c.Company, c.Login),
		Suspended:   c.Suspended,
		IsReseller:  false, // set by the caller from reseller enumeration (Step 6)
	}
}

var usernameStrip = regexp.MustCompile(`[^a-z0-9_-]`)

// sanitizeUsername lowercases a Plesk login and strips it to jabali's
// username charset. An email-shaped login keeps only the local part.
func sanitizeUsername(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	if at := strings.IndexByte(login, '@'); at > 0 {
		login = login[:at]
	}
	return usernameStrip.ReplaceAllString(login, "")
}

// parsePleskSizeMB parses a Plesk size ("500M", "1 GB", "1073741824"
// bytes, "unlimited", "-1") into megabytes. Unlimited / unparseable → 0
// (unlimited in HostingPackage).
func parsePleskSizeMB(s string) uint32 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "unlimited" || s == "-1" || s == "0" {
		return 0
	}
	// leading number (may be float), optional unit suffix.
	num := s
	i := 0
	for i < len(num) && (num[i] >= '0' && num[i] <= '9' || num[i] == '.') {
		i++
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(num[:i]), 64)
	if err != nil || val <= 0 {
		return 0
	}
	unit := strings.TrimSpace(num[i:])
	switch {
	case strings.HasPrefix(unit, "t"): // TB
		val *= 1024 * 1024
	case strings.HasPrefix(unit, "g"): // GB
		val *= 1024
	case strings.HasPrefix(unit, "m"): // MB
		// already MB
	case strings.HasPrefix(unit, "k"): // KB
		val /= 1024
	case unit == "": // bare number → bytes
		val /= 1024 * 1024
	}
	if val < 0 {
		return 0
	}
	return uint32(val)
}

// parsePleskCount parses a Plesk count limit; "unlimited"/-1/"" → 0.
func parsePleskCount(s string) uint32 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "unlimited" || s == "-1" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}
