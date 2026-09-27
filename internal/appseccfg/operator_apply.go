package appseccfg

import "fmt"

// operator_apply.go — GH #1650. The panel→agent contract for writing the
// operator before-plugin file (CRSPluginOperatorBeforePath) from the panel.
//
// panel-api runs as `jabali` (JAB-357) and cannot write /var/lib/crowdsec or
// reload crowdsec, so a row it adds (a Flarum install's scoped exclusion, or
// the admin UI in #1649) would do nothing live until root next ran
// `jabali appsec render-config`. The agent verb closes that gap.
//
// The params are the COMPLETE desired state: every exclusion and every host
// mode, read from the database in one pass. The agent renders them with
// RenderOperatorBeforeFile — the same function `render-config` uses — so the
// two writers produce byte-identical files for the same rows and never undo
// each other. Invalid rows therefore render as SKIPPED comments here too,
// rather than failing the whole apply: one stale row must not block every
// other exclusion from going live.

// OperatorApplyVerb is the agent verb that writes the operator file.
const OperatorApplyVerb = "security.appsec.operator.apply"

// MaxOperatorApplyRows bounds each list on the wire. The renderer already caps
// what it emits (and says so in the file); this only bounds the request itself.
// It sits well above the render caps, so any list the renderer would handle is
// accepted and the agent's output stays identical to render-config's.
const MaxOperatorApplyRows = 5000

// OperatorApplyParams is the request body for OperatorApplyVerb.
//
// Both lists are REQUIRED, and an empty list must be sent as [] rather than
// null. A missing key is refused instead of read as "no entries": otherwise a
// panel bug that forgot one list would silently remove every live entry of
// that kind (e.g. drop a host out of detect mode and start blocking it again).
// Build it with NewOperatorApplyParams, which never sends null.
type OperatorApplyParams struct {
	Exclusions []Exclusion `json:"exclusions"`
	HostModes  []HostMode  `json:"host_modes"`
}

// NewOperatorApplyParams returns params whose lists are never nil, so they
// marshal as [] rather than null.
func NewOperatorApplyParams(list []Exclusion, modes []HostMode) OperatorApplyParams {
	if list == nil {
		list = []Exclusion{}
	}
	if modes == nil {
		modes = []HostMode{}
	}
	return OperatorApplyParams{Exclusions: list, HostModes: modes}
}

// Check refuses a request whose shape is wrong. It does not validate the rows
// themselves: RenderOperatorBeforeFile does that, one row at a time.
func (p OperatorApplyParams) Check() error {
	if p.Exclusions == nil || p.HostModes == nil {
		return fmt.Errorf("exclusions and host_modes are both required (send [] for none)")
	}
	if n := len(p.Exclusions); n > MaxOperatorApplyRows {
		return fmt.Errorf("%d exclusions exceeds the limit of %d", n, MaxOperatorApplyRows)
	}
	if n := len(p.HostModes); n > MaxOperatorApplyRows {
		return fmt.Errorf("%d host modes exceeds the limit of %d", n, MaxOperatorApplyRows)
	}
	return nil
}

// OperatorApplyResult is the verb's response.
type OperatorApplyResult struct {
	// Changed is true when the file was written or removed.
	Changed bool `json:"changed"`
	// Reloaded is true when crowdsec was reloaded (or restarted) to load it.
	Reloaded bool `json:"reloaded"`
	// Skipped names why nothing was attempted (crowdsec is not installed).
	Skipped string `json:"skipped,omitempty"`
}
