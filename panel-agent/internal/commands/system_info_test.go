package commands

import (
	"context"
	"encoding/json"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUptimeData(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		up, err := ParseUptimeData("12345.67 98765.43\n")
		require.NoError(t, err)
		assert.InDelta(t, 12345.67, up, 0.01)
	})

	t.Run("empty", func(t *testing.T) {
		_, err := ParseUptimeData("")
		assert.Error(t, err)
	})
}

func TestParseLoadAvgData(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		avg, err := ParseLoadAvgData("0.15 0.10 0.05 1/234 5678\n")
		require.NoError(t, err)
		assert.InDelta(t, 0.15, avg[0], 0.001)
		assert.InDelta(t, 0.10, avg[1], 0.001)
		assert.InDelta(t, 0.05, avg[2], 0.001)
	})

	t.Run("too few fields", func(t *testing.T) {
		_, err := ParseLoadAvgData("0.15 0.10\n")
		assert.Error(t, err)
	})
}

func TestParseCPUCountData(t *testing.T) {
	t.Parallel()

	content := `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-9750H CPU

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-9750H CPU

processor	: 2
vendor_id	: GenuineIntel
`
	assert.Equal(t, 3, ParseCPUCountData(content))

	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, 1, ParseCPUCountData(""))
	})
}

func TestParseMeminfoData(t *testing.T) {
	t.Parallel()

	content := `MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          4096000 kB
`

	t.Run("normal", func(t *testing.T) {
		total, avail, err := ParseMeminfoData(content)
		require.NoError(t, err)
		assert.Equal(t, uint64(16384000), total)
		assert.Equal(t, uint64(8192000), avail)
	})

	t.Run("missing MemTotal", func(t *testing.T) {
		_, _, err := ParseMeminfoData("MemAvailable: 100 kB\n")
		assert.Error(t, err)
	})

	t.Run("missing MemAvailable", func(t *testing.T) {
		_, _, err := ParseMeminfoData("MemTotal: 100 kB\n")
		assert.Error(t, err)
	})
}

func TestSystemInfoHandler_Dispatch(t *testing.T) {
	t.Parallel()

	// Use the Default registry which has system.info registered via init().
	r := NewRegistry()
	r.Register("system.info", systemInfoHandler)

	data, agentErr := r.Dispatch(context.Background(), "system.info", nil)
	// On a real Linux box this should succeed; on CI it depends on /proc.
	if agentErr != nil {
		t.Skipf("system.info not available (probably no /proc): %s", agentErr.Message)
	}

	var resp SystemInfoResponse
	require.NoError(t, json.Unmarshal(data, &resp))
	assert.NotEmpty(t, resp.Hostname)
	assert.Greater(t, resp.UptimeSeconds, float64(0))
	assert.Greater(t, resp.MemTotalKB, uint64(0))
	assert.GreaterOrEqual(t, resp.MemAvailKB, uint64(0))
	assert.Greater(t, resp.CPUCount, 0)
	assert.NotEmpty(t, resp.Partitions)
}

// --- container memory scoping (LXC /proc/meminfo == host) ----------------

func TestReadCgroupValueKB(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := dir + "/" + name
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}

	t.Run("numeric bytes to KB", func(t *testing.T) {
		v, err := readCgroupValueKB(write("a", "1703936\n")) // 1664 KB
		require.NoError(t, err)
		assert.Equal(t, uint64(1664), v)
	})
	t.Run("max -> 0 (unlimited)", func(t *testing.T) {
		v, err := readCgroupValueKB(write("b", "max\n"))
		require.NoError(t, err)
		assert.Equal(t, uint64(0), v)
	})
	t.Run("missing file errors", func(t *testing.T) {
		_, err := readCgroupValueKB(dir + "/nope")
		assert.Error(t, err)
	})
}

func TestReadCgroupMemStats(t *testing.T) {
	t.Run("finite limit + swap", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/memory.current", []byte("1073741824\n"), 0o644)) // 1 GiB -> 1048576 KB
		require.NoError(t, os.WriteFile(dir+"/memory.max", []byte("2147483648\n"), 0o644))     // 2 GiB -> 2097152 KB
		require.NoError(t, os.WriteFile(dir+"/memory.swap.current", []byte("524288000\n"), 0o644))
		require.NoError(t, os.WriteFile(dir+"/memory.swap.max", []byte("1073741824\n"), 0o644))
		st, ok := readCgroupMemStats(dir)
		require.True(t, ok)
		assert.Equal(t, uint64(1048576), st.currentKB)
		assert.Equal(t, uint64(2097152), st.limitKB)
		assert.True(t, st.hasSwap)
		assert.Equal(t, uint64(512000), st.swapCurrentKB)
		assert.Equal(t, uint64(1048576), st.swapLimitKB)
	})
	t.Run("unlimited (max), no swap controller", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/memory.current", []byte("1703936\n"), 0o644)) // 1664 KB
		require.NoError(t, os.WriteFile(dir+"/memory.max", []byte("max\n"), 0o644))
		st, ok := readCgroupMemStats(dir)
		require.True(t, ok)
		assert.Equal(t, uint64(1664), st.currentKB)
		assert.Equal(t, uint64(0), st.limitKB) // unlimited
		assert.False(t, st.hasSwap)
	})
	t.Run("no memory.current -> not ok", func(t *testing.T) {
		st, ok := readCgroupMemStats(t.TempDir())
		assert.False(t, ok)
		assert.Equal(t, cgroupMemStats{}, st)
	})
}

