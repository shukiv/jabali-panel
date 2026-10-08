package dockerapp

import (
	"errors"
	"fmt"
	"strings"
)

// Release tracks (GH #1956) keep Update from crossing a major version an app
// can't take in place. Update re-renders an install from the catalog, so
// without them a catalog bump to a new major reached every existing install:
// Odoo can't open an older major's database without a migration, and
// Nextcloud upgrades one major at a time.
//
// An entry with a track serves that track to new installs. An install on
// another track gets, in order:
//   - the current image, on Update, when its track is in update_from;
//   - a held track's image, on Update, when its track is in that held
//     track's update_from (a step on the way, e.g. Nextcloud 33 → 34);
//   - its own held track's image, when it is on one;
//   - the current image, on any other re-render (an env or domain edit),
//     when its track is in update_from;
//   - otherwise nothing: ErrNoUpdatePath.
//
// A held track keeps the entry's compose template, env and volumes; only the
// image and the version label differ. A track whose template differs needs
// its own catalog entry.

// HeldTrack is an older release line the catalog still serves to the
// installs on it.
type HeldTrack struct {
	Track        string   `yaml:"track"`
	Version      string   `yaml:"version"`
	ImageChannel string   `yaml:"image_channel"`
	UpdateFrom   []string `yaml:"update_from,omitempty"`
}

// Target is the image an install renders with, and the version label it
// carries afterwards.
type Target struct {
	Version      string
	ImageChannel string
	// Notice, set on Update only, says why the install doesn't move to the
	// entry's current version.
	Notice string
}

// ErrNoUpdatePath means the catalog has no image for an install's version:
// neither its own track nor one it can update to.
var ErrNoUpdatePath = errors.New("no update path")

// onTrack reports whether version v belongs to track t: "19.0" and "19.0.1"
// are on track "19", "1.27.3" is on "1.27" but not on "1.2".
func onTrack(v, t string) bool {
	return t != "" && (v == t || strings.HasPrefix(v, t+"."))
}

func onAnyTrack(v string, tracks []string) bool {
	for _, t := range tracks {
		if onTrack(v, t) {
			return true
		}
	}
	return false
}

// TargetFor returns the image an install recorded at installVersion renders
// with. update is true for Update, false for a re-render that only applies
// an edit.
func (e Entry) TargetFor(installVersion string, update bool) (Target, error) {
	current := Target{Version: e.Version, ImageChannel: e.ImageChannel}
	if e.Track == "" || onTrack(installVersion, e.Track) {
		return current, nil
	}
	if update && onAnyTrack(installVersion, e.UpdateFrom) {
		return current, nil
	}
	for _, h := range e.HeldTracks {
		held := Target{Version: h.Version, ImageChannel: h.ImageChannel}
		if update && onAnyTrack(installVersion, h.UpdateFrom) {
			held.Notice = fmt.Sprintf("This update moves %s from %s to %s, not to %s: %s %s can't be reached from %s in one step.",
				e.Name, installVersion, h.Version, e.Version, e.Name, e.Track, installVersion)
			if onAnyTrack(h.Version, e.UpdateFrom) {
				held.Notice += fmt.Sprintf(" Run Update again afterwards to reach %s.", e.Version)
			}
			return held, nil
		}
		if onTrack(installVersion, h.Track) {
			if update {
				held.Notice = fmt.Sprintf("%s %s is for new installs. This install stays on %s %s: Update can't move it from %s to %s in place. To move it, back it up and migrate it by hand.",
					e.Name, e.Version, e.Name, h.Version, h.Track, e.Track)
			}
			return held, nil
		}
	}
	if !update && onAnyTrack(installVersion, e.UpdateFrom) {
		return current, nil
	}
	if !update {
		if t, err := e.TargetFor(installVersion, true); err == nil {
			return Target{}, noUpdatePathError(fmt.Sprintf("The catalog no longer has an image for %s %s, the version this install is recorded as. Update it first: Update moves it to %s.",
				e.Name, installVersion, t.Version))
		}
	}
	return Target{}, noUpdatePathError(fmt.Sprintf("%s can't update this install in place: it is recorded as %s %s, and the catalog has no image for that version or one it can update to (the catalog has %s). Back it up and migrate it by hand.",
		e.Name, e.Name, installVersion, e.Version))
}

// noUpdatePathError is ErrNoUpdatePath with a message for the operator.
type noUpdatePathError string

func (e noUpdatePathError) Error() string        { return string(e) }
func (e noUpdatePathError) Is(target error) bool { return target == ErrNoUpdatePath }

// validateTracks checks the track fields: each version on its track, every
// held image digest-pinned, and no track listed twice.
func (e Entry) validateTracks() error {
	if e.Track == "" {
		if len(e.UpdateFrom) > 0 || len(e.HeldTracks) > 0 {
			return errors.New("update_from and held_tracks need a track")
		}
		return nil
	}
	if !onTrack(e.Version, e.Track) {
		return fmt.Errorf("version %q is not on track %q", e.Version, e.Track)
	}
	seen := map[string]bool{e.Track: true}
	if err := validateUpdateFrom("update_from", e.UpdateFrom, e.Track); err != nil {
		return err
	}
	for i, h := range e.HeldTracks {
		field := fmt.Sprintf("held_tracks[%d]", i)
		if h.Track == "" || seen[h.Track] {
			return fmt.Errorf("%s.track %q: must be set and differ from the entry's and the other held tracks", field, h.Track)
		}
		seen[h.Track] = true
		if !onTrack(h.Version, h.Track) {
			return fmt.Errorf("%s.version %q is not on track %q", field, h.Version, h.Track)
		}
		if err := validatePinnedImage(h.ImageChannel); err != nil {
			return fmt.Errorf("%s.image_channel %q: %w", field, h.ImageChannel, err)
		}
		if err := validateUpdateFrom(field+".update_from", h.UpdateFrom, h.Track); err != nil {
			return err
		}
	}
	return nil
}

func validateUpdateFrom(field string, from []string, own string) error {
	for i, f := range from {
		if f == "" || f == own {
			return fmt.Errorf("%s[%d] %q: must be set and differ from its own track", field, i, f)
		}
	}
	return nil
}
