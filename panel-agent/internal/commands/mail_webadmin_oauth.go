package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #2542: Stalwart's WebAdmin signs in with OAuth, and its client id is the
// URL of its own client document on the page's origin, here
// https://<panel-host>:<port>/admin/oauth-client.json. Stalwart recognises
// its own web interface only on its own hosts, which never carry a port, so
// this id is treated as a third-party Client ID Metadata Document and refused
// ("Client metadata documents are disabled", the default policy). Registering
// an OAuthClient with that exact id is Stalwart's supported way to accept it
// without allowing metadata documents in general.
//
// Checked on Stalwart 1.0.2: no `scopes` are set (that build accepts only
// mail/contacts/calendars/files there, not full), and none are needed: the
// WebAdmin asks for openid, and the admin rights come from the account that
// signs in. Signing in shows Stalwart's consent screen for this client.

// webadminOAuthDescription marks the OAuthClient the panel owns. Only clients
// carrying it are ever replaced or removed.
const webadminOAuthDescription = "Jabali WebAdmin"

type stalwartOAuthClient struct {
	ID                     string          `json:"id,omitempty"`
	ClientID               string          `json:"clientId"`
	Description            string          `json:"description"`
	RedirectURIs           map[string]bool `json:"redirectUris"`
	PostLogoutRedirectURIs map[string]bool `json:"postLogoutRedirectUris"`
}

// webadminOAuthClient is the client the WebAdmin at https://host:port needs.
func webadminOAuthClient(serverName string, port int) stalwartOAuthClient {
	origin := fmt.Sprintf("https://%s:%d", strings.ToLower(serverName), port)
	return stalwartOAuthClient{
		ClientID:               origin + "/admin/oauth-client.json",
		Description:            webadminOAuthDescription,
		RedirectURIs:           map[string]bool{origin + "/admin/oauth/callback": true},
		PostLogoutRedirectURIs: map[string]bool{origin + "/admin/login": true},
	}
}

// syncWebadminOAuthClient makes the panel's OAuthClients match the door: with
// enabled, exactly one, for serverName:port; without, none. Clients the panel
// did not create are left alone.
func syncWebadminOAuthClient(ctx context.Context, serverName string, port int, enabled bool) error {
	var got struct {
		List []stalwartOAuthClient `json:"list"`
	}
	if err := jmapCall(ctx, "x:OAuthClient/get", map[string]any{"ids": nil}, &got); err != nil {
		return err
	}
	want := webadminOAuthClient(serverName, port)
	have := false
	var stale []string
	for _, c := range got.List {
		if c.Description != webadminOAuthDescription {
			continue
		}
		if enabled && !have && c.ClientID == want.ClientID &&
			maps.Equal(c.RedirectURIs, want.RedirectURIs) && maps.Equal(c.PostLogoutRedirectURIs, want.PostLogoutRedirectURIs) {
			have = true
			continue
		}
		stale = append(stale, c.ID)
	}
	// Destroy before create: the client id is unique, and a stale client may
	// hold the same id with other redirect addresses.
	if len(stale) > 0 {
		if err := oauthClientSet(ctx, map[string]any{"destroy": stale}); err != nil {
			return err
		}
	}
	if enabled && !have {
		want.ID = ""
		return oauthClientSet(ctx, map[string]any{"create": map[string]any{"webadmin": want}})
	}
	return nil
}

// oauthClientSet runs x:OAuthClient/set and fails on a refused create or
// destroy, which Stalwart reports inside a 200 response.
func oauthClientSet(ctx context.Context, args map[string]any) error {
	var res struct {
		NotCreated   map[string]json.RawMessage `json:"notCreated"`
		NotDestroyed map[string]json.RawMessage `json:"notDestroyed"`
	}
	if err := jmapCall(ctx, "x:OAuthClient/set", args, &res); err != nil {
		return err
	}
	for _, refused := range []map[string]json.RawMessage{res.NotCreated, res.NotDestroyed} {
		for id, why := range refused {
			return &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("stalwart refused the WebAdmin OAuth client (%s): %s", id, why)}
		}
	}
	return nil
}

// dropWebadminOAuthClient removes the panel's WebAdmin client when the door
// closes. Best-effort: with mail off there is no Stalwart to ask, and a
// leftover client only lets a closed door's address sign in.
func dropWebadminOAuthClient(ctx context.Context) {
	if err := syncWebadminOAuthClient(ctx, "", 0, false); err != nil {
		slog.Warn("webadmin: could not remove the OAuth client", "err", err)
	}
}
