package main

import (
	"strings"
	"testing"
)

func TestParseIMAPCSV(t *testing.T) {
	in := "host,user,password,to,port,starttls\n" +
		"imap.gmail.com,old@a.com,app pass one,new@a.com,,\n" +
		"mail.example.net,old@b.com,secret2,new@b.com,143,true\n" +
		"\n" + // blank row ignored
		"imap.gmail.com,old@c.com,secret3,new@c.com\n" // short row (no port/starttls cols)

	got, err := parseIMAPCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseIMAPCSV: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[0].Password != "app pass one" {
		t.Errorf("row0 password = %q, want spaces preserved", got[0].Password)
	}
	if got[0].Port != 0 || got[0].STARTTLS {
		t.Errorf("row0 = port %d starttls %v, want 0/false", got[0].Port, got[0].STARTTLS)
	}
	if got[1].Port != 143 || !got[1].STARTTLS {
		t.Errorf("row1 = port %d starttls %v, want 143/true", got[1].Port, got[1].STARTTLS)
	}
	if got[2].To != "new@c.com" {
		t.Errorf("row2 to = %q", got[2].To)
	}
}

func TestParseIMAPCSVColumnOrderFree(t *testing.T) {
	in := "to,password,user,host\nnew@a.com,pw,old@a.com,imap.gmail.com\n"
	got, err := parseIMAPCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseIMAPCSV: %v", err)
	}
	if got[0].Host != "imap.gmail.com" || got[0].User != "old@a.com" || got[0].To != "new@a.com" || got[0].Password != "pw" {
		t.Errorf("mapped wrong: %+v", got[0])
	}
}

func TestParseIMAPCSVErrors(t *testing.T) {
	cases := map[string]string{
		"missing column": "host,user,to\nimap,old,new\n",            // no password col
		"missing field":  "host,user,password,to\nimap,,pw,new@a\n", // empty user
		"no rows":        "host,user,password,to\n",                 // header only
		"bad port":       "host,user,password,to,port\nh,u,p,t,abc\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseIMAPCSV(strings.NewReader(in)); err == nil {
				t.Errorf("parseIMAPCSV(%s) = nil error, want error", name)
			}
		})
	}
}

func TestReadSecretLine(t *testing.T) {
	// Trailing newline stripped; interior spaces (Google app-password
	// format) preserved.
	got, err := readSecretLine(strings.NewReader("abcd efgh ijkl mnop\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "abcd efgh ijkl mnop" {
		t.Errorf("got %q, want spaces kept + newline stripped", got)
	}
	got, _ = readSecretLine(strings.NewReader("nonewline"))
	if got != "nonewline" {
		t.Errorf("got %q", got)
	}
}

func TestCollectIMAPAccountsSingle(t *testing.T) {
	got, err := collectIMAPAccounts(strings.NewReader("hunter2\n"), csvArgs{
		host: "imap.gmail.com", user: "old@a.com", to: "new@a.com", passwordStdin: true,
	})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 1 || got[0].Password != "hunter2" || got[0].To != "new@a.com" {
		t.Errorf("got %+v", got)
	}
}

func TestCollectIMAPAccountsValidation(t *testing.T) {
	cases := map[string]csvArgs{
		"missing host":       {user: "u", to: "t", passwordStdin: true},
		"missing to":         {host: "h", user: "u", passwordStdin: true},
		"no password source": {host: "h", user: "u", to: "t"},
		"both password srcs": {host: "h", user: "u", to: "t", passwordStdin: true, passwordFile: "/x"},
		"csv + single flags": {csvPath: "/x.csv", host: "h"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := collectIMAPAccounts(strings.NewReader("pw\n"), args); err == nil {
				t.Errorf("collect(%s) = nil error, want validation error", name)
			}
		})
	}
}

func TestCollectIMAPAccountsEmptyPassword(t *testing.T) {
	if _, err := collectIMAPAccounts(strings.NewReader("\n"), csvArgs{
		host: "h", user: "u", to: "t", passwordStdin: true,
	}); err == nil {
		t.Error("empty stdin password = nil error, want error")
	}
}
