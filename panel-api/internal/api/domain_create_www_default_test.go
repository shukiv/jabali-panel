package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A domain created without a www choice gets www when it is a main domain
// (two-part suffixes such as co.il included), not when it is a subdomain; an
// explicit choice wins either way.
func TestCreateDomainOp_WWWDefault(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	on, off := true, false
	cases := []struct {
		name   string
		choice *bool
		want   bool
	}{
		{"example.co.il", nil, true},
		{"example.com", nil, true},
		{"blog.example.com", nil, false},
		{"example.co.il", &off, false},
		{"blog.example.com", &on, true},
	}
	for _, c := range cases {
		h := &domainHandler{cfg: DomainHandlerConfig{
			Users:          newAbUsers(owner),
			Domains:        newDCDomains(),
			ServerSettings: &fakeStatusSettingsRepo{s: &models.ServerSettings{}},
		}}
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:       owner.ID,
			Name:          c.name,
			SSLMode:       models.SSLModeLE,
			CreateWWW:     c.choice,
			SkipInlineSSL: true,
		})
		if oerr != nil {
			t.Fatalf("%s: %v", c.name, oerr)
		}
		if d.CreateWWW != c.want {
			t.Errorf("%s (choice %v): CreateWWW = %v, want %v", c.name, c.choice, d.CreateWWW, c.want)
		}
	}
}
