package reconciler

import (
	"context"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// stubDockerRepo embeds the interface (only ListPortsForApp is exercised).
type stubDockerRepo struct {
	repository.DockerAppRepository
	ports []*models.DockerAppPublishedPort
}

func (s *stubDockerRepo) ListPortsForApp(context.Context, string) ([]*models.DockerAppPublishedPort, error) {
	return s.ports, nil
}

// Gitea #533: only an enabled reverse-proxy port that belongs to the app may be
// proxied to; anything else (other backend, disabled port, non-rev-proxy port)
// is rejected.
func TestDockerAppOwnsUpstreamPort(t *testing.T) {
	appID := "01APP0000000000000000000AA"
	repo := &stubDockerRepo{ports: []*models.DockerAppPublishedPort{
		{HostPort: 10042, ReverseProxy: true, Enabled: true},
		{HostPort: 10043, ReverseProxy: false, Enabled: true}, // not a rev-proxy port
		{HostPort: 10044, ReverseProxy: true, Enabled: false}, // disabled
	}}
	r := &Reconciler{dockerApps: repo, log: slog.Default()}
	dom := &models.Domain{Name: "app.example.com", DockerAppID: &appID}

	cases := map[string]bool{
		"http://127.0.0.1:10042": true,  // owned, enabled rev-proxy
		"http://127.0.0.1:10043": false, // owned but not rev-proxy
		"http://127.0.0.1:10044": false, // owned rev-proxy but disabled
		"http://127.0.0.1:19999": false, // not this app's port (another backend)
		"http://127.0.0.1":       false, // no port
		"not a url":              false,
	}
	for upstream, want := range cases {
		if got := r.dockerAppOwnsUpstreamPort(context.Background(), dom, upstream); got != want {
			t.Errorf("upstream %q: got %v want %v", upstream, got, want)
		}
	}

	// No app link → fail closed.
	if r.dockerAppOwnsUpstreamPort(context.Background(), &models.Domain{Name: "x"}, "http://127.0.0.1:10042") {
		t.Error("missing DockerAppID must fail closed")
	}
}

