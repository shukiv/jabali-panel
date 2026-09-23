package dbconsoleops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakePMAMinter struct {
	token                   string
	err                     error
	gotUser, gotDB, gotName string
	calls                   int
}

func (f *fakePMAMinter) MintToken(_ context.Context, userID, databaseID, dbName string) (string, error) {
	f.calls++
	f.gotUser, f.gotDB, f.gotName = userID, databaseID, dbName
	return f.token, f.err
}

type fakeAdminerMinter struct {
	token                     string
	err                       error
	gotUser, gotDB, gotEngine string
	calls                     int
}

func (f *fakeAdminerMinter) MintAdminerToken(_ context.Context, userID, databaseID, engine string) (string, error) {
	f.calls++
	f.gotUser, f.gotDB, f.gotEngine = userID, databaseID, engine
	return f.token, f.err
}

func TestIssuePhpMyAdminLogin_Success(t *testing.T) {
	m := &fakePMAMinter{token: "pmatoken"}
	url, prefix, err := IssuePhpMyAdminLogin(context.Background(), m, "u1", "db1", "shop", "https://panel.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The phpMyAdmin console leaf must build a phpMyAdmin redirect — never an
	// Adminer one. This is the JAB-348 AC3 pairing invariant: a phpMyAdmin token
	// can only leave through the phpMyAdmin door.
	if !strings.Contains(url, phpMyAdminSSOPath) {
		t.Errorf("url %q missing phpMyAdmin path %q", url, phpMyAdminSSOPath)
	}
	if strings.Contains(url, adminerSSOPath) {
		t.Errorf("phpMyAdmin leaf leaked an Adminer path into %q", url)
	}
	if !strings.Contains(url, "token=pmatoken") || !strings.Contains(url, "db=shop") {
		t.Errorf("url %q missing token/db scope", url)
	}
	if strings.Contains(url, "engine=") {
		t.Errorf("phpMyAdmin url %q must not carry an engine param", url)
	}
	if want := TokenAuditPrefix("pmatoken"); prefix != want {
		t.Errorf("hashPrefix = %q, want %q (must match the validate-side digest)", prefix, want)
	}
	if m.gotUser != "u1" || m.gotDB != "db1" || m.gotName != "shop" {
		t.Errorf("minter got (%q,%q,%q), want (u1,db1,shop)", m.gotUser, m.gotDB, m.gotName)
	}
}

func TestIssueAdminerLogin_Success(t *testing.T) {
	m := &fakeAdminerMinter{token: "admtoken"}
	url, prefix, err := IssueAdminerLogin(context.Background(), m, "u2", "db2", "shop", "postgres", "https://panel.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The Adminer console leaf must build an Adminer redirect — never a phpMyAdmin
	// one (the AC3 pairing invariant, mirror of the test above).
	if !strings.Contains(url, adminerSSOPath) {
		t.Errorf("url %q missing Adminer path %q", url, adminerSSOPath)
	}
	if strings.Contains(url, phpMyAdminSSOPath) {
		t.Errorf("Adminer leaf leaked a phpMyAdmin path into %q", url)
	}
	if !strings.Contains(url, "token=admtoken") || !strings.Contains(url, "engine=postgres") {
		t.Errorf("url %q missing token/engine scope", url)
	}
	if want := TokenAuditPrefix("admtoken"); prefix != want {
		t.Errorf("hashPrefix = %q, want %q", prefix, want)
	}
	if m.gotUser != "u2" || m.gotDB != "db2" || m.gotEngine != "postgres" {
		t.Errorf("minter got (%q,%q,%q), want (u2,db2,postgres)", m.gotUser, m.gotDB, m.gotEngine)
	}
}

func TestIssueLogin_MintError(t *testing.T) {
	sentinel := errors.New("boom")

	if url, prefix, err := IssuePhpMyAdminLogin(context.Background(),
		&fakePMAMinter{err: sentinel}, "u", "d", "n", "https://b"); !errors.Is(err, sentinel) || url != "" || prefix != "" {
		t.Errorf("phpMyAdmin mint error: got (%q,%q,%v), want empty url/prefix and the underlying error", url, prefix, err)
	}
	if url, prefix, err := IssueAdminerLogin(context.Background(),
		&fakeAdminerMinter{err: sentinel}, "u", "d", "n", "postgres", "https://b"); !errors.Is(err, sentinel) || url != "" || prefix != "" {
		t.Errorf("Adminer mint error: got (%q,%q,%v), want empty url/prefix and the underlying error", url, prefix, err)
	}
}
