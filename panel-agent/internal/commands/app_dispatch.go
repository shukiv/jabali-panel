package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// app_dispatch.go provides the M19 generic agent commands —
// app.install, app.delete, app.clone — that read an `app_type`
// discriminator from the request body and forward the unchanged body
// to the matching app-specific handler.
//
// Each app-specific command file (e.g. wordpress_install.go) keeps its
// existing legacy registration on Default (so old wordpress.install
// callers don't break) AND opts into the dispatch table by calling
// RegisterAppInstaller / RegisterAppDeleter / RegisterAppCloner from
// its package-level init(). Adding a new app means: register on the
// legacy command name (optional), register on the app dispatch tables,
// and the panel's generic /applications routes light up automatically.

var (
	appDispatchMu sync.RWMutex
	appInstallers = map[string]Handler{}
	appDeleters   = map[string]Handler{}
	appCloners    = map[string]Handler{}
)

// RegisterAppInstaller adds h to the install dispatch table for
// appType. Panics on duplicate registration — every app declares its
// installer in exactly one init(), so a collision is a programmer
// error worth surfacing at startup, not a silent overwrite.
func RegisterAppInstaller(appType string, h Handler) {
	appDispatchMu.Lock()
	defer appDispatchMu.Unlock()
	if _, dup := appInstallers[appType]; dup {
		panic(fmt.Sprintf("agent: duplicate app installer registration %q", appType))
	}
	appInstallers[appType] = h
}

// RegisterAppDeleter mirrors RegisterAppInstaller for delete handlers.
func RegisterAppDeleter(appType string, h Handler) {
	appDispatchMu.Lock()
	defer appDispatchMu.Unlock()
	if _, dup := appDeleters[appType]; dup {
		panic(fmt.Sprintf("agent: duplicate app deleter registration %q", appType))
	}
	appDeleters[appType] = h
}

// RegisterAppCloner mirrors RegisterAppInstaller for clone handlers.
func RegisterAppCloner(appType string, h Handler) {
	appDispatchMu.Lock()
	defer appDispatchMu.Unlock()
	if _, dup := appCloners[appType]; dup {
		panic(fmt.Sprintf("agent: duplicate app cloner registration %q", appType))
	}
	appCloners[appType] = h
}

// appDispatchHead is the discriminator the dispatcher reads off every
// app.* request. The full body is forwarded unchanged — the per-app
// handler unmarshals into its own typed struct, app_type included or
// not (per-app structs ignore unknown fields).
type appDispatchHead struct {
	AppType string `json:"app_type"`
}

func dispatchByAppType(table map[string]Handler, op string, ctx context.Context, params json.RawMessage) (any, error) {
	var head appDispatchHead
	if err := json.Unmarshal(params, &head); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse app.%s params: %v", op, err),
		}
	}
	if head.AppType == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "app_type is required",
		}
	}
	appDispatchMu.RLock()
	h, ok := table[head.AppType]
	appDispatchMu.RUnlock()
	if !ok {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown app_type %q for app.%s", head.AppType, op),
		}
	}
	return h(ctx, params)
}

// appInstallHandler records what the install actually laid down (GH #946).
//
// The diff is taken around the per-app installer rather than from a static list,
// because only the filesystem knows what upstream's tree contains today. On
// delete this is what lets the cleanup remove exactly our files and nothing
// else.
func appInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p := parseAppDispatchPaths(params)
	root := p.installRoot()
	before := topLevelEntries(root)

	res, err := dispatchByAppType(appInstallers, "install", ctx, params)
	if err != nil {
		// A failed install may still have written files, but recording them as
		// "the install" would be a lie, and the panel tears down a failed
		// install by its own path. Leave no manifest.
		return res, err
	}

	if root != "" {
		var created []string
		for name := range topLevelEntries(root) {
			if !before[name] {
				created = append(created, name)
			}
		}
		writeAppManifest(root, p.AppType, created)
	}
	return res, nil
}

// appDeleteHandler sweeps the recorded entries before handing off to the
// per-app deleter (GH #946).
//
// Sweep first, dispatch second: the per-app handlers finish by restoring the
// default index page and dropping the nginx rewrite, and those must survive.
// Running them last means a file the sweep removed can be legitimately put back.
//
// The per-app deleter still runs in full. It is the fallback for installs made
// before manifests existed, and it owns the parts of teardown that are not file
// removal.
func appDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p := parseAppDispatchPaths(params)
	root := p.installRoot()

	sweepAppManifest(ctx, p.OSUser, root)

	res, err := dispatchByAppType(appDeleters, "delete", ctx, params)
	if err != nil {
		// Keep the manifest so a retried delete still knows what to remove.
		return res, err
	}
	removeAppManifestFile(root)
	return res, nil
}

func appCloneHandler(ctx context.Context, params json.RawMessage) (any, error) {
	return dispatchByAppType(appCloners, "clone", ctx, params)
}

func init() {
	Default.Register("app.install", appInstallHandler)
	Default.Register("app.delete", appDeleteHandler)
	Default.Register("app.clone", appCloneHandler)
}
