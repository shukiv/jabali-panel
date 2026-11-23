package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeScanDomains map[string]*models.Domain

func (f fakeScanDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := f[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type fakeScanInstalls struct {
	existing map[string]*models.ApplicationInstall // domainID|subdir|type
	created  []*models.ApplicationInstall
}

func (f *fakeScanInstalls) FindByDomainAndSubdirectoryAndAppType(_ context.Context, domainID, subdirectory, appType string) (*models.ApplicationInstall, error) {
	if in, ok := f.existing[domainID+"|"+subdirectory+"|"+appType]; ok {
		return in, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeScanInstalls) Create(_ context.Context, in *models.ApplicationInstall) error {
	f.created = append(f.created, in)
	return nil
}

func scanFixture() (*agent.MockClient, fakeScanDomains, *fakeScanInstalls, *models.User) {
	username := "alice"
	u := &models.User{ID: "U1", Username: &username, Email: "alice@example.com"}
	ag := agent.NewMockClient().On("app.scan", map[string]any{"hits": []map[string]string{
		{"domain": "shop.example", "subdirectory": "", "app_type": "wordpress", "version": "6.8.1", "admin_username": "shopadmin"},
		{"domain": "shop.example", "subdirectory": "blog", "app_type": "wordpress"},
		{"domain": "old.example", "subdirectory": "", "app_type": "joomla"},
		{"domain": "bob.example", "subdirectory": "", "app_type": "wordpress"},
		{"domain": "gone.example", "subdirectory": "", "app_type": "drupal"},
	}})
	domains := fakeScanDomains{
		"shop.example": {ID: "D1", UserID: "U1", Name: "shop.example"},
		"old.example":  {ID: "D2", UserID: "U1", Name: "old.example"},
		"bob.example":  {ID: "D3", UserID: "U2", Name: "bob.example"},
	}
	installs := &fakeScanInstalls{existing: map[string]*models.ApplicationInstall{
		"D2||joomla": {ID: "I-old"},
	}}
	return ag, domains, installs, u
}

func TestRegisterScannedApps_AddsOnlyUntrackedHitsOnOwnDomains(t *testing.T) {
	ag, domains, installs, u := scanFixture()
	scanned, added, report, err := registerScannedApps(context.Background(), ag, domains, installs, u, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 5 || added != 2 {
		t.Fatalf("scanned=%d added=%d, want 5/2", scanned, added)
	}
	want := []string{"added", "added", "skipped_already_present", "skipped_domain_owned_by_other_user", "skipped_no_domain"}
	for i, r := range report {
		if r.Action != want[i] {
			t.Errorf("report[%d] %s/%s = %s, want %s", i, r.Domain, r.Subdirectory, r.Action, want[i])
		}
	}
	root, sub := installs.created[0], installs.created[1]
	if root.UserID != "U1" || root.DomainID != "D1" || root.Status != "ready" || root.AppType != "wordpress" ||
		root.AdminUsername != "shopadmin" || root.AdminEmail != "alice@example.com" || root.Version == nil || *root.Version != "6.8.1" {
		t.Errorf("root install row = %+v", root)
	}
	if sub.Subdirectory != "blog" || sub.AdminUsername != "alice" || sub.Version != nil {
		t.Errorf("subfolder install row = %+v (admin falls back to the Linux user, no version)", sub)
	}
}

func TestRegisterImportedApps_PrintsCountsAndWhatWasLeftOut(t *testing.T) {
	ag, domains, installs, u := scanFixture()
	var out bytes.Buffer
	registerImportedApps(context.Background(), &out, ag, domains, installs, u)
	got := out.String()
	for _, w := range []string{
		"apps: 5 found, 2 registered",
		"wordpress bob.example/ not registered: skipped_domain_owned_by_other_user",
		"drupal gone.example/ not registered: skipped_no_domain",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "old.example") {
		t.Errorf("an install already tracked is not a problem to report:\n%s", got)
	}
	if len(installs.created) != 2 {
		t.Errorf("created %d rows, want 2", len(installs.created))
	}
}

func TestRegisterImportedApps_AgentFailureIsAWarning(t *testing.T) {
	_, domains, installs, u := scanFixture()
	ag := agent.NewMockClient().OnError("app.scan", errors.New("socket closed"))
	var out bytes.Buffer
	registerImportedApps(context.Background(), &out, ag, domains, installs, u)
	if !strings.Contains(out.String(), "warning: apps not registered") || !strings.Contains(out.String(), "jabali app scan --user alice") {
		t.Errorf("output = %q", out.String())
	}
	if len(installs.created) != 0 {
		t.Errorf("created %d rows on a failed scan", len(installs.created))
	}
}

func TestRegisterImportedApps_ChecksUploadPathOfEachWordPressSite(t *testing.T) {
	ag, domains, installs, u := scanFixture()
	resp, err := os.ReadFile("../../internal/agent/testdata/wordpress_upload_path_fix_response.json")
	if err != nil {
		t.Fatal(err)
	}
	ag.On("wordpress.upload_path_fix", json.RawMessage(resp))
	var out bytes.Buffer
	registerImportedApps(context.Background(), &out, ag, domains, installs, u)

	var params []json.RawMessage
	for _, c := range ag.Calls() {
		if c.Command == "wordpress.upload_path_fix" {
			params = append(params, c.Params)
		}
	}
	// The two WordPress sites registered; not the Joomla site, nor the hits on
	// another user's or an unknown domain.
	if len(params) != 2 {
		t.Fatalf("upload_path_fix calls = %d, want 2: %s", len(params), params)
	}
	want, err := os.ReadFile("../../internal/agent/testdata/wordpress_upload_path_fix_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, wantMap map[string]any
	_ = json.Unmarshal(params[1], &got)
	_ = json.Unmarshal(want, &wantMap)
	if !reflect.DeepEqual(got, wantMap) {
		t.Errorf("subfolder site params = %v, want the contract fixture %v", got, wantMap)
	}
	if !strings.Contains(out.String(), "wordpress shop.example/blog: upload_path /home/alice/public_html/shop.example/blog/wp-content/uploads was the old server's folder; cleared") {
		t.Errorf("output:\n%s", out.String())
	}
}
