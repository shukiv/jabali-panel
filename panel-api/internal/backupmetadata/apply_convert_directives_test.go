package backupmetadata

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: raw nginx directives an administrator added to a domain are not
// restored from an uploaded backup, since whoever made the file chose them.
// What the domain page's "Import from an nginx config" can convert is added
// to the restored domain's typed nginx rules instead, after the backup's own
// rules, and every line it doesn't convert is listed in the report. The
// restore's CheckDomain then holds the converted rules to the tenant rules,
// as it does the backup's own.
func TestApply_UploadedBackupConvertsCustomDirectivesToRules(t *testing.T) {
	pools := &ppPools{}
	doms := &ppDomains{pools: pools}
	meta := ppMeta()
	meta.PHPPools, meta.Domains[0].PHPPoolID, meta.Domains[0].Mailboxes = nil, nil, nil
	meta.Domains[0].NginxRules = `[{"type":"custom_header","name":"X-Own","value":"1"}]`
	raw := strings.Join([]string{
		"rewrite ^/old/(.*)$ /new/$1 permanent;",
		"add_header X-Frame-Options SAMEORIGIN;",
		"location /x { fastcgi_pass unix:/run/php/bob.sock; }",
	}, "\n")
	meta.Domains[0].NginxCustomDirectives = &raw
	var checked []string
	deps := ppDeps(pools, doms, &dcMailboxes{})
	deps.Untrusted = true
	deps.CheckDomain = func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		for _, r := range row.NginxRules {
			checked = append(checked, r.Type)
		}
		return nil, nil
	}

	r := Apply(context.Background(), meta, deps)

	if len(doms.created) != 1 {
		t.Fatalf("domains = %+v (errors %v), want alice.org", doms.created, r.Errors)
	}
	d := doms.created[0]
	if d.NginxCustomDirectives != nil {
		t.Fatalf("raw directives = %q, want none from an uploaded backup", *d.NginxCustomDirectives)
	}
	var got []string
	for _, rule := range d.NginxRules {
		got = append(got, rule.Type+":"+rule.Name+rule.Pattern)
	}
	want := "custom_header:X-Own,rewrite:^/old/(.*)$,custom_header:X-Frame-Options"
	if strings.Join(got, ",") != want {
		t.Fatalf("nginx rules = %v, want %s (the backup's own rule first, then the converted ones)", got, want)
	}
	if strings.Join(checked, ",") != "custom_header,rewrite,custom_header" {
		t.Fatalf("CheckDomain saw rules %v, want the converted rules checked with the backup's own", checked)
	}
	if !hasError(r.Errors, "2 of them were converted to the domain's nginx rules") {
		t.Fatalf("errors %v should say how many directives were converted", r.Errors)
	}
	if !hasError(r.Errors, "custom nginx directive not converted (line 3") {
		t.Fatalf("errors %v should list the fastcgi_pass location as not converted", r.Errors)
	}
}

// A backup whose directives hold nothing the import can convert says so.
func TestApply_UploadedBackupNothingToConvert(t *testing.T) {
	pools := &ppPools{}
	doms := &ppDomains{pools: pools}
	meta := ppMeta()
	meta.PHPPools, meta.Domains[0].PHPPoolID, meta.Domains[0].Mailboxes = nil, nil, nil
	raw := "location /x { fastcgi_pass unix:/run/php/bob.sock; }"
	meta.Domains[0].NginxCustomDirectives = &raw
	deps := ppDeps(pools, doms, &dcMailboxes{})
	deps.Untrusted = true

	r := Apply(context.Background(), meta, deps)

	if len(doms.created) != 1 || len(doms.created[0].NginxRules) != 0 || doms.created[0].NginxCustomDirectives != nil {
		t.Fatalf("domains = %+v (errors %v), want alice.org with no rules and no raw directives", doms.created, r.Errors)
	}
	if !hasError(r.Errors, "none of them could be converted to the domain's nginx rules") {
		t.Fatalf("errors %v should say nothing was converted", r.Errors)
	}
}

// A whole server block of directives the import can't convert lists the first
// 20 and counts the rest, so the restore report stays readable.
func TestConvertUploadedDirectives_CapsTheUnconvertedLines(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "client_max_body_size 64m;")
	}
	raw := strings.Join(lines, "\n")
	row := &models.Domain{NginxCustomDirectives: &raw}

	notes := convertUploadedDirectives(row)

	listed := 0
	for _, n := range notes {
		if strings.HasPrefix(n, "custom nginx directive not converted (line ") {
			listed++
		}
	}
	if listed != 20 || notes[len(notes)-1] != "5 more custom nginx directives not converted" {
		t.Fatalf("listed %d lines, last note %q; want 20 and a count of the other 5", listed, notes[len(notes)-1])
	}
}
