package stalwartadmin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClient_Query_HappyPath(t *testing.T) {
	c := &Client{
		Binary: "/usr/local/bin/stalwart-cli",
		URL:    "http://127.0.0.1:8446",
		User:   "admin",
		Password: "secret",
		Timeout: time.Second,
	}
	c.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		// Pin every argument the production call hits.
		want := []string{
			"--url", "http://127.0.0.1:8446",
			"--user", "admin",
			"--password", "secret",
			"query", "DmarcExternalReport", "--json",
			"--filter", "receivedAt:>2026-05-20T00:00:00Z",
		}
		if !slicesEqual(args, want) {
			t.Errorf("args mismatch\nwant: %v\n got: %v", want, args)
		}
		return []byte(`[{"id":"x"}]`), nil, nil
	}
	out, err := c.Query(context.Background(), "DmarcExternalReport", "receivedAt:>2026-05-20T00:00:00Z")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var rows []map[string]any
	if uerr := json.Unmarshal(out, &rows); uerr != nil || len(rows) != 1 || rows[0]["id"] != "x" {
		t.Fatalf("unmarshal: %v rows=%+v", uerr, rows)
	}
}

func TestClient_Query_EmptyOutputBecomesEmptyArray(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(context.Context, []string) ([]byte, []byte, error) { return []byte{}, nil, nil }
	out, err := c.Query(context.Background(), "DmarcExternalReport")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "[]" {
		t.Errorf("want [], got %s", out)
	}
}

func TestClient_Query_RejectsBadType(t *testing.T) {
	c := NewClient("admin", "secret")
	cases := []string{"", "lowercase", "Has Space", "Has;Semi"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := c.Query(context.Background(), in); err == nil {
				t.Errorf("expected rejection for %q", in)
			}
		})
	}
}

func TestClient_Query_RejectsFlagInFilter(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(context.Context, []string) ([]byte, []byte, error) {
		t.Fatal("run should not be invoked when validation fails")
		return nil, nil, nil
	}
	if _, err := c.Query(context.Background(), "Domain", "--password=evil"); err == nil {
		t.Error("expected rejection for flag-shaped filter")
	}
}

func TestClient_Query_SurfacesStderr(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(context.Context, []string) ([]byte, []byte, error) {
		return nil, []byte("authentication failed (HTTP 401)"), errors.New("exit status 1")
	}
	_, err := c.Query(context.Background(), "Domain")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("expected stderr in error, got: %v", err)
	}
}

func TestClient_Get_Singleton(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "get MtaSts singleton") {
			t.Errorf("expected `get MtaSts singleton`, got: %s", joined)
		}
		return []byte(`{"mode":"testing"}`), nil, nil
	}
	out, err := c.Get(context.Background(), "MtaSts", "singleton")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "testing") {
		t.Errorf("unexpected stdout: %s", out)
	}
}

func TestClient_Get_RejectsBadID(t *testing.T) {
	c := NewClient("admin", "secret")
	for _, id := range []string{"", "../etc/passwd", "a b", "a;b"} {
		t.Run(id, func(t *testing.T) {
			if _, err := c.Get(context.Background(), "Domain", id); err == nil {
				t.Errorf("expected rejection for %q", id)
			}
		})
	}
}

func slicesEqual[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Client.Create parses the "Created <Type> <id>" stdout shape stalwart-cli
// emits on success. Pin the parse so an upstream output drift fails
// here, not at runtime.
func TestClient_Create_ParsesAssignedID(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		// Spot-check the args: create + json flag + type.
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "create MtaOutboundThrottle --json") {
			t.Errorf("expected `create MtaOutboundThrottle --json` in args, got: %s", joined)
		}
		return []byte("Created MtaOutboundThrottle irxz7ww7abaa\n"), nil, nil
	}
	id, err := c.Create(context.Background(), "MtaOutboundThrottle", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if id != "irxz7ww7abaa" {
		t.Errorf("id = %q, want %q", id, "irxz7ww7abaa")
	}
}

func TestClient_Create_RejectsUnexpectedStdout(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(context.Context, []string) ([]byte, []byte, error) {
		return []byte("Updated MtaOutboundThrottle x123"), nil, nil // wrong verb
	}
	if _, err := c.Create(context.Background(), "MtaOutboundThrottle", map[string]any{}); err == nil {
		t.Error("expected reject when stdout doesn't start with `Created`")
	}
}

func TestClient_Delete_UsesIdsFlag(t *testing.T) {
	c := NewClient("admin", "secret")
	c.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "delete MtaOutboundThrottle --ids irxz7ww7abaa") {
			t.Errorf("expected `delete --ids <id>` form, got: %s", joined)
		}
		return []byte("irxz7ww7abaa deleted\n"), nil, nil
	}
	if err := c.Delete(context.Background(), "MtaOutboundThrottle", "irxz7ww7abaa"); err != nil {
		t.Fatal(err)
	}
}
