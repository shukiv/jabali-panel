package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// result is the outcome of checking one app against its registry.
type result struct {
	appEntry
	NewTag     string // tag to pin (may equal the current tag on a digest refresh)
	NewVersion string
	NewDigest  string
	Major      bool
	Refresh    bool   // same tag, upstream re-pushed it (new digest)
	Unresolved bool   // track digest moved but no concrete tag matched it
	Truncated  bool   // tag listing hit the page cap
	Err        string // registry-side failure (kept per-app, run continues)
}

func (r result) action() string {
	var s string
	switch {
	case r.Err != "":
		return "❌ error: " + r.Err
	case r.Skip != "":
		return "⏭ skipped: " + r.Skip
	case r.NewTag == "":
		return "✅ up to date"
	case r.Major:
		s = "⚠ MAJOR bump — re-validate ports/env/volumes before merging"
	case r.Refresh:
		s = "↻ digest refresh (tag re-pushed upstream)"
	default:
		s = "⬆ bumped"
	}
	if r.Unresolved {
		s += " — ⚠ resolved version unknown, version field left as-is; verify manually"
	}
	return s
}

func run(catalogDir string, dryRun bool, client *regClient, out *strings.Builder) (changed int, hadErr bool, err error) {
	entries, err := loadCatalog(catalogDir)
	if err != nil {
		return 0, false, err
	}

	var results []result
	for _, e := range entries {
		r := result{appEntry: e}
		if e.Skip == "" {
			r = checkApp(e, client, dryRun)
		}
		if r.Err != "" {
			hadErr = true
		}
		if r.NewTag != "" {
			changed++
		}
		results = append(results, r)
	}

	fmt.Fprintf(out, "| App | Image tag | Version | Status |\n|---|---|---|---|\n")
	for _, r := range results {
		tagCol, verCol := "—", "—"
		if r.Ref.Tag != "" {
			tagCol = "`" + r.Ref.Tag + "`"
			verCol = r.Version
		}
		if r.NewTag != "" && r.NewTag != r.Ref.Tag {
			tagCol += " → `" + r.NewTag + "`"
		}
		if r.NewVersion != "" && r.NewVersion != r.Version {
			verCol += " → " + r.NewVersion
		}
		fmt.Fprintf(out, "| %s | %s | %s | %s |\n", r.Slug, tagCol, verCol, r.action())
		if r.Truncated {
			fmt.Fprintf(out, "| | | | ⚠ tag listing truncated at page cap — newest tag may be missed |\n")
		}
	}
	return changed, hadErr, nil
}

// checkApp queries the registry for one bumpable entry and, unless dryRun,
// rewrites its app.yaml in place. Two update signals are checked for every
// app: a newer matching tag (pickBestTag), and the current tag's digest
// having moved upstream (rolling tracks like ghost:6-alpine, odoo:18.0).
func checkApp(e appEntry, client *regClient, dryRun bool) result {
	r := result{appEntry: e}
	tags, truncated, err := client.tags(e.Ref.Host, e.Ref.Repo)
	r.Truncated = truncated
	if err != nil {
		r.Err = err.Error()
		return r
	}

	targetTag, targetParts := e.Ref.Tag, e.TagP
	bestTag, bestParts, advance := pickBestTag(e.TagP, tags)
	if advance {
		targetTag, targetParts = bestTag, bestParts
	}
	liveDigest, err := client.digest(e.Ref.Host, e.Ref.Repo, targetTag)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	if !advance && liveDigest == e.Ref.Digest {
		return r // up to date
	}

	newVersion := e.Version
	switch e.Mode {
	case modePinned:
		if advance {
			newVersion = targetParts.coreString()
		}
	case modeTrack:
		if v := resolveConcrete(client, e.Ref.Host, e.Ref.Repo, targetParts, tags, liveDigest); v != "" {
			newVersion = v
		} else {
			r.Unresolved = true
		}
	}

	r.NewTag = targetTag
	r.NewVersion = newVersion
	r.NewDigest = liveDigest
	r.Major = targetParts.Core[0] != e.TagP.Core[0]
	r.Refresh = !advance
	if dryRun {
		return r
	}
	rewritten, err := rewriteAppYaml(e.Raw, e, targetTag, newVersion, liveDigest)
	if err == nil {
		err = os.WriteFile(e.Path, []byte(rewritten), 0o644)
	}
	if err != nil {
		r.Err = err.Error()
		r.NewTag, r.NewVersion, r.NewDigest = "", "", ""
	}
	return r
}

func main() {
	catalogDir := flag.String("catalog", "../install/docker-apps", "path to the docker-apps catalog")
	dryRun := flag.Bool("dry-run", false, "report only; do not rewrite app.yaml files")
	flag.Parse()

	var out strings.Builder
	changed, hadErr, err := run(*catalogDir, *dryRun, newRegClient(), &out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog-bump:", err)
		os.Exit(1)
	}
	fmt.Print(out.String())
	fmt.Printf("\n%d app(s) with updates.\n", changed)
	if hadErr {
		// Per-app registry failures are visible in the table; a non-zero
		// exit here would kill the whole weekly PR over one flaky registry.
		fmt.Fprintln(os.Stderr, "catalog-bump: some apps failed — see table")
	}
}
