package migrate

import "golang.org/x/crypto/ssh"

// SSHPasswordAuthMethods returns the SSH auth methods to try for a plaintext
// password. It offers BOTH the "password" method and "keyboard-interactive".
//
// GH #429: many servers do password login through PAM and advertise ONLY the
// keyboard-interactive method, not "password". With ssh.Password alone the
// handshake fails with `no supported methods remain [none password]` even
// though `ssh host` logs in fine with the same password (lxsdevcode's Plesk
// source hit exactly this). The keyboard-interactive callback answers every
// challenge prompt with the password, which covers the common single-prompt
// ("Password:") PAM flow while leaving genuine multi-factor prompts to fail.
func SSHPasswordAuthMethods(password string) []ssh.AuthMethod {
	return []ssh.AuthMethod{ssh.Password(password), ssh.KeyboardInteractive(passwordChallenge(password))}
}

// passwordChallenge answers every keyboard-interactive prompt with the given
// password. Extracted for testability.
func passwordChallenge(password string) ssh.KeyboardInteractiveChallenge {
	return func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			answers[i] = password
		}
		return answers, nil
	}
}