func TestApplyContainerMemory(t *testing.T) {
	// Host meminfo numbers (the misleading ones): 16 GiB total, 9.2 GiB used,
	// 12 GiB swap total, 6.7 GiB swap used.
	const hostTotal, hostUsed = 16_000_000, 9_200_000
	const hostSwapTotal, hostSwapUsed = 12_000_000, 6_700_000

	t.Run("unlimited cgroup keeps host total, scopes used to container", func(t *testing.T) {
		// 10.0.3.14 case: memory.max == "max" (limitKB 0), current 1.6 GiB.
		mt, mu, swt, swu := applyContainerMemory(
			hostTotal, hostUsed, hostSwapTotal, hostSwapUsed,
			cgroupMemStats{currentKB: 1_600_000, limitKB: 0},
		)
		assert.Equal(t, uint64(16_000_000), mt, "total stays host capacity when unlimited")
		assert.Equal(t, uint64(1_600_000), mu, "used scoped to container current")
		// no swap controller -> swap untouched
		assert.Equal(t, uint64(12_000_000), swt)
		assert.Equal(t, uint64(6_700_000), swu)
	})

	t.Run("finite cgroup limit becomes reported total", func(t *testing.T) {
		mt, mu, swt, swu := applyContainerMemory(
			hostTotal, hostUsed, hostSwapTotal, hostSwapUsed,
			cgroupMemStats{currentKB: 800_000, limitKB: 2_000_000,
				hasSwap: true, swapCurrentKB: 100_000, swapLimitKB: 1_000_000},
		)
		assert.Equal(t, uint64(2_000_000), mt)
		assert.Equal(t, uint64(800_000), mu)
		assert.Equal(t, uint64(1_000_000), swt)
		assert.Equal(t, uint64(100_000), swu)
	})

	t.Run("clamps used to total when current exceeds limit", func(t *testing.T) {
		mt, mu, _, _ := applyContainerMemory(
			hostTotal, hostUsed, 0, 0,
			cgroupMemStats{currentKB: 3_000_000, limitKB: 2_000_000},
		)
		assert.Equal(t, uint64(2_000_000), mt)
		assert.Equal(t, uint64(2_000_000), mu, "used clamped to total")
	})
}

func TestInContainer_PID1Environ(t *testing.T) {
	origProc, origCE, origDE := procRoot, containerEnvPath, dockerEnvPath
	defer func() { procRoot, containerEnvPath, dockerEnvPath = origProc, origCE, origDE }()
	// Point the container markers at non-existent paths so both subtests
	// isolate the PID1-environ logic regardless of whether the test host is
	// itself a container (the CI runner is a Docker container -> /.dockerenv
	// exists; without this the bare-metal case wrongly observes true).
	containerEnvPath = t.TempDir() + "/absent-containerenv"
	dockerEnvPath = t.TempDir() + "/absent-dockerenv"

	t.Run("container=lxc in PID1 environ -> true", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(dir+"/1", 0o755))
		// /proc/1/environ is NUL-separated key=value pairs.
		require.NoError(t, os.WriteFile(dir+"/1/environ",
			[]byte("PATH=/usr/bin\x00container=lxc\x00HOME=/root\x00"), 0o644))
		procRoot = dir
		assert.True(t, inContainer())
	})

	t.Run("bare metal (no container= marker) -> false", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(dir+"/1", 0o755))
		require.NoError(t, os.WriteFile(dir+"/1/environ",
			[]byte("PATH=/usr/bin\x00HOME=/root\x00"), 0o644))
		procRoot = dir
		assert.False(t, inContainer())
	})
}

// GH #2029: Used is what df reports, so the root-reserved blocks of an ext4
// filesystem are not counted as used. The numbers are the reporter's root
// filesystem (df -B1: 760740884480 total, 32386052096 used, 697378873344
// available).
func TestPartitionFromStatfs_ReservedBlocksAreNotUsed(t *testing.T) {
	st := syscall.Statfs_t{Bsize: 4096, Blocks: 185727755, Bfree: 177821004, Bavail: 170258514}

	p := partitionFromStatfs("/", &st)

	assert.Equal(t, PartitionInfo{
		MountPoint: "/",
		TotalBytes: 760740884480,
		UsedBytes:  32386052096,
		FreeBytes:  697378873344,
	}, p)
}
