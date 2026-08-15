// Package cpanel implements the M35 Discoverer + Restorer for cPanel
// source panels. Discovery connects via SSH (operator's choice of
// password or key) and runs UAPI / whmapi1 in JSON mode. Restore
// invokes `pkgacct` on the source to produce a tarball + pulls it
// locally for offline parsing.
//
// Read-only contract: every cPanel command this package shells out
// to is a list / show variant. We never run an `add`, `set`, or
// `delete` UAPI call against the source.
package cpanel

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UAPI wraps the cPanel UAPI JSON envelope:
//
//	{
//	  "result": {
//	    "data": ...,
//	    "errors": [...] | null,
//	    "messages": [...] | null,
//	    "metadata": {...},
//	    "status": 0 | 1,
//	    "warnings": [...] | null
//	  }
//	}
//
// status=1 is success. errors is a string slice or null on success;
// callers should join the slice when surfacing the error to the
// operator.
type uapiEnvelope struct {
	Result struct {
		Data     json.RawMessage `json:"data"`
		Errors   []string        `json:"errors"`
		Messages []string        `json:"messages"`
		Metadata map[string]any  `json:"metadata"`
		Status   int             `json:"status"`
		Warnings []string        `json:"warnings"`
	} `json:"result"`
}

// decodeUAPI unmarshals stdout from `uapi --output=jsonpretty
// Module function ...` into out. Returns an error containing the
// joined errors slice when status != 1.
func decodeUAPI(stdout []byte, out any) error {
	var env uapiEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		// cPanel sometimes prepends "uapi" usage text on a
		// not-installed binary. Surface the head so the operator
		// can diagnose without re-running.
		head := stdout
		if len(head) > 200 {
			head = head[:200]
		}
		return fmt.Errorf("uapi: malformed JSON envelope: %w (head=%q)", err, head)
	}
	if env.Result.Status != 1 {
		return fmt.Errorf("uapi: %s", strings.Join(env.Result.Errors, "; "))
	}
	if out == nil {
		return nil
	}
	if len(env.Result.Data) == 0 || string(env.Result.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Result.Data, out)
}

// whmapi1Envelope is the WHM-side JSON envelope:
//
//	{
//	  "metadata": { "result": 1, "reason": "...", ... },
//	  "data": { ... }
//	}
//
// Used when the connected principal has root / WHM access.
type whmapi1Envelope struct {
	Metadata struct {
		Result int    `json:"result"`
		Reason string `json:"reason"`
	} `json:"metadata"`
	Data json.RawMessage `json:"data"`
}

func decodeWHMAPI1(stdout []byte, out any) error {
	var env whmapi1Envelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		head := stdout
		if len(head) > 200 {
			head = head[:200]
		}
		return fmt.Errorf("whmapi1: malformed JSON envelope: %w (head=%q)", err, head)
	}
	if env.Metadata.Result != 1 {
		return fmt.Errorf("whmapi1: %s", env.Metadata.Reason)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// userInformation is the smallest payload we can fetch to validate
// a cPanel session — UAPI Variables::get_user_information returns
// the connected user's home + email + bandwidth_usage. Empty home
// or status != 1 means our principal isn't actually a cPanel user.
type userInformation struct {
	User          string `json:"user"`
	Home          string `json:"home"`
	Email         string `json:"email"`
	BandwidthUsed int64  `json:"bandwidth_usage"`
}

// listAccts is one row of `whmapi1 listaccts` data.acct[]. We
// pull the small set we surface in AccountSummary; the full row
// has dozens more fields the importer doesn't need.
type listAccts struct {
	Acct []struct {
		User      string `json:"user"`
		Email     string `json:"email"`
		Domain    string `json:"domain"`
		DiskUsed  string `json:"diskused"` // "1234M" — string, not int
		DiskLimit string `json:"disklimit"`
		Suspended int    `json:"suspended"` // 0 | 1
	} `json:"acct"`
}
