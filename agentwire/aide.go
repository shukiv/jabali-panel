package agentwire

// AIDE baseline rebuild in the background (GH #2079). A rebuild runs
// `aide --init` over the whole host, which takes minutes, longer than an HTTP
// request may last. The agent's security.aide.rebuild starts
// `jabali aide rebuild --full --result-file AideRebuildResultPath` in the
// transient unit AideRebuildUnit and returns; the command records its outcome
// in that file, and security.aide.status reports it.

// AideRebuildUnit is the transient systemd unit a panel-started rebuild runs in.
const AideRebuildUnit = "jabali-aide-rebuild.service"

// AideRebuildResultPath is the file `jabali aide rebuild --result-file`
// writes. It lives next to the AIDE database, which only root can read.
const AideRebuildResultPath = "/var/lib/aide/.jabali-rebuild.json"

// AideRebuildResult is the content of AideRebuildResultPath. Running is true
// from the start of a rebuild until it ends; OK and Error describe the end.
// Times are RFC 3339, UTC.
type AideRebuildResult struct {
	Running    bool   `json:"running"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}
