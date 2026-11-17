package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A new install's automatic Let's Encrypt (ADR-0066 amendment) ends as soon
// as the admin acts on the panel certificate: the toggle and an issue with
// Let's Encrypt on both close the window, so a later failure is the admin's
// to retry, not a reason to turn Let's Encrypt back off. A request the panel
// turns down changes nothing.

func withLEAutoWindow(f *panelCertFixture) {
	until := time.Now().Add(models.PanelLEAutoWindow)
	f.certs.mu.Lock()
	f.certs.rows[models.PanelCertKindHostname].LEAutoUntil = &until
	f.certs.mu.Unlock()
}

func (f *panelCertFixture) toggle(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/panel-certificate/toggle", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestPanelCertToggle_LEChoiceEndsTheAutoWindow(t *testing.T) {
	for _, body := range []string{`{"use_le":true}`, `{"use_le":false}`} {
		t.Run(body, func(t *testing.T) {
			f := newPanelCertFixture(t, panelCertOpts{})
			withLEAutoWindow(f)
			if rec := f.toggle(t, body); rec.Code != http.StatusOK {
				t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
			}
			if w := f.certs.get(models.PanelCertKindHostname).LEAutoUntil; w != nil {
				t.Errorf("window still open (%v) after the admin chose", w)
			}
		})
	}
}

func TestPanelCertToggle_StagingOnlyKeepsTheAutoWindow(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{})
	withLEAutoWindow(f)
	if rec := f.toggle(t, `{"staging":true}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
	}
	if f.certs.get(models.PanelCertKindHostname).LEAutoUntil == nil {
		t.Error("a staging change closed the window")
	}
}

func TestPanelCertIssue_LEOnEndsTheAutoWindow(t *testing.T) {
	for _, kind := range []string{models.PanelCertKindHostname, models.PanelCertKindMail} {
		t.Run(kind, func(t *testing.T) {
			f := newPanelCertFixture(t, panelCertOpts{useLE: true})
			withLEAutoWindow(f)
			// The fixture's hostname isn't routable, so the attempt is
			// parked for a retry and no row is written with every column:
			// only the explicit end closes the window.
			rec, _ := f.issue(t, kind)
			if rec.Code != http.StatusFailedDependency {
				t.Fatalf("want 424 not routable, got %d %s", rec.Code, rec.Body.String())
			}
			host := f.certs.get(models.PanelCertKindHostname)
			if host.LEAutoUntil != nil {
				t.Errorf("window still open: a failure would turn Let's Encrypt off under the admin")
			}
			if !host.UseLE {
				t.Error("Let's Encrypt turned off")
			}
		})
	}
}

func TestPanelCertIssue_TurnedDownKeepsTheAutoWindow(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{useLE: true, noAdminEmail: true})
	withLEAutoWindow(f)
	rec, _ := f.issue(t, models.PanelCertKindHostname)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 missing admin email, got %d %s", rec.Code, rec.Body.String())
	}
	if f.certs.get(models.PanelCertKindHostname).LEAutoUntil == nil {
		t.Error("a turned-down request closed the window")
	}
}
