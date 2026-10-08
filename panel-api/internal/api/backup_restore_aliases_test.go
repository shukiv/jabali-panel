package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a restored web domain alias (GH #1625) goes through the alias
// page's checks: the same hostname rules and collision guards as an alias
// added on the domain's Aliases tab.

func raCheck() func(context.Context, *models.Domain, string) (string, error) {
	return RestoreAliasCheck(
		rdcDomains{"other.com": {ID: "d-o", Name: "other.com", UserID: "u-other"}},
		rdcAliases{"aliased.com": {ID: "a1", DomainID: "d-x", Hostname: "aliased.com"}},
		rdcSettings{},
	)
}

func TestRestoreAliasCheck_StoresTheHostnameAsThePageDoes(t *testing.T) {
	host, err := raCheck()(context.Background(), rdcRow("site.org"), "  Shop.Example.NET ")
	if err != nil || host != "shop.example.net" {
		t.Fatalf("host %q err %v, want shop.example.net", host, err)
	}
}

func TestRestoreAliasCheck_RefusesWhatTheAliasPageRefuses(t *testing.T) {
	noWeb := rdcRow("site.org")
	noWeb.WebDisabled = true
	for _, tc := range []struct {
		name string
		dom  *models.Domain
		host string
		want string
	}{
		{"not a hostname", rdcRow("site.org"), "bad host", "invalid"},
		{"the domain itself", rdcRow("site.org"), "site.org", "already the domain's primary name"},
		{"its www name", rdcRow("site.org"), "www.site.org", "www option"},
		{"a domain without web", noWeb, "shop.example.net", "web hosting disabled"},
		{"the panel's hostname", rdcRow("site.org"), "panel.example.net", "reserved by the panel"},
		{"another domain", rdcRow("site.org"), "other.com", "already a domain on this server"},
		{"another domain's mail name", rdcRow("site.org"), "mail.other.com", "reserved mail/web name for the domain other.com"},
		{"another alias", rdcRow("site.org"), "aliased.com", "already an alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, err := raCheck()(context.Background(), tc.dom, tc.host)
			if err == nil || host != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("host %q err %v, want refused: %q", host, err, tc.want)
			}
		})
	}
}

// Any store missing refuses: an unwired guard would find no conflict, and
// without the settings the panel's hostname would pass.
func TestRestoreAliasCheck_UnwiredRefuses(t *testing.T) {
	doms, als, set := rdcDomains{}, rdcAliases{}, rdcSettings{}
	for _, tc := range []struct {
		name  string
		check func(context.Context, *models.Domain, string) (string, error)
		host  string
	}{
		{"nothing", RestoreAliasCheck(nil, nil, nil), "shop.example.net"},
		{"no domains", RestoreAliasCheck(nil, als, set), "shop.example.net"},
		{"no alias store", RestoreAliasCheck(doms, nil, set), "shop.example.net"},
		{"no settings", RestoreAliasCheck(doms, als, nil), "panel.example.net"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if host, err := tc.check(context.Background(), rdcRow("site.org"), tc.host); err == nil {
				t.Fatalf("stored %q; an unwired check must refuse", host)
			}
		})
	}
}

// The admin and the tenant backup both carry the domains' aliases: each hands
// its alias store to the shared builder, and the panel wires it into both.
func TestBackupProducers_CarryTheAliases(t *testing.T) {
	aliases := struct {
		repository.WebDomainAliasRepository
	}{}
	if got := (BackupHandlerConfig{WebDomainAliases: aliases}).metadataDeps().WebDomainAliases; got != aliases {
		t.Fatalf("admin backup: builder alias store %v, want the handler's", got)
	}
	if got := (MeBackupsHandlerConfig{WebDomainAliases: aliases}).metadataDeps().WebDomainAliases; got != aliases {
		t.Fatalf("tenant backup: builder alias store %v, want the handler's", got)
	}
	src, err := os.ReadFile("../app/app.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, lit := range []string{"api.BackupHandlerConfig{", "api.MeBackupsHandlerConfig{"} {
		// The literal runs to the "})" indented as the line that opens it.
		i := strings.Index(s, lit)
		if i < 0 {
			t.Fatalf("app.go: no %s literal", lit)
		}
		line := s[strings.LastIndex(s[:i], "\n")+1 : i]
		indent := line[:len(line)-len(strings.TrimLeft(line, "\t"))]
		j := strings.Index(s[i:], "\n"+indent+"})")
		if j < 0 || !strings.Contains(s[i:i+j], "WebDomainAliases:") {
			t.Errorf("app.go: %s must wire WebDomainAliases", lit)
		}
	}
}

// Both restore doors restore aliases through the checks.
func TestRestoreDoors_WireTheAliasChecks(t *testing.T) {
	aliases := struct {
		repository.WebDomainAliasRepository
	}{}
	h := &backupHandler{cfg: BackupHandlerConfig{WebDomainAliases: aliases}}
	deps := h.restoreMetadataDeps(nil)
	if deps.CheckAlias == nil || deps.WebDomainAliases != aliases {
		t.Fatalf("restore deps: CheckAlias set %v, alias store %v; want both wired", deps.CheckAlias != nil, deps.WebDomainAliases)
	}
	src, err := os.ReadFile("../../cmd/server/account_restore_cmd.go")
	if err != nil || !strings.Contains(string(src), "CheckAlias:") || !strings.Contains(string(src), "WebDomainAliases:") {
		t.Fatal("`jabali account restore` must wire WebDomainAliases and CheckAlias")
	}
}
