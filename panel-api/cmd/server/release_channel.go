package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// releaseChannel reads the operator-selected release channel from
// server_settings (GH #445): "stable" or "development".
//
// Any config/DB problem is an ERROR, not a guess. The old helper fell back to
// "development" on every failure, so a stable host whose DB read hiccuped
// during the nightly auto-update reset to unreviewed main. updateBaseRef turns
// the error into "stay on the current build" instead.
func releaseChannel() (string, error) {
	if err := initConfig(); err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	if err := initDB(); err != nil {
		return "", fmt.Errorf("open database: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
	if err != nil {
		return "", fmt.Errorf("read server settings: %w", err)
	}
	if s == nil {
		return "", errors.New("read server settings: no settings row")
	}
	return parseReleaseChannel(s.ReleaseChannel)
}

// parseReleaseChannel accepts only the two channels the API and CLI can set.
// Anything else is an error rather than "not stable, so development".
func parseReleaseChannel(v string) (string, error) {
	switch v {
	case "stable", "development":
		return v, nil
	}
	return "", fmt.Errorf("unknown release channel %q", v)
}

// updateBaseRef picks what `jabali update` resets the checkout to.
//
//   - an unreadable channel: "HEAD", the current build. Moving anywhere would
//     be a guess, and guessing "main" on a stable host ships unreviewed code;
//   - "development": "origin/main";
//   - "stable": followStable=true, and the caller resolves the `stable` tag
//     (staying on HEAD when none has been promoted yet).
func updateBaseRef(channel string, channelErr error) (ref string, followStable bool) {
	if channelErr != nil {
		return "HEAD", false
	}
	if channel == "stable" {
		return "", true
	}
	return "origin/main", false
}
