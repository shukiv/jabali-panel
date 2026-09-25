package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-370 AC8: characterization of both Mail Inventory adapters — the admin
// Directory (GET /admin/mailboxes) and the tenant Workspace (GET /me/mailboxes).
// Both render rows from the same ListDirectoryPage projection; these tests pin
// what each adapter puts on the wire so a change to one cannot silently drift
// from the other:
//   - the exact key set of the envelope and of a row;
//   - the shared base row (identical fields and values on both adapters);
//   - owner columns on admin rows only;
//   - no secret or infrastructure field, even when the repository row carries
//     one (password hash, SSO ciphertext, the system flag);
//   - system rows dropped by the handler as well as in SQL.

// inventoryBaseRowKeys is the mailbox row every inventory adapter returns.
var inventoryBaseRowKeys = []string{
	"created_at", "display_name", "domain_id", "domain_name", "email", "id",
	"is_disabled", "last_usage_at", "last_usage_bytes", "quota_bytes",
	"send_only", "updated_at",
}

func inventoryFixture() *dirCaptureMbxRepo {
	used := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	return &dirCaptureMbxRepo{
		rows: []repository.MailboxWithDomain{
			{
				Mailbox: models.Mailbox{
					ID: "mb1", DomainID: "d1", LocalPart: "alice", EmailCached: "alice@example.com",
					DisplayName: "Alice", QuotaBytes: 2 << 30, IsDisabled: false, SendOnly: true,
					LastUsageBytes: 4096, LastUsageAt: &used, CreatedAt: created, UpdatedAt: created,
					// Secrets a careless projection or response type could leak.
					PasswordHash: "$2b$12$not-a-real-hash", PasswordEnc: []byte{0xde, 0xad},
				},
				DomainName: "example.com", OwnerUserID: "u1", UserUsername: "alice",
			},
			{
				// A system principal (JAB-230 relay) that slipped past SQL.
				Mailbox:    models.Mailbox{ID: "mb-sys", DomainID: "d1", EmailCached: "relay@example.com", System: true},
				DomainName: "example.com", OwnerUserID: "u1", UserUsername: "alice",
			},
		},
		total: 1,
	}
}

func decodeInventory(t *testing.T, body []byte) (map[string]any, []map[string]any) {
	t.Helper()
	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env))
	raw, ok := env["data"].([]any)
	require.True(t, ok, "data must be an array: %s", body)
	rows := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, r.(map[string]any))
	}
	return env, rows
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func withKeys(base []string, extra ...string) []string {
	out := append(append([]string{}, base...), extra...)
	sort.Strings(out)
	return out
}

func runAdminInventory(t *testing.T, target string) (map[string]any, []map[string]any) {
	t.Helper()
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: inventoryFixture()}}
	c, w := newAdminMailboxTestCtx(target)
	h.listAllAdmin(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return decodeInventory(t, w.Body.Bytes())
}

func runWorkspaceInventory(t *testing.T, target string) (map[string]any, []map[string]any) {
	t.Helper()
	h := &mailboxHandler{cfg: MailboxHandlerConfig{Mailboxes: inventoryFixture()}}
	c, w := newAdminMailboxTestCtx(target)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.listWorkspace(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return decodeInventory(t, w.Body.Bytes())
}

func TestMailInventoryAdapters_Characterization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adminEnv, adminRows := runAdminInventory(t, "/admin/mailboxes?page=1&page_size=20")
	wsEnv, wsRows := runWorkspaceInventory(t, "/me/mailboxes?page=1&page_size=20")

	t.Run("envelopes", func(t *testing.T) {
		require.Equal(t, []string{"data", "page", "page_size", "total"}, keysOf(adminEnv))
		require.Equal(t, []string{"data", "page", "page_size", "total"}, keysOf(wsEnv))
		require.EqualValues(t, 1, adminEnv["total"])
		require.EqualValues(t, 1, wsEnv["total"])

		// The admin user-overview read sends no page params and gets the
		// unpaginated envelope; the workspace is always paginated.
		plainEnv, _ := runAdminInventory(t, "/admin/mailboxes")
		require.Equal(t, []string{"data", "total"}, keysOf(plainEnv))
		wsPlain, _ := runWorkspaceInventory(t, "/me/mailboxes")
		require.Equal(t, []string{"data", "page", "page_size", "total"}, keysOf(wsPlain))
	})

	t.Run("system rows are dropped by both handlers", func(t *testing.T) {
		require.Len(t, adminRows, 1)
		require.Len(t, wsRows, 1)
		require.Equal(t, "mb1", adminRows[0]["id"])
		require.Equal(t, "mb1", wsRows[0]["id"])
	})

	t.Run("row key sets: base on both, owner columns on admin only", func(t *testing.T) {
		require.Equal(t, withKeys(inventoryBaseRowKeys, "owner_user_id", "user_username"), keysOf(adminRows[0]))
		require.Equal(t, withKeys(inventoryBaseRowKeys), keysOf(wsRows[0]))
	})

	t.Run("no secret or infrastructure field on either adapter", func(t *testing.T) {
		for _, row := range []map[string]any{adminRows[0], wsRows[0]} {
			for _, k := range []string{"password_hash", "password_enc", "PasswordHash", "PasswordEnc", "system", "local_part"} {
				_, present := row[k]
				require.False(t, present, "inventory rows must not carry %q", k)
			}
		}
	})

	t.Run("the shared base row is identical on both adapters", func(t *testing.T) {
		for _, k := range inventoryBaseRowKeys {
			require.Equal(t, adminRows[0][k], wsRows[0][k], "base field %q drifted between adapters", k)
		}
		require.Equal(t, "alice@example.com", wsRows[0]["email"])
		require.Equal(t, "example.com", wsRows[0]["domain_name"])
		require.Equal(t, "Alice", wsRows[0]["display_name"])
		require.EqualValues(t, 2<<30, wsRows[0]["quota_bytes"])
		require.Equal(t, true, wsRows[0]["send_only"])
		require.EqualValues(t, 4096, wsRows[0]["last_usage_bytes"])
		require.Equal(t, "2026-09-01T12:00:00Z", wsRows[0]["last_usage_at"])
		require.Equal(t, "u1", adminRows[0]["owner_user_id"])
		require.Equal(t, "alice", adminRows[0]["user_username"])
	})
}
