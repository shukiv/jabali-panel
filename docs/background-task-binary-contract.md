# Background Task Binary Contract

This document specifies the stable contract that any binary invoked by
`agent.job.start` must implement. It is the integration point for addons
(jabali-backup, future tools) that want to appear in the panel's unified
Background Tasks dashboard.

See [ADR-0007](adr/0007-agent-v2-control-plane.md) for the architectural
context.

## Required behavior

### Arguments

The binary MUST accept the following arguments, which the agent injects
automatically when spawning:

```
--task-id=<uuid>            Background task UUID. Echo back in log events.
--progress-log=<path>       Absolute path to the JSONL progress log file.
                            Pre-created by the agent with mode 0640.
```

Binaries MAY accept additional implementation-specific arguments.

### Exit codes

- `0` — task succeeded.
- any non-zero — task failed.

### Signals

- `SIGTERM` — graceful shutdown. The binary SHOULD finish any critical
  in-flight work (e.g. flush a write to avoid corruption), emit a
  `canceled` event, and exit within 10 seconds.
- `SIGKILL` — forced termination. The binary cannot observe this.

### Progress log events (JSONL)

Each line in `--progress-log` MUST be a single JSON object with at
minimum a `ts` (UTC ISO 8601) and `event` field. Valid events:

| Event     | Required fields  | Optional fields                  | Terminal |
|-----------|------------------|----------------------------------|----------|
| `started` | ts, event        |                                  | no       |
| `progress`| ts, event        | `percent` (0..100), `step`       | no       |
| `step`    | ts, event, step  |                                  | no       |
| `done`    | ts, event        | `data` (object)                  | yes      |
| `failed`  | ts, event        | `error`, `step`                  | yes      |
| `canceled`| ts, event        | `error`                          | yes      |

Example stream for a backup run:

```json
{"ts":"2026-04-13T01:00:00Z","event":"started"}
{"ts":"2026-04-13T01:00:02Z","event":"step","step":"snapshotting database"}
{"ts":"2026-04-13T01:02:11Z","event":"progress","percent":30,"step":"archiving"}
{"ts":"2026-04-13T01:08:42Z","event":"progress","percent":85,"step":"uploading"}
{"ts":"2026-04-13T01:10:00Z","event":"done","data":{"bytes":1048576000,"files":842}}
```

On failure:

```json
{"ts":"2026-04-13T01:00:00Z","event":"started"}
{"ts":"2026-04-13T01:02:11Z","event":"progress","percent":20,"step":"uploading"}
{"ts":"2026-04-13T01:02:30Z","event":"failed","error":"rsync: connection lost","step":"uploading"}
```

Writes MUST be line-oriented; the agent tails the file and parses
line-by-line. Partial lines will be skipped (not an error condition).

## What binaries MUST NOT do

- **Do not POST to the panel HTTP API.** The agent tails the log and
  relays events via the internal-api endpoint. Direct HTTP would require
  authentication schemes and couples the binary to panel internals.
- **Do not exec itself under a different uid.** systemd-run already
  runs the binary as root in a transient unit with cgroup limits.
- **Do not create additional files outside `--progress-log` without
  cleaning them up.**
- **Do not rely on network connectivity to the panel** to report status —
  the only supported path is writing JSONL to the log file.

## What the panel guarantees

- The log file exists and is writable before the binary starts.
- Arguments are absolute paths / well-formed values (no shell
  interpretation).
- The binary is spawned as root in a cgroup-limited systemd unit. The
  limits (CPUQuota, MemoryMax, IOWeight) are set by the panel based on
  task type.
- On `SIGTERM`, the binary has at least 10 seconds before `SIGKILL`.
- Log events are replayed to the panel even if they arrive out of order
  or after the binary exits — the 60-second reconciler catches drift.

## Reference implementation (PHP)

For in-tree binaries, use `App\Services\BackgroundTasks\Binaries\TaskLogWriter`:

```php
use App\Services\BackgroundTasks\Binaries\TaskLogWriter;

$writer = new TaskLogWriter($this->option('progress-log'));
$writer->started();
$writer->step('compressing');
$writer->progress(percent: 42, step: 'uploading');
$writer->done(['bytes' => 1048576000]);
// or
$writer->failed('rsync: connection lost', step: 'uploading');
```

## Reference implementation (bash)

For external tools written in bash:

```bash
PROGRESS_LOG=""
TASK_ID=""

# Parse args
while [[ $# -gt 0 ]]; do
    case "$1" in
        --task-id=*)      TASK_ID="${1#*=}"; shift ;;
        --progress-log=*) PROGRESS_LOG="${1#*=}"; shift ;;
        *) shift ;;
    esac
done

emit() {
    local event="$1"
    shift
    local extra="$*"
    local ts
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    if [[ -n "$extra" ]]; then
        echo "{\"ts\":\"${ts}\",\"event\":\"${event}\",${extra}}" >> "$PROGRESS_LOG"
    else
        echo "{\"ts\":\"${ts}\",\"event\":\"${event}\"}" >> "$PROGRESS_LOG"
    fi
}

# Graceful SIGTERM handler
trap 'emit canceled "\"error\":\"sigterm received\""; exit 143' TERM

emit started
emit step "\"step\":\"snapshotting\""

# ... do work ...

emit progress "\"percent\":42,\"step\":\"uploading\""

# ... more work ...

emit done "\"data\":{\"bytes\":$BYTES}"
```

## Testing the contract

Binaries can be verified with a mock agent:

```bash
# Simulate the agent spawn
/opt/jabali-backup/bin/jabali-backup \
    --task-id=11111111-2222-3333-4444-555555555555 \
    --progress-log=/tmp/test-task.log \
    --whatever-your-binary-needs

# Tail the log to verify contract compliance
tail -f /tmp/test-task.log | jq .
```

Every line in `/tmp/test-task.log` must parse as JSON and include `ts`
plus `event`.
