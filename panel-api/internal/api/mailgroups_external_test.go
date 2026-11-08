package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1833: a distribution list delivers to outside addresses (a Gmail
// address, say) next to its member mailboxes, up to the admin's cap.

const externalPath = "/api/v1/mailgroups/grp1/external-members"

func TestSetExternalMembers_SavesAndAppliesTheList(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 1)
	fx.groups.deliverable = []string{"user1@example.org"}

	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{" Partner@Gmail.com", "a@outlook.com", "partner@gmail.com"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"a@outlook.com", "partner@gmail.com"}, fx.groups.external, "saved canonical, de-duplicated and sorted")

	p := fx.ag.lastParams("mailgroup.apply")
	require.NotNil(t, p, "the list must be re-applied; calls=%v", fx.ag.calls)
	require.Equal(t, []any{"user1@example.org"}, p["member_emails"], "member mailboxes stay apart")
	require.Equal(t, []any{"a@outlook.com", "partner@gmail.com"}, p["external_emails"])
}

func TestSetExternalMembers_EmptyClears(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.groups.external = []string{"partner@gmail.com"}

	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, fx.groups.externalSaved)
	require.Empty(t, fx.groups.external)
	require.Equal(t, []any{}, fx.ag.lastParams("mailgroup.apply")["external_emails"])
}

func requireExternalRefused(t *testing.T, fx *mgFixture, body map[string]any, wantCode string) {
	t.Helper()
	w := do(t, fx.router, "PUT", externalPath, body)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, wantCode, resp["error"], w.Body.String())
	require.False(t, fx.groups.externalSaved, "a refused set must not be saved")
	require.Empty(t, fx.ag.calls, "a refused set must not reach the mail server")
}

func TestSetExternalMembers_RefusesBadAddress(t *testing.T) {
	for _, bad := range []string{"not-an-address", `quote"d@gmail.com`, `back\slash@gmail.com`, "two words@gmail.com", "x@localhost"} {
		t.Run(bad, func(t *testing.T) {
			fx := newMGFixture(t, "distribution", false, 0)
			requireExternalRefused(t, fx, map[string]any{"addresses": []string{"ok@gmail.com", bad}}, "invalid_external_member")
		})
	}
}

// An address on the list's own domain is a mailbox there and belongs in the
// member mailboxes; the list's own address would loop.
func TestSetExternalMembers_RefusesTheListsOwnDomain(t *testing.T) {
	for _, addr := range []string{"team@example.org", "bob@Example.org"} {
		t.Run(addr, func(t *testing.T) {
			fx := newMGFixture(t, "distribution", false, 0)
			requireExternalRefused(t, fx, map[string]any{"addresses": []string{addr}}, "external_member_on_list_domain")
		})
	}
}

func TestSetExternalMembers_RefusesASharedWorkspaceGroup(t *testing.T) {
	fx := newMGFixture(t, "resource", false, 0)
	requireExternalRefused(t, fx, map[string]any{"addresses": []string{"partner@gmail.com"}}, "not_a_distribution_list")
}

func TestSetExternalMembers_EnforcesTheAdminCap(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 2}
	requireExternalRefused(t, fx, map[string]any{"addresses": []string{"a@gmail.com", "b@gmail.com", "c@gmail.com"}}, "too_many_external_members")

	fx = newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 2}
	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{"a@gmail.com", "b@gmail.com"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestSetExternalMembers_CapZeroAllowsNone(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 0}
	requireExternalRefused(t, fx, map[string]any{"addresses": []string{"a@gmail.com"}}, "too_many_external_members")
}

