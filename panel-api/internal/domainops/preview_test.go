package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakePreviewLister struct {
	rows []models.Domain
	err  error
}

func (f fakePreviewLister) ListPreviewEnabled(context.Context) ([]models.Domain, error) {
	return f.rows, f.err
}

func TestPreviewSlugConflict(t *testing.T) {
	rows := []models.Domain{
		{ID: "a", Name: "shop.example.com", TempURLEnabled: true},
		{ID: "b", Name: "My-Site.com.", TempURLEnabled: true},
		{ID: "c", Name: "other-site.com", TempURLEnabled: false},
	}
	cases := []struct {
		name, domain, self, want string
	}{
		{"no collision", "blog.example.com", "", ""},
		{"dots vs dashes collide", "my.site.com", "", "My-Site.com."},
		{"case and trailing dot are ignored", "MY.SITE.COM.", "", "My-Site.com."},
		{"a domain never collides with itself", "my-site.com", "b", ""},
		{"a preview-off row never collides", "other.site.com", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreviewSlugConflict(context.Background(), fakePreviewLister{rows: rows}, tc.domain, tc.self)
			if err != nil || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}

	t.Run("a store error is returned", func(t *testing.T) {
		cause := errors.New("db down")
		got, err := PreviewSlugConflict(context.Background(), fakePreviewLister{err: cause}, "my.site.com", "")
		if got != "" || !errors.Is(err, cause) {
			t.Fatalf("got (%q, %v), want (\"\", %v)", got, err, cause)
		}
	})
}
