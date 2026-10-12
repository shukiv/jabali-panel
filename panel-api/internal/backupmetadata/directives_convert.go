package backupmetadata

import (
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/nginximport"
)

// maxUnconvertedLines caps the report lines for directives the import can't
// convert, so a whole server block doesn't flood the restore report.
const maxUnconvertedLines = 20

// convertUploadedDirectives handles a domain's raw nginx directives from an
// uploaded backup (GH #1993). They are admin-only config, and from an upload
// whoever made the file chose them, so they are never restored as written.
// What the domain page's "Import from an nginx config" can convert (rewrite,
// add_header, deny or expires by extension, a front controller) is added to
// the domain's typed nginx rules after its own, where the restore's
// CheckDomain holds them to the rules an account owner may set, as it does the
// backup's own rules. It clears the raw directives and returns report lines.
func convertUploadedDirectives(row *models.Domain) []string {
	raw := *row.NginxCustomDirectives
	row.NginxCustomDirectives = nil
	res := nginximport.Convert(raw)
	var notes []string
	if len(res.Rules) == 0 {
		notes = append(notes, "custom nginx directives are not restored as raw config from an uploaded backup, and none of them could be converted to the domain's nginx rules; review them and add them again in the domain's settings")
	} else {
		row.NginxRules = append(row.NginxRules, res.Rules...)
		notes = append(notes, fmt.Sprintf("custom nginx directives are not restored as raw config from an uploaded backup; %d of them were converted to the domain's nginx rules", len(res.Rules)))
	}
	for i, w := range res.Warnings {
		if i == maxUnconvertedLines {
			notes = append(notes, fmt.Sprintf("%d more custom nginx directives not converted", len(res.Warnings)-i))
			break
		}
		notes = append(notes, fmt.Sprintf("custom nginx directive not converted (line %d: %s): %s", w.Line, strings.TrimSpace(w.Source), w.Reason))
	}
	return notes
}