// Before the settings row exists the cap is the migration default, 25.
func TestSetExternalMembers_NoSettingsRowUsesDefaultCap(t *testing.T) {
	addrs := make([]string, 0, 26)
	for i := 0; i < 26; i++ {
		addrs = append(addrs, "user"+string(rune('a'+i))+"@gmail.com")
	}
	fx := newMGFixture(t, "distribution", false, 0)
	requireExternalRefused(t, fx, map[string]any{"addresses": addrs}, "too_many_external_members")

	fx = newMGFixture(t, "distribution", false, 0)
	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": addrs[:25]})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// An admin lowered the cap under what a list holds: the list may still drop
// addresses, but not take a new one.
func TestSetExternalMembers_OverLoweredCapMayOnlyShrink(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 1}
	fx.groups.external = []string{"a@gmail.com", "b@gmail.com", "c@gmail.com"}
	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{"a@gmail.com", "b@gmail.com"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	fx = newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 1}
	fx.groups.external = []string{"a@gmail.com", "b@gmail.com"}
	requireExternalRefused(t, fx, map[string]any{"addresses": []string{"a@gmail.com", "new@gmail.com"}}, "too_many_external_members")
}

// An internal-only list redirects to every recipient through Sieve, and the
// mail server allows 20 redirects: mailboxes and outside addresses share it.
func TestSetExternalMembers_InternalOnlyCountsMailboxesToo(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 19)
	fx.groups.memberIDs = mailboxIDs(19)
	requireExternalRefused(t, fx, map[string]any{"addresses": []string{"a@gmail.com", "b@gmail.com"}}, "too_many_members")

	fx = newMGFixture(t, "distribution", true, 19)
	fx.groups.memberIDs = mailboxIDs(19)
	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{"a@gmail.com"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestSetMembers_InternalOnlyCountsOutsideAddresses(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 19)
	fx.groups.external = []string{"a@gmail.com", "b@gmail.com"}
	w := do(t, fx.router, "PUT", "/api/v1/mailgroups/grp1/members", map[string]any{"mailbox_ids": mailboxIDs(19)})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "too_many_members")
	require.False(t, fx.groups.setMembersCalled)
}

func TestAddMember_InternalOnlyCountsOutsideAddresses(t *testing.T) {
	fx := newMGFixture(t, "distribution", true, 19)
	fx.groups.memberIDs = mailboxIDs(18)
	fx.groups.external = []string{"a@gmail.com", "b@gmail.com"}
	w := do(t, fx.router, "POST", "/api/v1/mailgroups/grp1/members/mb19", nil)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.False(t, fx.groups.addMemberCalled)
}

func TestUpdate_EnableInternalOnlyCountsOutsideAddresses(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 19)
	fx.groups.memberIDs = mailboxIDs(19)
	fx.groups.external = []string{"a@gmail.com", "b@gmail.com"}
	w := do(t, fx.router, "PATCH", "/api/v1/mailgroups/grp1", map[string]any{"internal_only": true})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.False(t, fx.groups.internalOnlyWritten)
}

func TestGet_CarriesOutsideAddressesAndCap(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.settings.row = &models.ServerSettings{MailListExternalMemberCap: 10}
	fx.groups.external = []string{"partner@gmail.com"}

	w := do(t, fx.router, "GET", "/api/v1/mailgroups/grp1", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, []any{"partner@gmail.com"}, resp["external_members"])
	require.Equal(t, float64(1), resp["external_member_count"])
	require.Equal(t, float64(10), resp["external_member_cap"])
}

// A list with no outside addresses answers [], never null, so the UI never
// renders a blank editor.
func TestGet_NoOutsideAddressesIsEmptyArray(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	w := do(t, fx.router, "GET", "/api/v1/mailgroups/grp1", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"external_members":[]`)
	require.Contains(t, w.Body.String(), `"external_member_cap":25`)
}

// Only the domain's owner (or an admin) may change a list.
func TestSetExternalMembers_OtherOwnerForbidden(t *testing.T) {
	fx := newMGFixture(t, "distribution", false, 0)
	fx.dom.dom.UserID = "someone-else"
	w := do(t, fx.router, "PUT", externalPath, map[string]any{"addresses": []string{"partner@gmail.com"}})
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.False(t, fx.groups.externalSaved)
}
