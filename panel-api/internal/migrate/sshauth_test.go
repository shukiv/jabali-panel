package migrate

import "testing"

// TestSSHPasswordAuthMethods_OffersBoth guards GH #429: a plaintext password
// must be tried via BOTH "password" and "keyboard-interactive", so a PAM
// source that only advertises keyboard-interactive still authenticates.
func TestSSHPasswordAuthMethods_OffersBoth(t *testing.T) {
	methods := SSHPasswordAuthMethods("hunter2")
	if len(methods) != 2 {
		t.Fatalf("want 2 auth methods (password + keyboard-interactive), got %d", len(methods))
	}
}

// TestPasswordChallenge_AnswersEveryPromptWithPassword covers the single- and
// multi-prompt PAM flows.
func TestPasswordChallenge_AnswersEveryPromptWithPassword(t *testing.T) {
	ch := passwordChallenge("s3cret")
	ans, err := ch("", "", []string{"Password: "}, []bool{false})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 1 || ans[0] != "s3cret" {
		t.Fatalf("single prompt: got %v, want [s3cret]", ans)
	}
	ans, err = ch("", "", []string{"Password: ", "Verification code: "}, []bool{false, true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 2 || ans[0] != "s3cret" || ans[1] != "s3cret" {
		t.Fatalf("multi prompt: got %v", ans)
	}

	// Zero prompts (server sends an empty round) must yield an empty,
	// non-nil slice — never a panic.
	if ans, err := ch("", "", nil, nil); err != nil || len(ans) != 0 {
		t.Fatalf("zero prompts: got %v, err %v", ans, err)
	}
}
