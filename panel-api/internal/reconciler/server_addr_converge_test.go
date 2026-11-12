package reconciler

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// After the server's public IP changed (an IP swap at the end of a
// migration), every zone kept `mail A <old-ip>` and the bootstrap SPF
// `ip4:<old-ip>`: convergeApexAddrRecords moves only the apex A/AAAA, and
// nothing moved these two. They name the server itself — the MX target is
// where the mail server listens, the SPF lists the address mail leaves from —
// so they follow server_settings.public_ipv4/public_ipv6. Only the panel's
// own bootstrap rows move; an operator's edit and a feature's rows don't.

func TestConvergeServerAddr_MailAndSPFFollowTheServerIP(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	rec.records["mailA"] = managedBootstrap("mailA", z.ID, "mail", "A", "192.0.2.70")
	rec.records["mailAAAA"] = managedBootstrap("mailAAAA", z.ID, "mail", "AAAA", "2001:db8::70")
	rec.records["spf"] = managedBootstrap("spf", z.ID, "@", "TXT", `"v=spf1 mx ip4:192.0.2.70 ip6:2001:db8::70 ~all"`)

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{PublicIPv4: "192.0.2.175", PublicIPv6: "2001:db8::175"})

	require.Equal(t, "192.0.2.175", rec.records["mailA"].Content)
	require.Equal(t, "2001:db8::175", rec.records["mailAAAA"].Content)
	require.Equal(t, `"v=spf1 mx ip4:192.0.2.175 ip6:2001:db8::175 ~all"`, rec.records["spf"].Content)
}

func TestConvergeServerAddr_V4OnlySPF(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	rec.records["spf"] = managedBootstrap("spf", z.ID, "@", "TXT", `"v=spf1 mx ip4:192.0.2.70 ~all"`)

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{PublicIPv4: "192.0.2.175"})

	require.Equal(t, `"v=spf1 mx ip4:192.0.2.175 ~all"`, rec.records["spf"].Content)
}

// A server that gains an IPv6 address lists it in the SPF, as a fresh zone's
// would (BuildSPFString).
func TestConvergeServerAddr_SPFGainsIPv6(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	rec.records["spf"] = managedBootstrap("spf", z.ID, "@", "TXT", `"v=spf1 mx ip4:192.0.2.70 ~all"`)

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{PublicIPv4: "192.0.2.70", PublicIPv6: "2001:db8::175"})

	require.Equal(t, `"v=spf1 mx ip4:192.0.2.70 ip6:2001:db8::175 ~all"`, rec.records["spf"].Content)
}

func TestConvergeServerAddr_OperatorAndFeatureRowsUntouched(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	m6 := "m6"

	opMail := managedBootstrap("opMail", z.ID, "mail", "A", "198.51.100.7")
	opMail.Managed = false
	rec.records["opMail"] = opMail
	featMail := managedBootstrap("featMail", z.ID, "mail", "AAAA", "2001:db8::70")
	featMail.ManagedBy = &m6
	rec.records["featMail"] = featMail
	// Bootstrap-owned, but no longer the bootstrap shape: an include was
	// added.
	editedSPF := `"v=spf1 mx ip4:192.0.2.70 include:_spf.example.net ~all"`
	rec.records["spf"] = managedBootstrap("spf", z.ID, "@", "TXT", editedSPF)
	// Another name's A is not the server's address.
	rec.records["shopA"] = managedBootstrap("shopA", z.ID, "shop", "A", "192.0.2.70")

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{PublicIPv4: "192.0.2.175", PublicIPv6: "2001:db8::175"})

	require.Equal(t, "198.51.100.7", rec.records["opMail"].Content, "operator-edited mail A moved")
	require.Equal(t, "2001:db8::70", rec.records["featMail"].Content, "feature-owned mail AAAA moved")
	require.Equal(t, editedSPF, rec.records["spf"].Content, "edited SPF rewritten")
	require.Equal(t, "192.0.2.70", rec.records["shopA"].Content, "a non-mail A moved")
}

// No address for a family never blanks or drops that family: an unset
// setting is not evidence the address went away.
func TestConvergeServerAddr_NoAddressLeavesRows(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	rec.records["mailAAAA"] = managedBootstrap("mailAAAA", z.ID, "mail", "AAAA", "2001:db8::70")
	rec.records["spf"] = managedBootstrap("spf", z.ID, "@", "TXT", `"v=spf1 mx ip4:192.0.2.70 ip6:2001:db8::70 ~all"`)

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{})
	require.Equal(t, `"v=spf1 mx ip4:192.0.2.70 ip6:2001:db8::70 ~all"`, rec.records["spf"].Content)

	r.convergeServerAddrRecords(ctx, z, &models.ServerSettings{PublicIPv4: "192.0.2.175"})
	require.Equal(t, "2001:db8::70", rec.records["mailAAAA"].Content, "mail AAAA moved without a v6 address")
	require.Equal(t, `"v=spf1 mx ip4:192.0.2.175 ip6:2001:db8::70 ~all"`, rec.records["spf"].Content)
}

func TestConvergeServerAddr_NilSettingsNoOp(t *testing.T) {
	ctx := context.Background()
	r, rec, zr := newMigrator(t)
	z := seedZone(t, zr, "zone1", "example.com")
	rec.records["mailA"] = managedBootstrap("mailA", z.ID, "mail", "A", "192.0.2.70")

	r.convergeServerAddrRecords(ctx, z, nil)

	require.Equal(t, "192.0.2.70", rec.records["mailA"].Content)
}

// The reconcile pass runs it: the DNS reconcile of a domain calls it right
// after the bootstrap-shape migration, before the records are compiled.
func TestConvergeServerAddr_WiredIntoTheZonePass(t *testing.T) {
	raw, err := os.ReadFile("reconciler.go")
	require.NoError(t, err)
	require.True(t, strings.Contains(string(raw), "r.migrateBootstrapShape(ctx, zone, srv)\n\tr.convergeServerAddrRecords(ctx, zone, srv)"),
		"reconciler.go no longer calls convergeServerAddrRecords right after migrateBootstrapShape")
}

func TestParseBootstrapSPF(t *testing.T) {
	for _, tc := range []struct {
		in     string
		v4, v6 string
		ok     bool
	}{
		{`"v=spf1 mx ~all"`, "", "", true},
		{`"v=spf1 mx ip4:192.0.2.1 ~all"`, "192.0.2.1", "", true},
		{`"v=spf1 mx ip6:2001:db8::1 ~all"`, "", "2001:db8::1", true},
		{`"v=spf1 mx ip4:192.0.2.1 ip6:2001:db8::1 ~all"`, "192.0.2.1", "2001:db8::1", true},
		{`"v=spf1 mx ip6:2001:db8::1 ip4:192.0.2.1 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:192.0.2.1 ip4:192.0.2.2 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:192.0.2.1 include:_spf.example.net ~all"`, "", "", false},
		{`"v=spf1 mx  ip4:192.0.2.1 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:192.0.2 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:192.0.2.0/24 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:2001:db8::1 ~all"`, "", "", false},
		{`"v=spf1 mx ip4:192.0.2.1 -all"`, "", "", false},
		{`"v=spf1 a mx ip4:192.0.2.1 ~all"`, "", "", false},
		{`v=spf1 mx ip4:192.0.2.1 ~all`, "", "", false},
	} {
		v4, v6, ok := parseBootstrapSPF(tc.in)
		require.Equal(t, tc.ok, ok, tc.in)
		if ok {
			require.Equal(t, tc.v4, v4, tc.in)
			require.Equal(t, tc.v6, v6, tc.in)
		}
	}
}
