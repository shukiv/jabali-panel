package commands

import (
	"context"
	"testing"
	"time"
)

func TestCronUnitExitCode(t *testing.T) {
	cases := []struct {
		props map[string]string
		want  int
	}{
		{map[string]string{"Result": "success", "ExecMainCode": "1", "ExecMainStatus": "0"}, 0},
		{map[string]string{"Result": "exit-code", "ExecMainCode": "1", "ExecMainStatus": "2"}, 2},
		{map[string]string{"Result": "signal", "ExecMainCode": "2", "ExecMainStatus": "9"}, 137},
		{map[string]string{"Result": "core-dump", "ExecMainCode": "3", "ExecMainStatus": "11"}, 139},
		{map[string]string{"Result": "timeout", "ExecMainCode": "0", "ExecMainStatus": "0"}, 1},
	}
	for _, tc := range cases {
		if got := cronUnitExitCode(tc.props); got != tc.want {
			t.Errorf("%v: got %d want %d", tc.props, got, tc.want)
		}
	}
}

// The wait must end before the call's deadline, with room to read the
// result and answer, or the panel gives up on the call first (GH #2122).
func TestCronRunNowWait(t *testing.T) {
	if got := cronRunNowWait(context.Background(), 0); got != cronRunNowDefaultWait {
		t.Errorf("no wait asked, no deadline: got %s want %s", got, cronRunNowDefaultWait)
	}
	if got := cronRunNowWait(context.Background(), 5); got != 5*time.Second {
		t.Errorf("asked 5 s: got %s", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := cronRunNowWait(ctx, 20); got > 7*time.Second || got < 6*time.Second {
		t.Errorf("deadline 10 s away: the wait must end before it with margin; got %s", got)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if got := cronRunNowWait(short, 20); got != time.Second {
		t.Errorf("deadline 2 s away: got %s want the 1 s floor", got)
	}
}
