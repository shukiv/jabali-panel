package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func snippetPruneCalls(ag *fakeAgent) []fakeCall {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []fakeCall
	for _, c := range ag.calls {
		if c.method == "nginx.snippet_dirs.prune" {
			out = append(out, c)
		}
	}
	return out
}

// A reconcile tick sends every domain name to the agent's snippet dir prune,
// once per process.
func TestReconcileAll_PrunesSnippetDirsOnceWithEveryDomainName(t *testing.T) {
	ag := &fakeAgent{}
	dr := &fakeDomainRepo{domains: map[string]*models.Domain{}}
	ur := &fakeUserRepo{users: map[string]*models.User{}}
	now := time.Now().UTC()
	for i, name := range []string{"example.com", "foo.bar.com", "off.example.org"} {
		dr.domains[name] = &models.Domain{
			OwnershipState: verifiedOwnership, ID: name, UserID: "user-1", Name: name,
			DocRoot: "/home/alice/domains/" + name + "/public_html", IsEnabled: i < 2, CreatedAt: now, UpdatedAt: now,
		}
	}
	r := New(dr, ur, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second})

	require.NoError(t, r.ReconcileAll(context.Background()))
	require.NoError(t, r.ReconcileAll(context.Background()))

	calls := snippetPruneCalls(ag)
	require.Len(t, calls, 1, "the prune runs once per process")
	keep := calls[0].params.(map[string]any)["keep"].([]string)
	require.ElementsMatch(t, []string{"example.com", "foo.bar.com", "off.example.org"}, keep)
}

// Only a complete, non-empty list may decide what goes; a skipped tick does
// not use up the one try.
func TestPruneSnippetDirs_SkipsAPartialOrEmptyList(t *testing.T) {
	ag := &fakeAgent{}
	r := New(newFakeDomainRepo(), nil, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	rows := []models.Domain{{Name: "a.test"}, {Name: "b.test"}}

	r.pruneSnippetDirs(context.Background(), rows, 3)
	r.pruneSnippetDirs(context.Background(), nil, 0)
	require.Empty(t, snippetPruneCalls(ag))

	r.pruneSnippetDirs(context.Background(), rows, 2)
	require.Len(t, snippetPruneCalls(ag), 1)
}

// An agent that fails, or predates the verb, is not asked again every tick.
func TestPruneSnippetDirs_IsTriedOncePerProcess(t *testing.T) {
	for name, err := range map[string]error{
		"older agent": &agent.AgentError{Code: agent.CodeUnknownCommand, Message: "unknown command"},
		"agent error": &agent.AgentError{Code: agent.CodeFailedPrecondition, Message: "no sites-available"},
	} {
		ag := &fakeAgent{errByMethod: map[string]error{"nginx.snippet_dirs.prune": err}}
		r := New(newFakeDomainRepo(), nil, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
		rows := []models.Domain{{Name: "a.test"}}
		r.pruneSnippetDirs(context.Background(), rows, 1)
		r.pruneSnippetDirs(context.Background(), rows, 1)
		require.Len(t, snippetPruneCalls(ag), 1, name)
	}
}
