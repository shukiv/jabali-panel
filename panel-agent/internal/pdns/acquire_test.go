package pdns

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// GH #2377: the agent connected to the PowerDNS database once, at startup.
// On the reporter's box that attempt failed, and every zone push after it
// failed with "powerdns backend not available" until the agent was
// restarted. Acquire connects again on demand.

// fakeConnect swaps connectFn and nowFn for the test and clears the package
// state before and after it.
func fakeConnect(t *testing.T, results ...error) (calls *int, clock *time.Time) {
	t.Helper()
	reset := func() {
		defaultMu.Lock()
		defaultCl, lastConnectAt, lastConnectErr = nil, time.Time{}, nil
		defaultMu.Unlock()
	}
	reset()
	origConnect, origNow := connectFn, nowFn
	t.Cleanup(func() {
		connectFn, nowFn = origConnect, origNow
		reset()
	})

	n := 0
	now := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC)
	connectFn = func() (*Client, error) {
		i := n
		n++
		if i < len(results) && results[i] != nil {
			return nil, results[i]
		}
		return &Client{}, nil
	}
	nowFn = func() time.Time { return now }
	return &n, &now
}

func TestAcquire_ReconnectsAfterAFailedStartup(t *testing.T) {
	sockGone := errors.New("dial unix /var/run/mysqld/mysqld.sock: connect: no such file or directory")
	calls, clock := fakeConnect(t, sockGone)

	// Startup: MariaDB is not up yet.
	if _, err := Acquire(); err == nil {
		t.Fatal("first attempt should fail")
	}

	// MariaDB comes up. Once the retry interval has passed, the next DNS
	// call connects, without an agent restart.
	*clock = clock.Add(connectRetryInterval)
	cl, err := Acquire()
	if err != nil {
		t.Fatalf("after the cause went away, Acquire still fails: %v", err)
	}
	if cl == nil {
		t.Fatal("Acquire returned no client")
	}
	if *calls != 2 {
		t.Fatalf("connect calls = %d, want 2", *calls)
	}

	// Connected: later calls reuse the client.
	*clock = clock.Add(time.Hour)
	if again, _ := Acquire(); again != cl {
		t.Error("a connected client must be reused")
	}
	if *calls != 2 {
		t.Errorf("connect calls = %d after reuse, want 2", *calls)
	}
}

func TestAcquire_ErrorSaysWhyAndKeepsThePanelsPrefix(t *testing.T) {
	cause := errors.New("read /etc/jabali-panel/pdns.env: no such file or directory")
	fakeConnect(t, cause)

	_, err := Acquire()
	if err == nil {
		t.Fatal("expected an error")
	}
	// The panel matches this prefix (domainops, dnsops, ownership gate).
	if !strings.HasPrefix(err.Error(), "powerdns backend not available") {
		t.Errorf("error %q lost the prefix the panel matches on", err)
	}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "pdns.env") {
		t.Errorf("error %q does not say why", err)
	}
}

func TestAcquire_RetriesAreSpacedOut(t *testing.T) {
	down := errors.New("connection refused")
	calls, clock := fakeConnect(t, down, down, down)

	for i := 0; i < 5; i++ {
		if _, err := Acquire(); err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("call %d: err = %v, want the last attempt's error", i, err)
		}
		*clock = clock.Add(time.Second)
	}
	if *calls != 1 {
		t.Fatalf("connect calls within the retry interval = %d, want 1", *calls)
	}

	*clock = clock.Add(connectRetryInterval)
	_, _ = Acquire()
	if *calls != 2 {
		t.Errorf("connect calls after the interval = %d, want 2", *calls)
	}
}
