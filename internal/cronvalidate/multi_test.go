package cronvalidate

import (
	"strings"
	"testing"
)

var testDocroots = []string{"/home/u/domains/x/public_html"}

func TestValidateAnyMulti_OrderedWPSequence(t *testing.T) {
	raw := "#!/bin/bash\n" +
		"wp --path=/home/u/domains/x/public_html keyhook-properties generate-xml --file=/home/u/domains/x/public_html/wp-content/uploads/props.xml\n" +
		"\n" + // blank line tolerated
		"# import step\n" +
		"wp --path=/home/u/domains/x/public_html all-import run 1 --force-run\n"
	cmds, err := ValidateAnyMulti(raw, testDocroots, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("want 2 commands (shebang/blank/comment skipped), got %d", len(cmds))
	}
	if cmds[0].Argv[0] != "wp" || cmds[1].Argv[0] != "wp" {
		t.Fatalf("both should be wp: %v / %v", cmds[0].Argv, cmds[1].Argv)
	}
	if cmds[1].Argv[len(cmds[1].Argv)-1] != "--force-run" {
		t.Fatalf("second command mis-parsed: %v", cmds[1].Argv)
	}
}

func TestValidateAnyMulti_SingleLineBackCompat(t *testing.T) {
	cmds, err := ValidateAnyMulti("wp --path=/home/u/domains/x/public_html cron event run --due-now", testDocroots, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("single line must yield one command, got %d", len(cmds))
	}
}

func TestValidateAnyMulti_RejectsBadLineWithLineNumber(t *testing.T) {
	// line 2 tries to escape the allow-list.
	raw := "wp --path=/home/u/domains/x/public_html cron event run\n" +
		"cd /etc && cat shadow"
	_, err := ValidateAnyMulti(raw, testDocroots, nil)
	if err == nil {
		t.Fatal("expected rejection of the shell line")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should point at line 2: %v", err)
	}
}

func TestValidateAnyMulti_MetacharOnAnyLineRejected(t *testing.T) {
	raw := "wp --path=/home/u/domains/x/public_html cron event run\n" +
		"php /home/u/domains/x/public_html/a.php; rm -rf /"
	if _, err := ValidateAnyMulti(raw, testDocroots, nil); err == nil {
		t.Fatal("a metachar on any line must reject the whole job")
	}
}

func TestValidateAnyMulti_EmptyAfterSkips(t *testing.T) {
	if _, err := ValidateAnyMulti("#!/bin/bash\n\n# only comments\n", testDocroots, nil); err == nil {
		t.Fatal("a command with no executable lines must be rejected")
	}
}

func TestValidateAnyMulti_PathStillEnforcedPerLine(t *testing.T) {
	// second line's --path points outside the owned docroot.
	raw := "wp --path=/home/u/domains/x/public_html cron event run\n" +
		"wp --path=/home/other/domains/y/public_html plugin list"
	if _, err := ValidateAnyMulti(raw, testDocroots, nil); err == nil {
		t.Fatal("a --path outside owned docroots must reject the job")
	}
}

func TestValidateAnyMulti_TooMany(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxCronCommands+1; i++ {
		b.WriteString("wp --path=/home/u/domains/x/public_html cron event run\n")
	}
	if _, err := ValidateAnyMulti(b.String(), testDocroots, nil); err == nil {
		t.Fatalf("more than %d commands must be rejected", maxCronCommands)
	}
}

func TestNormalizeMultiCommand(t *testing.T) {
	raw := "#!/bin/bash\n wp --path=/x a b \n\n# c\nphp /x/y.php\n"
	got := NormalizeMultiCommand(raw)
	want := "wp --path=/x a b\nphp /x/y.php"
	if got != want {
		t.Fatalf("Normalize = %q, want %q", got, want)
	}
}
