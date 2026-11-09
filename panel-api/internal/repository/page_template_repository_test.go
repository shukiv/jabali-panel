package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// withRetiredDefault marks body as a retired built-in domain default page.
func withRetiredDefault(t *testing.T, body string) {
	t.Helper()
	prev := retiredDefaultBodies
	retiredDefaultBodies = map[string]map[string]bool{
		models.PageTemplateDomainDefaultIndex: {sha256Hex(body): true},
	}
	t.Cleanup(func() { retiredDefaultBodies = prev })
}

// pageTemplateRows returns every known key, the domain default page holding
// defaultIndex and the rest an admin's own edit.
func pageTemplateRows(defaultIndex string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"key", "content"})
	for _, k := range models.AllPageTemplateKeys {
		content := "<p>edited by the admin</p>"
		if k == models.PageTemplateDomainDefaultIndex {
			content = defaultIndex
		}
		rows.AddRow(k, content)
	}
	return rows
}

// GH #2422: a stored domain default page still byte-equal to a retired
// built-in moves to the current one.
func TestEnsureDefaults_MovesRetiredDefault(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	retired := "<p>{{.Domain}} {{.Username}} {{.DocRoot}}</p>"
	withRetiredDefault(t, retired)

	mock.ExpectQuery("SELECT `key`,`content` FROM `page_templates`").
		WillReturnRows(pageTemplateRows(retired))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `page_templates` SET .* WHERE `key` = \\? AND content = \\?").
		WithArgs(DefaultPageTemplateBody(models.PageTemplateDomainDefaultIndex), sqlmock.AnyArg(),
			models.PageTemplateDomainDefaultIndex, retired).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	n, err := NewPageTemplateRepository(db).EnsureDefaults(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// An admin's own page is never replaced, even one close to a retired body.
func TestEnsureDefaults_KeepsEditedDefault(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	retired := "<p>{{.Domain}} {{.Username}} {{.DocRoot}}</p>"
	withRetiredDefault(t, retired)

	mock.ExpectQuery("SELECT `key`,`content` FROM `page_templates`").
		WillReturnRows(pageTemplateRows(retired + "\n"))

	n, err := NewPageTemplateRepository(db).EnsureDefaults(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The current built-in must not be retired, or every boot would rewrite it,
// and must not show the account's login or document root.
func TestRetiredDefaultBodies_CurrentDefaultNotRetired(t *testing.T) {
	for key, retired := range retiredDefaultBodies {
		if retired[sha256Hex(DefaultPageTemplateBody(key))] {
			t.Errorf("%s: the current default body is listed as retired", key)
		}
	}
	body := DefaultPageTemplateBody(models.PageTemplateDomainDefaultIndex)
	for _, field := range []string{"{{.Username}}", "{{.DocRoot}}"} {
		if strings.Contains(body, field) {
			t.Errorf("domain default page uses %s", field)
		}
	}
}
