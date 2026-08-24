package main

// JAB-317: CLI shared-cert delete must be source-aware — revoke ACME lineages,
// delete_shared uploaded files — matching the HTTP handler.

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestSharedCertTeardown_SourceAware(t *testing.T) {
	cases := []struct {
		name       string
		cert       *models.SharedCertificate
		wantVerb   string
		wantDomain string // "" means expect an "id" param instead
	}{
		{
			name:     "uploaded → delete_shared by id",
			cert:     &models.SharedCertificate{ID: "c1", Name: "example.com", Source: models.SharedCertSourceUploaded},
			wantVerb: "ssl.delete_shared",
		},
		{
			name:       "acme → revoke the lineage",
			cert:       &models.SharedCertificate{ID: "c2", Name: "example.com", Source: models.SharedCertSourceACME},
			wantVerb:   "ssl.revoke",
			wantDomain: "example.com",
		},
		{
			name:       "acme wildcard → revoke the wildcard.<zone> lineage",
			cert:       &models.SharedCertificate{ID: "c3", Name: "*.example.com", Source: models.SharedCertSourceACME},
			wantVerb:   "ssl.revoke",
			wantDomain: "wildcard.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, params := sharedCertTeardown(tc.cert)
			if verb != tc.wantVerb {
				t.Fatalf("verb = %q, want %q", verb, tc.wantVerb)
			}
			if tc.wantDomain != "" {
				if params["domain"] != tc.wantDomain {
					t.Fatalf("domain = %v, want %q", params["domain"], tc.wantDomain)
				}
				if _, hasID := params["id"]; hasID {
					t.Fatal("ACME revoke must not send an id param")
				}
			} else {
				if params["id"] != tc.cert.ID {
					t.Fatalf("id = %v, want %q", params["id"], tc.cert.ID)
				}
			}
		})
	}
}
