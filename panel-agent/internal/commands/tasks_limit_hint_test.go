package commands

import (
	"context"
	"strings"
	"testing"
)

// GH #1820: "Failed to spawn executor: Resource temporarily unavailable" was
// the account's Max tasks limit (package max_tasks=20, four Python apps). The
// errors the panel stored never said so.

func showProps(lines ...string) string {
	return "printf '" + strings.Join(lines, `\n`) + `\n'`
}

func TestTasksLimitHint(t *testing.T) {
	cases := []struct {
		name string
		show string
		want string // substring; "" means no hint
	}{
		{"slice full", showProps("Slice=jabali-user-bob.slice", "TasksCurrent=21", "TasksMax=20"), "Max tasks limit (21 of 20 in use)"},
		{"exactly at limit", showProps("Slice=jabali-user-bob.slice", "TasksCurrent=20", "TasksMax=20"), "Max tasks limit (20 of 20 in use)"},
		{"room left", showProps("Slice=jabali-user-bob.slice", "TasksCurrent=5", "TasksMax=20"), ""},
		{"no limit", showProps("Slice=jabali-user-bob.slice", "TasksCurrent=500", "TasksMax=infinity"), ""},
		{"not a user slice", showProps("Slice=system.slice", "TasksCurrent=21", "TasksMax=20"), ""},
		{"systemctl fails", "exit 1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSystemctl(t, map[string]string{"show": tc.show})
			got := tasksLimitHint(context.Background(), "jabali-fpm@bob.service")
			if tc.want == "" {
				if got != "" {
					t.Fatalf("want no hint, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("hint %q must contain %q", got, tc.want)
			}
		})
	}
}

func TestRestartOrReloadUserFPM_FullSliceNamesMaxTasks(t *testing.T) {
	stubSystemctl(t, map[string]string{
		"restart": failWith(systemctlResourcesMsg),
		"show":    showProps("Slice=jabali-user-bob.slice", "TasksCurrent=21", "TasksMax=20"),
	})
	err := restartOrReloadUserFPM(context.Background(), "bob-php8.3", "8.3", "8.4")
	if err == nil || !strings.Contains(err.Error(), "Max tasks limit (21 of 20 in use)") {
		t.Fatalf("restart error must name the Max tasks limit, got %v", err)
	}
	if !strings.Contains(err.Error(), "unavailable resources") {
		t.Fatalf("systemctl's own reason must stay in the error, got %v", err)
	}
}

func TestRestartOrReloadUserFPM_ReloadFullSliceNamesMaxTasks(t *testing.T) {
	stubSystemctl(t, map[string]string{
		"reload":    failWith(systemctlResourcesMsg),
		"is-active": "exit 0",
		"show":      showProps("Slice=jabali-user-bob.slice", "TasksCurrent=21", "TasksMax=20"),
	})
	err := restartOrReloadUserFPM(context.Background(), "bob", "8.4", "8.4")
	if err == nil || !strings.Contains(err.Error(), "Max tasks limit (21 of 20 in use)") {
		t.Fatalf("reload error must name the Max tasks limit, got %v", err)
	}
}
