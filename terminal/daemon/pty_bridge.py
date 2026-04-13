"""PTY spawning and WebSocket ↔ PTY bidirectional proxy."""

import asyncio
import json
import os
import pty
import signal
import time
from collections.abc import Callable

from .audit import AuditSession


class PTYError(Exception):
    """PTY operation error."""

    pass


async def run_pty_session(
    shell: str,
    ws_send: Callable,
    ws_recv: Callable,
    audit_session: AuditSession,
    admin_name: str,
    session_id: str,
    idle_timeout_seconds: int,
    hard_timeout_seconds: int,
) -> int:
    """
    Spawn shell under PTY and proxy WebSocket ↔ PTY bidirectionally.

    Args:
        shell: Path to shell (e.g., /bin/bash)
        ws_send: Async callable(message: str | bytes) to send to WebSocket
        ws_recv: Async callable() -> str | bytes to receive from WebSocket
        audit_session: AuditSession for logging
        admin_name: Admin username for logging
        session_id: Session ID for logging
        idle_timeout_seconds: Close if stdin+stdout silent (SEC-REV-3)
        hard_timeout_seconds: Force close after this long (SEC-REV-9)

    Returns: PTY exit code

    Proxying logic:
    - Client sends BINARY frames for PTY stdin (raw UTF-8 bytes).
    - Client sends TEXT (JSON) frames for control: {type: "resize", cols, rows}.
    - Daemon sends PTY output as BINARY frames; control messages as TEXT.
    - ws_recv() returns:
        * bytes   -> stdin
        * str     -> JSON control frame
        * None    -> peer closed; end the session
    - Idle timeout: both stdin AND stdout silent (SEC-REV-3).
    - Hard timeout: force close regardless of activity (SEC-REV-9).
    """
    # pty.fork() forks and wires the child's stdio to the PTY slave for us.
    # (pty.openpty() returns two fds, not (pid, fd) — using openpty here was
    # the original bug that made whoami silently hang.)
    pid, master_fd = pty.fork()
    if pid == 0:
        # Child: clean env so the admin always gets a predictable prompt,
        # then exec the shell as an interactive login.
        os.execve(shell, [shell, "-i"], {
            "TERM": "xterm-256color",
            "HOME": os.environ.get("HOME", "/root"),
            "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "LANG": os.environ.get("LANG", "C.UTF-8"),
        })
        os._exit(127)

    # Parent: manage PTY
    try:
        # Make PTY non-blocking
        os.set_blocking(master_fd, False)

        # Track timeouts. The idle check uses last_stdin AND last_stdout
        # independently (only times out when both have been silent past
        # idle_timeout_seconds — see below), so we don't keep a combined
        # last_activity clock.
        session_start = time.time()
        last_stdin = session_start
        last_stdout = session_start

        # Tracks exit code for paths that `break` out of the loop before the
        # child is reaped. The finally block seals the audit log with this
        # code if run_pty_session exits via any route other than the normal
        # "child exited" return (which handles its own seal inline).
        pending_exit_code: int | None = None

        while True:
            # Check hard timeout
            elapsed = time.time() - session_start
            if elapsed > hard_timeout_seconds:
                audit_session.write_warning(f"hard timeout at {time.time()}")
                os.kill(pid, signal.SIGTERM)
                # GNU `timeout` convention: 124 = time limit exceeded.
                pending_exit_code = 124
                break

            # Check idle timeout (both stdin AND stdout silent)
            now = time.time()
            stdin_idle = now - last_stdin
            stdout_idle = now - last_stdout
            if stdin_idle > idle_timeout_seconds and stdout_idle > idle_timeout_seconds:
                audit_session.write_warning(f"idle timeout at {time.time()}")
                os.kill(pid, signal.SIGTERM)
                pending_exit_code = 124
                break

            # Read from PTY (non-blocking, timeout 0.1s)
            try:
                await asyncio.sleep(0.01)  # Yield control
                data = os.read(master_fd, 4096)
                if data:
                    audit_session.write_stdout(data)
                    await ws_send(data)  # Binary frame
                    last_stdout = time.time()
            except (OSError, BlockingIOError):
                pass  # No data available

            # Receive from WebSocket (non-blocking, timeout 0.1s)
            try:
                msg = await asyncio.wait_for(ws_recv(), timeout=0.1)
                if msg is None:
                    # Peer closed — signal the shell and exit the loop.
                    os.kill(pid, signal.SIGHUP)
                    # 129 = 128 + SIGHUP(1), conventional exit code for a
                    # process terminated by the shell hanging up.
                    pending_exit_code = 129
                    break
                if isinstance(msg, (bytes, bytearray)):
                    data = bytes(msg)
                    # Defence-in-depth cap: the UI already caps pastes at 4KB
                    # (SEC-REV-8 client-side), enforce server-side too so a
                    # patched browser cannot flood the PTY.
                    if len(data) > 4096:
                        await ws_send(
                            json.dumps({
                                "type": "error",
                                "message": "stdin frame exceeds 4KB limit",
                            })
                        )
                        continue
                    os.write(master_fd, data)
                    audit_session.write_stdin(data)
                    last_stdin = time.time()
                elif isinstance(msg, str):
                    try:
                        msg_json = json.loads(msg)
                    except json.JSONDecodeError:
                        continue
                    if msg_json.get("type") == "resize":
                        try:
                            cols = int(msg_json.get("cols", 80))
                            rows = int(msg_json.get("rows", 24))
                            # Validate dimensions (PTY size must be positive)
                            if rows < 1 or rows > 999 or cols < 1 or cols > 999:
                                continue  # Reject invalid dimensions
                            _set_pty_size(master_fd, rows, cols)
                        except (TypeError, ValueError):
                            # Malformed cols/rows in resize message — ignore
                            continue

            except TimeoutError:
                pass  # No data available

            # Check if child exited
            wpid, status = os.waitpid(pid, os.WNOHANG)
            if wpid == pid:
                # Child exited; read any remaining PTY data
                try:
                    while True:
                        data = os.read(master_fd, 4096)
                        if not data:
                            break
                        audit_session.write_stdout(data)
                        await ws_send(data)
                except OSError:
                    pass

                # Determine exit code
                if os.WIFEXITED(status):
                    exit_code = os.WEXITSTATUS(status)
                elif os.WIFSIGNALED(status):
                    exit_code = 128 + os.WTERMSIG(status)
                else:
                    exit_code = 1

                audit_session.close(exit_code=exit_code)
                return exit_code

    finally:
        try:
            os.close(master_fd)
        except OSError:
            pass
        try:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        except OSError:
            pass
        # Seal the audit log on every exit path. AuditSession.close() is
        # idempotent (guarded by self._closed), so if the normal child-exit
        # branch already sealed, this is a no-op. For the break paths (hard
        # timeout / idle timeout / peer closed), pending_exit_code carries
        # the reason; for any other unexpected exception, default to 1 so
        # the log doesn't stay orphaned. Without this, a dropped WS tab
        # used to leave an "unsealed" log until the next daemon restart
        # when scan_unclosed_logs() would fix it — now it seals immediately.
        try:
            audit_session.close(exit_code=pending_exit_code if pending_exit_code is not None else 1)
        except Exception:
            # Don't mask the original exception (if any) with a seal error.
            pass

    # Reached only when a break left the loop without returning. Report the
    # reason to the caller; ws_handler's finally then closes the WS.
    return pending_exit_code if pending_exit_code is not None else 1


def _set_pty_size(fd: int, rows: int, cols: int) -> None:
    """Set PTY window size (TIOCSWINSZ)."""
    import fcntl
    import struct
    import termios

    try:
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    except Exception:
        pass  # Ignore any errors (invalid fd, bad types, struct packing, ioctl, etc.)
