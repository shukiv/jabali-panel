package models

import "encoding/json"

// DNSSECEnableKey is one key in a dns.dnssec_enable agent reply.
type DNSSECEnableKey struct {
	KeyTag    int    `json:"key_tag"`
	KeyType   string `json:"key_type"`
	Algorithm uint8  `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Active    bool   `json:"active"`
}

// DNSSECEnableReply mirrors the agent's dns.dnssec_enable success payload
// (the handler returns {ok:true, keys:[...]}; the transport layer already
// turns a transport-level failure into an error before this point).
type DNSSECEnableReply struct {
	Ok   bool              `json:"ok"`
	Keys []DNSSECEnableKey `json:"keys"`
}

// ParseDNSSECEnableReply parses a dns.dnssec_enable reply and reports whether
// it is a well-formed SUCCESS (ok=true bit set by the agent handler).
//
// JAB-322 — fail closed: a reply that fails to parse or carries ok=false is
// NOT a success, and callers MUST leave dnssec_enabled unchanged rather than
// flipping the flag on a malformed/unsuccessful reply. Both the HTTP handler
// and the CLI go through this one function so they can never drift on the
// gate. (The happy path is byte-identical to the old `_ = json.Unmarshal`:
// a real enable returns ok=true, so this simply stops trusting a reply that
// the old code silently accepted.)
func ParseDNSSECEnableReply(raw []byte) (DNSSECEnableReply, bool) {
	var r DNSSECEnableReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return DNSSECEnableReply{}, false
	}
	return r, r.Ok
}
