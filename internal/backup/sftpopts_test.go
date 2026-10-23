package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComposeSFTPURL(t *testing.T) {
	require.Equal(t, "sftp:bob@host:/repo", ComposeSFTPURL(SFTPInputs{
		Host: "host", User: "bob", Path: "/repo",
	}))
	require.Equal(t, "sftp:bob@host:", ComposeSFTPURL(SFTPInputs{
		Host: "host", User: "bob",
	}))
	require.Equal(t, "", ComposeSFTPURL(SFTPInputs{Host: "host"}))
}

// GH #2245: with default ssh config the command is still built, so ssh gets
// the connect and keepalive bounds, but nothing else changes: no host-key
// policy and no auth options beyond what the system ssh config says.
func TestSFTPCommandFlag_DefaultsOnlyAddTheTimeouts(t *testing.T) {
	got := SFTPCommandFlag(SFTPInputs{Host: "h", User: "u", Path: "/r"})
	require.Equal(t,
		"sftp.command=ssh -o ConnectTimeout=30 -o ServerAliveInterval=15 -o ServerAliveCountMax=4 -l u -s -- h sftp", got)
}

// GH #2245: every auth mode bounds the connect and notices a dead
// connection, so a destination that is down fails the backup in seconds
// instead of holding it.
func TestSFTPCommandFlag_EveryModeBoundsTheConnection(t *testing.T) {
	for _, in := range []SFTPInputs{
		{Host: "h", User: "u", Path: "/r"},
		{Host: "h", User: "u", Path: "/r", Auth: "key"},
		{Host: "h", User: "u", Path: "/r", Auth: "key", KeyPath: "/root/.ssh/id_ed25519"},
		{Host: "h", User: "u", Path: "/r", Auth: "password"},
		{Host: "h", User: "u", Path: "/r", Port: 2222},
	} {
		got := SFTPCommandFlag(in)
		for _, opt := range []string{"-o ConnectTimeout=30", "-o ServerAliveInterval=15", "-o ServerAliveCountMax=4"} {
			require.Contains(t, got, opt, "%+v", in)
		}
		require.NotContains(t, got, ",", "go-shellwords splits on commas: %+v", in)
	}
}

func TestSFTPCommandFlag_NoHostNoCommand(t *testing.T) {
	require.Empty(t, SFTPCommandFlag(SFTPInputs{User: "u", Path: "/r"}))
}

func TestSFTPCommandFlag_KeyPath(t *testing.T) {
	got := SFTPCommandFlag(SFTPInputs{
		Host: "h", User: "u", Path: "/r", Auth: "key",
		KeyPath: "/root/.ssh/id_rsa",
	})
	require.Contains(t, got, "sftp.command=ssh -i /root/.ssh/id_rsa")
	require.True(t, strings.HasSuffix(got, " -l u -s -- h sftp"), got)
}

func TestSFTPCommandFlag_Password(t *testing.T) {
	got := SFTPCommandFlag(SFTPInputs{
		Host: "h", User: "u", Path: "/r", Auth: "password",
	})
	require.True(t, strings.HasPrefix(got, "sftp.command=sshpass -e ssh"))
	require.True(t, strings.HasSuffix(got, " -l u -s -- h sftp"), got)
	// Password auth must NOT set BatchMode=yes — that would block the
	// interactive prompt sshpass needs.
	require.NotContains(t, got, "BatchMode=yes")
	require.Contains(t, got, "PreferredAuthentications=password")
	require.NotContains(t, got, "keyboard-interactive") // commas break go-shellwords tokenization
	require.Contains(t, got, "PubkeyAuthentication=no")
}

func TestSFTPCommandFlag_KeyAuthSetsBatchMode(t *testing.T) {
	got := SFTPCommandFlag(SFTPInputs{
		Host: "h", User: "u", Path: "/r", Auth: "key",
		KeyPath: "/root/.ssh/id_rsa",
	})
	require.Contains(t, got, "BatchMode=yes")
}

func TestSFTPCommandFlag_NonStandardPort(t *testing.T) {
	got := SFTPCommandFlag(SFTPInputs{
		Host: "h", User: "u", Path: "/r", Auth: "key",
		KeyPath: "/root/.ssh/id_ed25519", Port: 2222,
	})
	require.Contains(t, got, "-p 2222")
	require.Contains(t, got, "-i /root/.ssh/id_ed25519")
}

// A user or host that starts with `-` must never reach ssh where it would be
// read as an ssh option (`-oProxyCommand=...` runs through a shell): the user
// is -l's argument and the host comes after `--`.
func TestSFTPCommandFlag_DashLedUserAndHostAreNotOptions(t *testing.T) {
	for _, in := range []SFTPInputs{
		{Host: "-oProxyCommand=host-cmd", User: "-oProxyCommand=user-cmd"},
		{Host: "-oProxyCommand=host-cmd", User: "-oProxyCommand=user-cmd", Auth: "password"},
		{Host: "-oProxyCommand=host-cmd", User: "-oProxyCommand=user-cmd", Auth: "key", KeyPath: "/k", Port: 2222},
	} {
		argv := strings.Fields(strings.TrimPrefix(SFTPCommandFlag(in), "sftp.command="))
		dashdash := -1
		for i, a := range argv {
			if a == "--" {
				dashdash = i
				break
			}
		}
		require.GreaterOrEqual(t, dashdash, 0, "no -- in %q", argv)
		require.Equal(t, []string{"-oProxyCommand=host-cmd", "sftp"}, argv[dashdash+1:], "%+v", in)
		for i, a := range argv[:dashdash] {
			if a == in.User {
				require.Equal(t, "-l", argv[i-1], "user must only appear as -l's argument: %q", argv)
			}
			require.NotEqual(t, in.Host, a, "host before --: %q", argv)
		}
	}
}
