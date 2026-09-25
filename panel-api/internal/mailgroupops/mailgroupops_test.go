package mailgroupops

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestCheckMemberCap(t *testing.T) {
	require.NoError(t, CheckMemberCap("distribution", true, MaxInternalOnlyMembers))
	require.True(t, IsMemberCapErr(CheckMemberCap("distribution", true, MaxInternalOnlyMembers+1)))
	// Only internal-only distribution lists fan out through Sieve redirects.
	require.NoError(t, CheckMemberCap("distribution", false, 500))
	require.NoError(t, CheckMemberCap("resource", true, 500))
}

func TestBuildApplyParams(t *testing.T) {
	dist := &models.MailGroup{EmailCached: "sales@example.org", GroupKind: "distribution", InternalOnly: true, DisplayName: "Sales"}
	p := BuildApplyParams(dist, nil)
	require.Equal(t, "distribution", p["group_kind"])
	require.Equal(t, []string{}, p["member_emails"], "an empty list must be sent as [], never null")
	require.Equal(t, true, p["internal_only"])

	res := &models.MailGroup{EmailCached: "team@example.org", GroupKind: "resource"}
	p = BuildApplyParams(res, []string{"a@example.org"})
	_, has := p["member_emails"]
	require.False(t, has, "resource membership is projected by mailgroup.members_set, not apply")
}
