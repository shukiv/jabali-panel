package api

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1999: the front_controller rule. The query is rendered unquoted as the
// last try_files argument, so the frontcontroller grammar is the injection
// boundary; these cases pin it at the API.
func TestValidateNginxRules_FrontController(t *testing.T) {
	ok := []models.NginxRule{
		{Type: "front_controller", Script: "/index.php", Query: "mod=$uri&$args"},
		{Type: "front_controller", Script: "/public/index.php", Query: "$query_string"},
		{Type: "front_controller", Script: "/app.php"},
	}
	for _, r := range ok {
		if err := validateNginxRules(models.NginxRules{r}); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
		if err := validateTenantNginxRules(models.NginxRules{r}); err != nil {
			t.Errorf("tenant %+v: %v", r, err)
		}
	}
	bad := map[string]models.NginxRule{
		"no script":         {Type: "front_controller", Query: "$args"},
		"not .php":          {Type: "front_controller", Script: "/index.html"},
		"traversal":         {Type: "front_controller", Script: "/../index.php"},
		"directive inject":  {Type: "front_controller", Script: "/index.php", Query: "a=1; return 302 https://evil.test"},
		"block inject":      {Type: "front_controller", Script: "/index.php", Query: "a=1}"},
		"unknown variable":  {Type: "front_controller", Script: "/index.php", Query: "a=$host"},
		"variable boundary": {Type: "front_controller", Script: "/index.php", Query: "a=$urix"},
		"newline":           {Type: "front_controller", Script: "/index.php", Query: "a=1\nb"},
	}
	for name, r := range bad {
		if err := validateNginxRules(models.NginxRules{r}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if err := validateTenantNginxRules(models.NginxRules{r}); err == nil {
			t.Errorf("tenant %s: accepted", name)
		}
	}
	// A vhost has one `location /`, so a domain has one front controller.
	two := models.NginxRules{
		{Type: "front_controller", Script: "/index.php", Query: "$query_string"},
		{Type: "front_controller", Script: "/app.php"},
	}
	if err := validateNginxRules(two); err == nil || !strings.Contains(err.Error(), "only one front_controller") {
		t.Errorf("two front controllers: got %v", err)
	}
}
