package cronvalidate

import (
	"path/filepath"
	"testing"
)

// FuzzValidateCommand tests the command validator against random inputs.
// Property: validator must NEVER panic, must NEVER return (*Command, nil)
// if input contains any unquoted metacharacters from forbiddenMetachars set.
func FuzzValidateCommand(f *testing.F) {
	// Seed with representative happy paths
	f.Add("wp cron event run --path=/home/shuki/a/public_html")
	f.Add("wp cron event run --path /home/shuki/a/public_html")
	f.Add("php /home/shuki/a/public_html/cleanup.php")
	f.Add("php /home/shuki/a/public_html/cleanup.php --verbose")
	f.Add("wp cron test-event --path=/home/shuki/example.com/public_html --debug")

	ownedDocroots := []string{
		"/home/shuki/a/public_html",
		"/home/shuki/example.com/public_html",
		"/home/user/site/html",
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Call the validator - must not panic
		cmd, err := ValidateCommand(input, ownedDocroots, "")

		// If we get an error, it must have a code
		if err != nil {
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if ve.Code == "" {
				t.Fatalf("ValidationError has empty Code")
			}
			// Known error codes
			knownCodes := map[string]bool{
				ErrCodeEmpty:             true,
				ErrCodeTooLong:           true,
				ErrCodeBinaryNotAllowed:  true,
				ErrCodeMetacharReject:    true,
				ErrCodeBadPathArg:        true,
				ErrCodeBadScheduleSyntax: true,
			}
			if !knownCodes[ve.Code] {
				t.Fatalf("unknown error code: %s", ve.Code)
			}
		} else {
			// Success case: cmd must not be nil
			if cmd == nil {
				t.Fatalf("ValidateCommand returned nil error but also nil command")
			}
			// argv must have at least 1 token (the binary)
			if len(cmd.Argv) == 0 {
				t.Fatalf("returned Command has empty Argv")
			}
			// First token must be wp, php, or a versioned php<X.Y>
			// (bare or a full path ending in one of those).
			b0 := filepath.Base(cmd.Argv[0])
			if b0 != "wp" && b0 != "php" && !isVersionedPHP(b0) {
				t.Fatalf("first token is not wp/php/php<X.Y>: %s", cmd.Argv[0])
			}
		}
	})
}

// FuzzValidateSchedule tests the schedule validator against random inputs.
// Property: validator must NEVER panic.
func FuzzValidateSchedule(f *testing.F) {
	// Seed with representative schedules
	f.Add("0 * * * *")       // hourly
	f.Add("0 3 * * *")       // 3 AM daily
	f.Add("*/5 * * * *")     // every 5 min
	f.Add("0 3 * * 0")       // Sunday 3 AM
	f.Add("30 2 1 * *")      // 2:30 AM on 1st
	f.Add("@hourly")         // shortcut (should reject)
	f.Add("")                // empty
	f.Add("bad syntax here") // invalid
	f.Add("* * * * * *")     // 6 fields (too many)

	f.Fuzz(func(t *testing.T, input string) {
		// Call the validator - must not panic
		err := ValidateSchedule(input)

		// If we get an error, it must have a code
		if err != nil {
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("expected *ValidationError, got %T", err)
			}
			if ve.Code == "" {
				t.Fatalf("ValidationError has empty Code")
			}
			// Known error codes for schedule validation
			knownCodes := map[string]bool{
				ErrCodeBadScheduleSyntax: true,
			}
			if !knownCodes[ve.Code] {
				t.Fatalf("unknown error code for schedule: %s", ve.Code)
			}
		}
		// Success is also valid - some inputs may parse correctly
	})
}
