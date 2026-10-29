package reconciler

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2328: each path row applies to the domain and www.<domain>, and a row
// whose domain is gone is not written.
func TestBotPathExclusionsFor(t *testing.T) {
	domains := []models.Domain{
		{ID: "d1", Name: "Shop.example"},
		{ID: "d2", Name: "api.example"},
	}
	rows := []models.BotChallengeExclusion{
		{DomainID: "d2", PathPrefix: "/hook", Methods: "POST"},
		{DomainID: "gone", PathPrefix: "/x"},
		{DomainID: "d1", PathPrefix: "/feed"},
	}
	got := botPathExclusionsFor(domains, rows)
	want := []appseccfg.BotPathExclusion{
		{Host: "api.example", PathPrefix: "/hook", Methods: []string{"POST"}},
		{Host: "www.api.example", PathPrefix: "/hook", Methods: []string{"POST"}},
		{Host: "shop.example", PathPrefix: "/feed"},
		{Host: "www.shop.example", PathPrefix: "/feed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("botPathExclusionsFor =\n%+v\nwant\n%+v", got, want)
	}
	if got := botPathExclusionsFor(domains, nil); got == nil || len(got) != 0 {
		t.Fatalf("no rows: got %#v, want an empty, non-nil list (the state file is emptied)", got)
	}
}
