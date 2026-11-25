// Package crowdsecctl checks that crowdsec's AppSec listener comes back after a
// reload, and restarts crowdsec when it does not.
//
// A `systemctl reload crowdsec` (SIGHUP) can leave crowdsec hung: the engine
// logs that it is shutting down and that the AppSec runner is dying, then
// nothing. systemd still reports the unit active, but the AppSec listener and
// the LAPI are gone until someone restarts crowdsec, and that stop then hangs
// for systemd's full 90-second timeout. Seen on crowdsec 1.8.x. The same reload
// worked a few minutes earlier, so it is intermittent, and every writer of
// crowdsec config (exclusions, login IPs, geoblock, bot detection, hub items)
// reloads.
package crowdsecctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AppsecAcquisPath is jabali's AppSec acquisition. Without it the host runs no
// AppSec listener, so there is nothing to wait for.
const AppsecAcquisPath = "/etc/crowdsec/acquis.d/jabali-appsec.yaml"

// AppsecURL is the AppSec listener the nginx bouncer calls.
const AppsecURL = "http://127.0.0.1:7422/"

// Recovery waits for AppSec after a reload and restarts a hung crowdsec.
type Recovery struct {
	// Run runs a command and returns its combined output.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Up reports whether the AppSec listener answers.
	Up func(ctx context.Context) bool
	// Settle passes before the first check: SIGHUP returns at once and the
	// old listener goes away a moment later, so an early check would see it.
	Settle time.Duration
	// Wait is how long crowdsec gets to bring AppSec back.
	Wait time.Duration
	// Poll is the interval between checks.
	Poll time.Duration
	// Again, when set, reports a reload made since the wait began; the wait
	// then starts over, so back-to-back reloads are not taken for a hang.
	Again func() bool
}

// Default returns a Recovery that runs real commands and checks AppsecURL.
// A reload brings AppSec back in a few seconds; 30 seconds leaves room for a
// large config before crowdsec is treated as hung.
func Default() Recovery {
	return Recovery{
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		Up:     AppsecAnswers,
		Settle: 3 * time.Second,
		Wait:   30 * time.Second,
		Poll:   500 * time.Millisecond,
	}
}

// AppsecExpected reports whether this host runs the AppSec listener.
func AppsecExpected() bool {
	_, err := os.Stat(AppsecAcquisPath)
	return err == nil
}

// AppsecAnswers reports whether the AppSec listener answers HTTP. Any status
// counts (a request without the bouncer key is refused); a refused connection
// or a timeout means the listener is gone or hung.
func AppsecAnswers(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, AppsecURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// ErrStillDown means AppSec did not answer even after crowdsec was restarted.
var ErrStillDown = errors.New("AppSec is still not answering after a crowdsec restart")

// AfterReload waits for AppSec to answer after a reload. If it does not within
// Wait, crowdsec is hung: it is killed and restarted, and restarted is true. A
// hung crowdsec ignores SIGTERM for systemd's whole stop timeout, so it is
// killed first rather than left to `systemctl restart`.
func (r Recovery) AfterReload(ctx context.Context) (restarted bool, err error) {
	if r.waitUp(ctx, r.Settle) {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	_, _ = r.Run(ctx, "systemctl", "kill", "--signal=SIGKILL", "crowdsec")
	if out, err := r.Run(ctx, "systemctl", "restart", "crowdsec"); err != nil {
		return true, fmt.Errorf("AppSec did not come back after a crowdsec reload, and the restart failed: %v (%s)",
			err, strings.TrimSpace(string(out)))
	}
	if !r.waitUp(ctx, 0) {
		return true, ErrStillDown
	}
	return true, nil
}

// waitUp sleeps settle, then checks Up every Poll until it answers or Wait has
// passed.
func (r Recovery) waitUp(ctx context.Context, settle time.Duration) bool {
	if !sleep(ctx, settle) {
		return false
	}
	deadline := time.Now().Add(r.Wait)
	for {
		if r.Again != nil && r.Again() {
			if !sleep(ctx, r.Settle) {
				return false
			}
			deadline = time.Now().Add(r.Wait)
		}
		if r.Up(ctx) {
			return true
		}
		if !time.Now().Before(deadline) || !sleep(ctx, r.Poll) {
			return false
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
