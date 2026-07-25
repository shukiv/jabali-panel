package ssrfguard

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// IP literals short-circuit the resolver (no network), so these run offline.

func TestValidateHost_BlocksDangerousRanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // IPv6 loopback
		"169.254.169.254", // link-local / cloud metadata
		"0.0.0.0",         // unspecified
	}
	for _, ip := range blocked {
		_, err := ValidateHost(ctx, ip, false)
		require.Error(t, err, "expected %s to be blocked", ip)
		require.Contains(t, err.Error(), "ssrf")
	}
}

func TestValidateHost_PrivateGatedByAllowFlag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, ip := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1"} {
		_, err := ValidateHost(ctx, ip, false)
		require.Error(t, err, "%s must be blocked when allowPrivate=false", ip)

		_, err = ValidateHost(ctx, ip, true)
		require.NoError(t, err, "%s must be allowed when allowPrivate=true", ip)
	}
}

func TestValidateHost_AllowsPublic(t *testing.T) {
	t.Parallel()
	_, err := ValidateHost(context.Background(), "8.8.8.8", false)
	require.NoError(t, err)
}

func TestValidateHost_EmptyHost(t *testing.T) {
	t.Parallel()
	_, err := ValidateHost(context.Background(), "", false)
	require.Error(t, err)
}

func TestGuardedDialContext_RejectsLoopbackBeforeConnect(t *testing.T) {
	t.Parallel()
	// Nothing is listening; a successful guard rejects at classification time,
	// well before any TCP attempt, so the error is the ssrf rejection.
	_, err := GuardedDialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "ssrf"), "want ssrf rejection, got %v", err)
}

func TestGuardedDialContext_RejectsMetadataIP(t *testing.T) {
	t.Parallel()
	_, err := GuardedDialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssrf")
}
