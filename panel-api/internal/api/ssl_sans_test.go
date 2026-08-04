package api

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestWebCertSANsForDomain(t *testing.T) {
	cases := []struct {
		name string
		dom  *models.Domain
		want []string
	}{
		{
			name: "plain, www opted out (GH #895)",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: false, CreateWWW: false},
			want: []string{"example.com"},
		},
		{
			name: "plain, www opted in",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: false, CreateWWW: true},
			want: []string{"example.com", "www.example.com"},
		},
		{
			name: "email adds mail/autoconfig/autodiscover, no www",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: true, CreateWWW: false},
			want: []string{"example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"},
		},
		{
			name: "email + www opted in",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: true, CreateWWW: true},
			want: []string{"example.com", "www.example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com"},
		},
		{
			name: "mta-sts adds its SAN (www out)",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: true, MTASTSEnabled: true, CreateWWW: false},
			want: []string{"example.com", "mail.example.com", "autoconfig.example.com", "autodiscover.example.com", "mta-sts.example.com"},
		},
		{
			name: "SkipAutoSAN, www out -> base only",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: true, MTASTSEnabled: true, SkipAutoSAN: true, CreateWWW: false},
			want: []string{"example.com"},
		},
		{
			name: "SkipAutoSAN, www in -> base + www",
			dom:  &models.Domain{Name: "example.com", EmailEnabled: true, MTASTSEnabled: true, SkipAutoSAN: true, CreateWWW: true},
			want: []string{"example.com", "www.example.com"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := webCertSANsForDomain(tc.dom); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMailCertSANsForBase(t *testing.T) {
	// MTA-STS off → no mta-sts SAN.
	want := []string{"mail.example.com", "autoconfig.example.com", "autodiscover.example.com"}
	if got := mailCertSANsForBase("Example.COM", false); !reflect.DeepEqual(got, want) {
		t.Errorf("off: got %v want %v", got, want)
	}
	// MTA-STS on → mta-sts SAN appended.
	wantOn := append(append([]string{}, want...), "mta-sts.example.com")
	if got := mailCertSANsForBase("example.com", true); !reflect.DeepEqual(got, wantOn) {
		t.Errorf("on: got %v want %v", got, wantOn)
	}
}
