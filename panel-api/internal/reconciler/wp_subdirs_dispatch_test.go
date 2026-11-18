package reconciler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The reconciler↔agent seam for subfolder WordPress routes: exactly the
// shape the agent's sanitizeWPSubdir accepts ("/shop", "/shop/eu").
func TestWordPressSubdirsFromInstalls(t *testing.T) {
	wp := func(subdir string) models.ApplicationInstall {
		return models.ApplicationInstall{AppType: "wordpress", Subdirectory: subdir}
	}
	app := func(appType, subdir string) models.ApplicationInstall {
		return models.ApplicationInstall{AppType: appType, Subdirectory: subdir}
	}
	cases := []struct {
		name string
		in   []models.ApplicationInstall
		want []string
	}{
		{"none", nil, []string{}},
		{"root install only", []models.ApplicationInstall{wp("")}, []string{}},
		{"root + shop", []models.ApplicationInstall{wp(""), wp("shop")}, []string{"/shop"}},
		{"slashes and spaces normalized", []models.ApplicationInstall{wp(" /shop/ ")}, []string{"/shop"}},
		{"nested", []models.ApplicationInstall{wp("shop/eu")}, []string{"/shop/eu"}},
		{"dedupe", []models.ApplicationInstall{wp("blog"), wp("/blog/")}, []string{"/blog"}},
		{"sorted whatever the row order", []models.ApplicationInstall{wp("shop"), wp("blog")}, []string{"/blog", "/shop"}},
		// Drupal/Joomla/ITFlow write their own `location ^~ /<sub>/` snippet.
		{"other app types left out", []models.ApplicationInstall{app("drupal", "d"), app("joomla", "j"), wp("shop")}, []string{"/shop"}},
	}
	for _, c := range cases {
		if got := wordPressSubdirsFromInstalls(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: wordPressSubdirsFromInstalls = %v, want %v", c.name, got, c.want)
		}
	}
}

// installsByDomain answers ListByDomainIDs from a map; everything else is the
// shared mock.
type installsByDomain struct {
	mockWordPressInstallRepo
	byDomain map[string][]models.ApplicationInstall
	err      error
}

func (f *installsByDomain) ListByDomainIDs(_ context.Context, ids []string) ([]models.ApplicationInstall, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []models.ApplicationInstall
	for _, id := range ids {
		out = append(out, f.byDomain[id]...)
	}
	return out, nil
}

// A subfolder WordPress reaches the agent's domain.create payload, adding one
// re-dispatches, and a failed lookup dispatches nothing (a vhost without the
// route would 404 every permalink under the folder).
func TestCreateDomainOnAgent_WPSubdirsReachAgent(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	repo := &installsByDomain{byDomain: map[string][]models.ApplicationInstall{
		dom.ID: {
			{AppType: "wordpress", Subdirectory: ""},
			{AppType: "wordpress", Subdirectory: "shop"},
		},
	}}
	r.WithWordPressInstalls(repo)

	r.createDomainOnAgent(context.Background(), dom, false)
	call, ok := findAgentCall(ag, "domain.create")
	if !ok {
		t.Fatal("domain.create was not dispatched")
	}
	params := call.params.(map[string]any)
	if got, _ := params["wp_subdirs"].([]string); !reflect.DeepEqual(got, []string{"/shop"}) {
		t.Fatalf("wp_subdirs param = %#v, want [/shop]", params["wp_subdirs"])
	}

	// Unchanged installs on the next periodic tick: no re-dispatch.
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 1 {
		t.Fatalf("unchanged installs must not re-dispatch, got %d", got)
	}

	// A new subfolder install flips the hash: the vhost is re-rendered.
	repo.byDomain[dom.ID] = append(repo.byDomain[dom.ID], models.ApplicationInstall{AppType: "wordpress", Subdirectory: "blog"})
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("a new subfolder install must re-dispatch, got %d", got)
	}

	// Lookup failure: no dispatch, even when forced.
	repo.err = errors.New("db gone")
	r.createDomainOnAgent(context.Background(), dom, true)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("a failed install lookup must not dispatch, got %d", got)
	}
}

// No subfolder WordPress: the key is absent, so the payload (and the vhost)
// is byte-identical to before.
func TestCreateDomainOnAgent_NoWPSubdirsKeyWhenNone(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	r.WithWordPressInstalls(&installsByDomain{byDomain: map[string][]models.ApplicationInstall{
		dom.ID: {{AppType: "wordpress", Subdirectory: ""}},
	}})
	r.createDomainOnAgent(context.Background(), dom, false)
	call, ok := findAgentCall(ag, "domain.create")
	if !ok {
		t.Fatal("domain.create was not dispatched")
	}
	if _, present := call.params.(map[string]any)["wp_subdirs"]; present {
		t.Fatal("wp_subdirs must be omitted when the domain has no subfolder WordPress")
	}
}
