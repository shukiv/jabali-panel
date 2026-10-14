package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// settings_cmd_support_chat_test.go — `jabali settings set support_chat_admin=…
// support_chat_clients=…` turns the Ask Jabali chat on or off from the shell,
// with the same panel CSP side effect and failed-enable revert as the REST
// PATCH (settingsops.SupportChatEffect).

// touchChat marks the chat switches as set by this `settings set`, as the key
// setters do.
func touchChat(t *testing.T) {
	t.Helper()
	supportChatDirty = true
	t.Cleanup(func() { supportChatDirty = false })
}

func TestSettingsSupportChatKeys(t *testing.T) {
	t.Cleanup(func() { supportChatDirty = false })
	for key, get := range map[string]func(*models.ServerSettings) bool{
		"support_chat_admin":   func(s *models.ServerSettings) bool { return s.SupportChatAdmin },
		"support_chat_clients": func(s *models.ServerSettings) bool { return s.SupportChatClients },
	} {
		supportChatDirty = false
		def, ok := settableKeys[key]
		require.True(t, ok, "%s must be settable", key)
		s := &models.ServerSettings{ID: 1}
		require.NoError(t, def.apply(s, "on"))
		require.True(t, get(s), key)
		require.True(t, supportChatDirty, "%s must mark the CSP side effect", key)
		require.NoError(t, def.apply(s, "off"))
		require.False(t, get(s), key)
		require.Error(t, def.apply(s, "maybe"), key)
	}
}

func TestApplySideEffects_SupportChatOnAppliesTheCSP(t *testing.T) {
	calls := stubSettingsDispatch(t, nil)
	touchChat(t)
	after := models.ServerSettings{SupportChatAdmin: true}

	err := applySettingsSideEffects(context.Background(), applyCmd(), &recordingRepo{current: &models.ServerSettings{}}, &after, models.ServerSettings{})

	require.NoError(t, err)
	require.Len(t, *calls, 1)
	require.Equal(t, "nginx.panel_csp.apply", (*calls)[0].Method)
	require.Equal(t, map[string]any{"support_chat": true}, (*calls)[0].Params)
	require.Positive(t, (*calls)[0].Timeout)
}

// The widget can't load under the old CSP, so a failed enable must not leave
// the DB claiming the chat is on.
func TestApplySideEffects_SupportChatFailedEnableSwitchesBack(t *testing.T) {
	stubSettingsDispatch(t, errors.New("nginx -t failed"))
	touchChat(t)
	before := models.ServerSettings{SupportChatAdmin: false, SupportChatClients: false}
	after := models.ServerSettings{SupportChatAdmin: true, SupportChatClients: true}
	repo := &recordingRepo{current: &models.ServerSettings{SupportChatAdmin: true, SupportChatClients: true}}

	err := applySettingsSideEffects(context.Background(), applyCmd(), repo, &after, before)

	require.Error(t, err)
	require.Contains(t, err.Error(), "switched back")
	require.Len(t, repo.upserts, 1)
	require.False(t, repo.upserts[0].SupportChatAdmin)
	require.False(t, repo.upserts[0].SupportChatClients)
}

func TestApplySideEffects_SupportChatFailedDisableStaysOff(t *testing.T) {
	stubSettingsDispatch(t, errors.New("agent down"))
	touchChat(t)
	before := models.ServerSettings{SupportChatAdmin: true}
	after := models.ServerSettings{}
	repo := &recordingRepo{current: &models.ServerSettings{}}

	err := applySettingsSideEffects(context.Background(), applyCmd(), repo, &after, before)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "switched back")
	require.Empty(t, repo.upserts, "a failed disable keeps the switches off")
}

func TestApplySideEffects_SupportChatUntouchedSendsNothing(t *testing.T) {
	calls := stubSettingsDispatch(t, nil)
	supportChatDirty = false
	s := models.ServerSettings{SupportChatAdmin: true}

	require.NoError(t, applySettingsSideEffects(context.Background(), applyCmd(), nil, &s, s))
	require.Empty(t, *calls)
}

// Setting a switch to its current value re-applies the CSP, as a re-save in
// the UI does, so the command heals a drifted snippet.
func TestApplySideEffects_SupportChatResaveReapplies(t *testing.T) {
	calls := stubSettingsDispatch(t, nil)
	touchChat(t)
	s := models.ServerSettings{SupportChatClients: true}

	require.NoError(t, applySettingsSideEffects(context.Background(), applyCmd(), nil, &s, s))
	require.Len(t, *calls, 1)
	require.Equal(t, map[string]any{"support_chat": true}, (*calls)[0].Params)
}
