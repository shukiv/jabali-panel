package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// `jabali service action` must refuse stop and disable on every unit the admin
// API refuses them on. Before, the CLI guarded only jabali-panel, jabali-agent
// and mariadb, so `jabali service action jabali-kratos stop --force` went
// through and locked every admin out of the panel.
func TestServiceAction_RefusesPanelSelfDestruct(t *testing.T) {
	prev := sharedAgent
	t.Cleanup(func() { sharedAgent = prev })
	dialed := 0
	sharedAgent = agent.NewClient(agent.Config{
		SocketPath: "/nonexistent/agent.sock",
		Dial: func(context.Context, string) (net.Conn, error) {
			dialed++
			return nil, errors.New("test: agent must not be called")
		},
	})

	units := []string{"jabali-panel", "jabali-agent", "jabali-kratos", "mariadb", "nginx", "redis-server"}
	for _, unit := range units {
		for _, action := range []string{"stop", "disable"} {
			dialed = 0
			cmd := newServiceActionCmd()
			if err := cmd.Flags().Set("force", "true"); err != nil {
				t.Fatal(err)
			}
			cmd.SetContext(context.Background())
			err := cmd.RunE(cmd, []string{unit, action})
			if err == nil || !strings.Contains(err.Error(), "would brick the management plane") {
				t.Errorf("%s %s: err = %v, want the self-destruct refusal", action, unit, err)
			}
			if dialed != 0 {
				t.Errorf("%s %s: the agent was called", action, unit)
			}
		}
	}
}
