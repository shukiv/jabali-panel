// Package agentwire holds the on-the-wire protocol types used by both
// panel-api (client, internal/agent) and panel-agent (server, internal/server).
// Keeping these in a top-level shared package is the only way to avoid Go's
// internal/ import rule firing when the agent binary needs the same envelope
// types the client sends.
//
// Changes to anything here are a breaking change on both sides — add, don't
// rename, and never remove an existing field without a deprecation window.
package agentwire

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Request is the envelope sent client → agent.
//
// ID is a ULID minted by the client; the agent MUST echo it in the
// Response.ID so log lines correlate across processes. Deadline, if
// present, is an RFC-3339 wall-clock cutoff — agents that spawn subprocesses
// should honour it.
type Request struct {
	ID       string          `json:"id"`
	Command  string          `json:"command"`
	Params   json.RawMessage `json:"params,omitempty"`
	Deadline string          `json:"deadline,omitempty"`
}

// Response is the envelope sent agent → client. Exactly one of Data or Error
// is populated; Ok mirrors that flag for readable switch statements.
type Response struct {
	ID    string          `json:"id"`
	Ok    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *AgentError     `json:"error,omitempty"`
}

// AgentError is a typed error an agent command can emit. Code is a short
// machine tag ("invalid_argument", "not_found" …); Message is human-facing;
// Details is optional structured context (bad field names, subprocess stderr
// excerpts, …).
type AgentError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// Error implements the error interface so callers can use errors.As to
// recover the typed form.
func (e *AgentError) Error() string {
	if e == nil {
		return "<nil agent error>"
	}
	if e.Message == "" {
		return fmt.Sprintf("agent: %s", e.Code)
	}
	return fmt.Sprintf("agent: %s: %s", e.Code, e.Message)
}

// Well-known error codes. Additive growth is OK; renames are not.
const (
	CodeInvalidArgument  = "invalid_argument"
	CodeNotFound         = "not_found"
	CodeAlreadyExists    = "already_exists"
	CodePermissionDenied = "permission_denied"
	CodeUnavailable      = "unavailable"
	// CodeUpstreamUnavailable: the agent itself is fine but an external
	// upstream it must reach (e.g. the enclosed note service) is not.
	// Distinct from CodeUnavailable, which the panel's client also uses for
	// "can't dial the local agent socket" — conflating the two would make an
	// egress problem look like a dead agent.
	CodeUpstreamUnavailable = "upstream_unavailable"
	CodeDeadlineExceeded    = "deadline_exceeded"
	CodeInternal            = "internal"
	CodeUnknownCommand      = "unknown_command"
	CodeMalformedEnvelope   = "malformed_envelope"
	CodeFailedPrecondition  = "failed_precondition"
)

// ErrMalformedResponse is raised by the client when response bytes don't
// parse as a Response. Not a protocol-defined value — client-side sentinel.
var ErrMalformedResponse = errors.New("agent: malformed response")

// ErrResponseIDMismatch is raised by the client when the response ID
// doesn't match the request ID it was paired with.
var ErrResponseIDMismatch = errors.New("agent: response id mismatch")
