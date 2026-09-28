package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
)

// mail_throttle.go — outbound mail throttles (M47 Wave 3), pushed into
// Stalwart on the panel's behalf.
//
// The panel keeps the policy (mail_outbound_policy) but cannot reach
// Stalwart's admin API: the admin credential is readable by root and
// jabali-mail only (JAB-357). These two verbs are the narrow door. Each takes
// typed fields, builds the one Stalwart object type itself
// (mailthrottle.Payload), and never accepts a Stalwart payload or type name
// from the panel.
//
//	mail.throttle.apply   mailthrottle.ApplyRequest  -> mailthrottle.ApplyResult
//	mail.throttle.delete  mailthrottle.DeleteRequest -> mailthrottle.DeleteResult
//
// apply is idempotent: with a known id it reads the object first and writes
// only when it differs, so the reconciler can call it on every tick. An id
// Stalwart no longer has is replaced by a new object. delete treats an id
// Stalwart no longer has as done. Every write is followed by a settings
// reload (reloadStalwartSettings), as for the other MTA config objects.

// errStalwartNotFound marks a stalwart-cli call whose object id does not exist.
var errStalwartNotFound = errors.New("stalwart object not found")

func mailThrottleApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req mailthrottle.ApplyRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if err := req.Validate(); err != nil {
		return nil, csInvalidArg(err.Error())
	}
	want := mailthrottle.Payload(req)
	body, err := json.Marshal(want)
	if err != nil {
		return nil, csInternal("marshal throttle", err)
	}

	if req.StalwartID != "" {
		out, err := runStalwartObjectCLI(ctx, req.StalwartID, "get", mailthrottle.StalwartType, req.StalwartID, "--json")
		switch {
		case errors.Is(err, errStalwartNotFound):
			// Gone from Stalwart (deleted by hand, or a restore): create a new one below.
		case err != nil:
			return nil, err
		default:
			var cur mailthrottle.Throttle
			if err := json.Unmarshal(bytes.TrimSpace(out), &cur); err != nil {
				return nil, csInternal("parse stalwart throttle "+req.StalwartID, err)
			}
			if cur.Equal(want) {
				return mailthrottle.ApplyResult{StalwartID: req.StalwartID, Changed: false}, nil
			}
			_, err := runStalwartObjectCLI(ctx, req.StalwartID, "update", mailthrottle.StalwartType, req.StalwartID, "--json", string(body))
			if err == nil {
				if err := mailThrottleReload(ctx); err != nil {
					return nil, err
				}
				return mailthrottle.ApplyResult{StalwartID: req.StalwartID, Changed: true}, nil
			}
			if !errors.Is(err, errStalwartNotFound) {
				return nil, err
			}
		}
	}

	out, err := runStalwartObjectCLI(ctx, "", "create", mailthrottle.StalwartType, "--json", string(body))
	if err != nil {
		return nil, err
	}
	id, err := parseStalwartCreated(out, mailthrottle.StalwartType)
	if err != nil {
		return nil, err
	}
	if err := mailThrottleReload(ctx); err != nil {
		return nil, err
	}
	return mailthrottle.ApplyResult{StalwartID: id, Changed: true}, nil
}

func mailThrottleDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req mailthrottle.DeleteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, csInvalidArg(fmt.Sprintf("parse params: %v", err))
	}
	if err := req.Validate(); err != nil {
		return nil, csInvalidArg(err.Error())
	}
	_, err := runStalwartObjectCLI(ctx, req.StalwartID, "delete", mailthrottle.StalwartType, "--ids", req.StalwartID)
	if errors.Is(err, errStalwartNotFound) {
		return mailthrottle.DeleteResult{Deleted: false}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := mailThrottleReload(ctx); err != nil {
		return nil, err
	}
	return mailthrottle.DeleteResult{Deleted: true}, nil
}

// parseStalwartCreated reads the id out of stalwart-cli's
// "Created <Type> <id>" line.
func parseStalwartCreated(out []byte, typeName string) (string, error) {
	line := strings.TrimSpace(string(out))
	prefix := "Created " + typeName + " "
	id := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !strings.HasPrefix(line, prefix) || !mailthrottle.ValidStalwartID(id) {
		return "", csInternal("stalwart-cli create", fmt.Errorf("unexpected output %q", line))
	}
	return id, nil
}

// mailThrottleReload makes a changed throttle take effect. A var so tests
// can count reloads without a Stalwart.
var mailThrottleReload = reloadStalwartSettings

// runStalwartObjectCLI runs stalwart-cli like runStalwartCLI (credentials in
// env only), but keeps stdout apart from stderr so a caller can parse it, and
// returns errStalwartNotFound when the call failed because the object id does
// not exist. stalwart-cli reports that as "notFound" (update, delete) or
// "<Type> <id> not found" (get), with exit status 1.
func runStalwartObjectCLI(ctx context.Context, id string, args ...string) ([]byte, error) {
	token, err := stalwartAdminTokenFunc()
	if err != nil {
		return nil, csInternal("stalwart-admin token unreadable", err)
	}
	cmd := execCommandContext(ctx, "stalwart-cli", args...)
	cmd.Env = append(os.Environ(),
		"STALWART_URL="+stalwartAdminURLFunc(),
		"STALWART_USER="+jmapAdminUser,
		"STALWART_PASSWORD="+token,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		msg := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if id != "" && (strings.Contains(msg, "notFound") || strings.Contains(msg, id+" not found")) {
			return nil, errStalwartNotFound
		}
		if msg == "" {
			msg = runErr.Error()
		}
		// The token only ever travels in env; scrub anyway in case a future
		// stalwart-cli prints its config.
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "<redacted>")
		}
		if len(msg) > 512 {
			msg = msg[:512] + "…(truncated)"
		}
		return nil, csInternal("stalwart-cli "+args[0], errors.New(msg))
	}
	return stdout.Bytes(), nil
}

func init() {
	Default.Register(mailthrottle.VerbApply, mailThrottleApplyHandler)
	Default.Register(mailthrottle.VerbDelete, mailThrottleDeleteHandler)
}
