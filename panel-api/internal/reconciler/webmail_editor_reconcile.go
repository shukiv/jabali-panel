package reconciler

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileWebmailEditor sends webmail its WOPI document editor URL
// (GH #2501). The agent writes it to webmail's editor environment file and
// restarts webmail only when that changed. An empty URL turns editing off.
// It is the only sender: the whole setting is one value, so a save costs at
// most one webmail restart.
func (r *Reconciler) reconcileWebmailEditor(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	editorURL := ""
	s, err := r.settingsGet(ctx)
	switch {
	case err == nil && s != nil:
		editorURL = s.WebmailOfficeEditorURL
	case errors.Is(err, repository.ErrNotFound):
	default:
		r.log.Warn("reconcile webmail editor: server settings", "err", err)
		return
	}
	params := map[string]any{"wopi_client_url": editorURL}
	_, _ = r.project(ctx, PhaseWebmailEditor, "jabali-webmail", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "webmail.editor.apply", params); err != nil {
			r.log.Warn("reconcile webmail editor: apply failed", "err", err)
			return err
		}
		return nil
	})
}
