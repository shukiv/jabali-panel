package main

import (
	"errors"
	"strings"
	"testing"
)

// The `stable` tag genuinely does not exist until the first promote, and that
// case has to stay non-fatal or a fresh host on the stable channel could never
// update at all. git's wording for it has drifted across versions, so accept
// every form we know of.
func TestClassifyStableTagFetch_MissingRefIsNotFatal(t *testing.T) {
	t.Parallel()

	for _, out := range []string{
		"fatal: couldn't find remote ref refs/tags/stable\n",
		"fatal: Couldn't find remote ref refs/tags/stable\n",
		"fatal: could not find remote ref refs/tags/stable\n",
		"error: src refspec refs/tags/stable matched no remote refs\n",
		"fatal: no such ref: refs/tags/stable\n",
	} {
		exists, fatal := classifyStableTagFetch(errors.New("exit status 128"), out)
		if fatal != nil {
			t.Errorf("output %q: want non-fatal, got %v", out, fatal)
		}
		if exists {
			t.Errorf("output %q: tag must be reported absent", out)
		}
	}
}

// The bug this guards (JAB-210). A fetch that fails for any OTHER reason leaves
// the local refs/tags/stable exactly as it was, so the rev-parse right after it
// still succeeds — against a commit promoted who-knows-when. Swallowing the
// error there resets the checkout backwards and hands the release installer a
// tarball older than the binary already running.
func TestClassifyStableTagFetch_OtherFailuresAreFatal(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"dubious ownership": "fatal: detected dubious ownership in repository at '/opt/jabali-panel'\n",
		"transport":         "fatal: unable to access 'https://github.com/...': Could not resolve host\n",
		"auth":              "remote: Invalid username or password.\nfatal: Authentication failed\n",
		"empty output":      "",
	}
	for name, out := range cases {
		exists, fatal := classifyStableTagFetch(errors.New("exit status 128"), out)
		if fatal == nil {
			t.Errorf("%s: a fetch failure that is not `no such ref` must abort the update, "+
				"otherwise a stale local tag silently downgrades the host", name)
			continue
		}
		if exists {
			t.Errorf("%s: must not report the tag as usable", name)
		}
		// The operator has to be able to act on this, so the message must
		// carry git's own words and name the way out.
		if out != "" && !strings.Contains(fatal.Error(), strings.TrimSpace(strings.Split(out, "\n")[0])) {
			t.Errorf("%s: error must quote git's output verbatim, got: %v", name, fatal)
		}
		if !strings.Contains(fatal.Error(), "development channel") {
			t.Errorf("%s: error should name the escape hatch, got: %v", name, fatal)
		}
	}
}

func TestClassifyStableTagFetch_SuccessMeansTagExists(t *testing.T) {
	t.Parallel()

	exists, fatal := classifyStableTagFetch(nil, "")
	if fatal != nil {
		t.Fatalf("a successful fetch must not be fatal: %v", fatal)
	}
	if !exists {
		t.Error("a successful fetch means the tag is on origin")
	}
}

// A "no such ref" phrase must not be matched out of an unrelated failure just
// because the words appear somewhere — check the classifier is not so loose
// that a real error slips through as "expected".
func TestClassifyStableTagFetch_DoesNotSwallowUnrelatedErrors(t *testing.T) {
	t.Parallel()

	_, fatal := classifyStableTagFetch(errors.New("exit status 128"),
		"fatal: unable to read config file: refs/tags/stable is not a valid object name\n")
	if fatal == nil {
		t.Error("an error that merely mentions the ref is not the same as the remote not having it")
	}
}
