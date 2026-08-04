package reconciler

// GH #879: server-wide branded-app-error default with per-domain tri-state
// override — the resolution matrix must hold or a domain silently loses (or
// gains) interception on the next reconcile.

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestEffectiveInterceptErrors(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name       string
		serverOn   bool
		override   *bool
		noSettings bool
		want       bool
	}{
		{"inherit server off", false, nil, false, false},
		{"inherit server on", true, nil, false, true},
		{"override on beats server off", false, &on, false, true},
		{"override off beats server on", true, &off, false, false},
		{"no settings repo defaults off", false, nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Reconciler{}
			if !tc.noSettings {
				r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{InterceptAppErrorsDefault: tc.serverOn}}
			}
			d := &models.Domain{NginxSafeOptions: models.NginxSafeOptions{InterceptErrors: tc.override}}
			if got := r.effectiveInterceptErrors(context.Background(), d); got != tc.want {
				t.Errorf("effectiveInterceptErrors = %v, want %v", got, tc.want)
			}
		})
	}
}
