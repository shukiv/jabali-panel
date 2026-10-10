package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailspam"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The mail server's spam thresholds follow the panel's (GH #2017).

type spamApplyCall struct {
	want   mailspam.Scores
	reload bool
}

type fakeSpamApplier struct {
	calls []spamApplyCall
	// next results, consumed in order; empty = (false, nil)
	changed []bool
	errs    []error
}

func (f *fakeSpamApplier) Apply(_ context.Context, want mailspam.Scores, reload bool) (bool, error) {
	f.calls = append(f.calls, spamApplyCall{want, reload})
	var changed bool
	var err error
	if len(f.changed) > 0 {
		changed, f.changed = f.changed[0], f.changed[1:]
	}
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	return changed, err
}

type spamSettingsRepo struct {
	repository.ServerSettingsRepository
	srv *models.ServerSettings
	err error
}

func (f *spamSettingsRepo) Get(context.Context) (*models.ServerSettings, error) {
	return f.srv, f.err
}

func spamFixture(srv *models.ServerSettings) (*Reconciler, *fakeSpamApplier, *spamSettingsRepo) {
	ap := &fakeSpamApplier{}
	settings := &spamSettingsRepo{srv: srv}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, &fakeAgent{}, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithMailSpamScores(ap)
	r.serverSettings = settings
	return r, ap, settings
}

func spamSrv(junk, reject, discard float64) *models.ServerSettings {
	return &models.ServerSettings{MailEnabled: true, SpamJunkScore: junk, SpamRejectScore: reject, SpamDiscardScore: discard}
}

// The first tick applies the stored thresholds; a steady tick applies
// nothing; a change applies again.
func TestReconcileMailSpamScores_AppliesOnceThenOnChange(t *testing.T) {
	srv := spamSrv(5, 15, 20)
	r, ap, _ := spamFixture(srv)
	ctx := context.Background()

	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 1 || ap.calls[0] != (spamApplyCall{mailspam.Scores{Junk: 5, Reject: 15, Discard: 20}, false}) {
		t.Fatalf("first tick calls = %+v", ap.calls)
	}
	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 1 {
		t.Fatalf("steady tick called Apply: %+v", ap.calls)
	}
	srv.SpamJunkScore, srv.SpamRejectScore = 8, 0
	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 2 || ap.calls[1].want != (mailspam.Scores{Junk: 8, Reject: 0, Discard: 20}) {
		t.Fatalf("after change calls = %+v", ap.calls)
	}
}

// A box without the mail module has no mail server to configure.
func TestReconcileMailSpamScores_MailOff(t *testing.T) {
	srv := spamSrv(5, 15, 20)
	srv.MailEnabled = false
	r, ap, _ := spamFixture(srv)
	r.reconcileMailSpamScores(context.Background())
	if len(ap.calls) != 0 {
		t.Fatalf("calls = %+v with mail off", ap.calls)
	}
}

// Settings that can't be read give no thresholds to apply.
func TestReconcileMailSpamScores_SettingsUnreadable(t *testing.T) {
	r, ap, settings := spamFixture(nil)
	settings.err = errors.New("db down")
	r.reconcileMailSpamScores(context.Background())
	if len(ap.calls) != 0 {
		t.Fatalf("calls = %+v without settings", ap.calls)
	}
}

// Stored thresholds the API would refuse (a hand edit of the database) are
// not sent to the mail server.
func TestReconcileMailSpamScores_InvalidStored(t *testing.T) {
	r, ap, _ := spamFixture(spamSrv(10, 5, 0))
	r.reconcileMailSpamScores(context.Background())
	if len(ap.calls) != 0 {
		t.Fatalf("calls = %+v for reject below junk", ap.calls)
	}
}

// The thresholds were written but the reload failed: they are stored, not
// live. The next tick retries with a reload even though nothing differs.
func TestReconcileMailSpamScores_RetriesTheReload(t *testing.T) {
	r, ap, _ := spamFixture(spamSrv(5, 15, 20))
	ap.changed = []bool{true, false, false}
	ap.errs = []error{errors.New("reload failed"), nil, nil}
	ctx := context.Background()

	r.reconcileMailSpamScores(ctx)
	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 2 || !ap.calls[1].reload {
		t.Fatalf("calls = %+v; want a retry with reload", ap.calls)
	}
	// Once the reload went through, the pass is steady again.
	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 2 {
		t.Fatalf("calls = %+v after the reload succeeded", ap.calls)
	}
}

// A failed write retries the write, with no reload of its own.
func TestReconcileMailSpamScores_RetriesAFailedWrite(t *testing.T) {
	r, ap, _ := spamFixture(spamSrv(5, 15, 20))
	ap.errs = []error{errors.New("stalwart down")}
	ctx := context.Background()

	r.reconcileMailSpamScores(ctx)
	r.reconcileMailSpamScores(ctx)
	if len(ap.calls) != 2 || ap.calls[1].reload {
		t.Fatalf("calls = %+v; want a plain retry", ap.calls)
	}
}

func TestReconcileMailSpamScores_NotWired(t *testing.T) {
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, &fakeAgent{}, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second})
	r.serverSettings = &spamSettingsRepo{srv: spamSrv(5, 15, 20)}
	r.reconcileMailSpamScores(context.Background()) // must not panic
}
