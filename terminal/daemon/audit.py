"""Audit logging with HMAC sealing (SEC-REV-4, SEC-REV-10)."""

import hashlib
import hmac
import os
import time
from datetime import datetime
from pathlib import Path


class AuditSession:
    """
    Per-session audit transcript with HMAC sealing.

    Line-buffered I/O (1s or 4KB flush window). On close, computes
    HMAC-SHA256(log_content || metadata, audit_hmac_secret) and writes
    <session>.log.sig next to the log.

    Format:
    - Opening line: `# Session start: <iso-ts>, admin=<user>, ip=<ip>, session=<id>`
    - Framed interleaved stdin/stdout as written/read
    - Any warning events: `# WARNING: idle timeout at <iso-ts>` (SEC-REV-3)
    - Closing line: `# Session end: <iso-ts>, exit_code=<N>`
    """

    def __init__(
        self,
        log_dir: str,
        admin_id: int,
        admin_name: str,
        ip: str,
        session_id: str,
        audit_hmac_secret: str,
    ):
        """Initialize audit session."""
        self.log_dir = log_dir
        self.admin_id = admin_id
        self.admin_name = admin_name
        self.ip = ip
        self.session_id = session_id
        self.audit_hmac_secret = audit_hmac_secret

        # Ensure log directory exists with 0700 permissions
        os.makedirs(log_dir, mode=0o700, exist_ok=True)

        # Log file: /var/log/jabali-terminal/sessions/<iso-date>_<admin>_<session>.log
        iso_date = datetime.utcnow().strftime("%Y-%m-%d")
        self.log_path = os.path.join(
            log_dir,
            f"{iso_date}_{admin_name}_{session_id}.log",
        )

        self.sig_path = f"{self.log_path}.sig"

        # Line buffer and flush timer
        self._buffer = []
        self._buffer_size = 0
        self._last_flush = time.time()
        self._flush_interval = 1.0  # 1s
        self._flush_threshold = 4096  # 4KB

        self._file = None
        self._closed = False

        # Create file with 0600 permissions
        self._file = open(self.log_path, "a", buffering=1)  # line-buffered
        os.chmod(self.log_path, 0o600)

        # Write opening line
        iso_ts = datetime.utcnow().isoformat() + "Z"
        opening = f"# Session start: {iso_ts}, admin={admin_name}, ip={ip}, session={session_id}\n"
        self._file.write(opening)
        self._file.flush()

    def write_stdin(self, data: bytes) -> None:
        """Log shell stdin."""
        if self._closed:
            raise RuntimeError("AuditSession is closed")

        # Escape unprintable characters for readability
        try:
            text = data.decode("utf-8", errors="replace")
        except Exception:
            text = repr(data)

        line = f"[STDIN] {text}\n"
        self._file.write(line)
        self._buffer_size += len(line)

        if self._should_flush():
            self._file.flush()
            self._buffer_size = 0
            self._last_flush = time.time()

    def write_stdout(self, data: bytes) -> None:
        """Log shell stdout."""
        if self._closed:
            raise RuntimeError("AuditSession is closed")

        try:
            text = data.decode("utf-8", errors="replace")
        except Exception:
            text = repr(data)

        line = f"[STDOUT] {text}\n"
        self._file.write(line)
        self._buffer_size += len(line)

        if self._should_flush():
            self._file.flush()
            self._buffer_size = 0
            self._last_flush = time.time()

    def write_warning(self, event: str) -> None:
        """Log a warning event (e.g., idle timeout)."""
        if self._closed:
            raise RuntimeError("AuditSession is closed")

        iso_ts = datetime.utcnow().isoformat() + "Z"
        line = f"# WARNING: {event} at {iso_ts}\n"
        self._file.write(line)
        self._file.flush()

    def close(self, exit_code: int = 0) -> None:
        """
        Close audit session and seal with HMAC.

        Appends closing line, computes HMAC-SHA256, writes .sig file.
        """
        if self._closed:
            return

        # Write closing line
        iso_ts = datetime.utcnow().isoformat() + "Z"
        closing = f"# Session end: {iso_ts}, exit_code={exit_code}\n"
        self._file.write(closing)
        self._file.close()

        # Read entire log content
        with open(self.log_path, "rb") as f:
            log_content = f.read()

        # Compute HMAC over log content + metadata
        # Metadata: admin_id, ip, session_id
        metadata = f"{self.admin_id}|{self.ip}|{self.session_id}".encode()
        message = log_content + metadata

        secret_bytes = bytes.fromhex(self.audit_hmac_secret)
        h = hmac.new(secret_bytes, message, hashlib.sha256)
        hmac_hex = h.hexdigest()

        # Write signature file
        with open(self.sig_path, "w") as f:
            f.write(hmac_hex)

        os.chmod(self.sig_path, 0o600)

        self._closed = True

    def _should_flush(self) -> bool:
        """Check if buffer should be flushed (1s or 4KB threshold)."""
        now = time.time()
        time_elapsed = now - self._last_flush
        return time_elapsed >= self._flush_interval or self._buffer_size >= self._flush_threshold


async def scan_unclosed_logs(
    log_dir: str,
    audit_hmac_secret: str,
) -> None:
    """
    Scan log directory for unsealed logs on daemon startup (SEC-REV-10).

    If a log is missing its .sig file (unsealed), append:
    `# Session interrupted: daemon restart at <iso-ts>`
    and seal it with HMAC.

    This prevents a crash-loop from leaving sessions in unsealed state.
    """
    log_path = Path(log_dir)
    if not log_path.exists():
        return

    for log_file in log_path.glob("*.log"):
        sig_file = Path(f"{log_file}.sig")

        if not sig_file.exists():
            # Log is missing signature — it was interrupted
            iso_ts = datetime.utcnow().isoformat() + "Z"
            interrupt_line = f"# Session interrupted: daemon restart at {iso_ts}\n"

            # Append interrupt line
            with open(log_file, "a") as f:
                f.write(interrupt_line)

            # Read content and compute HMAC
            with open(log_file, "rb") as f:
                log_content = f.read()

            # Extract metadata from opening line
            # Format: `# Session start: <iso-ts>, admin=<user>, ip=<ip>, session=<id>`
            with open(log_file) as f:
                first_line = f.readline()

            try:
                # Parse: admin=<user>, ip=<ip>, session=<id>
                # admin_name is in the log's first line and therefore already
                # covered by the HMAC (it's part of log_content), so we don't
                # extract it separately — the metadata trailer only binds the
                # fields the normal seal path binds: admin_id | ip | session_id.
                parts = first_line.split(", ")
                ip = parts[2].split("=")[1]
                session_id = parts[3].split("=")[1].strip()

                # The normal seal path uses the real admin_id (an integer).
                # During recovery the ID isn't in the log header, so we use 0
                # as a placeholder. Verifiers of recovered logs must be aware
                # that admin_id=0 was substituted; the `# Session interrupted:`
                # line appended just above is the breadcrumb.
                metadata = f"0|{ip}|{session_id}".encode()
                message = log_content + metadata

                secret_bytes = bytes.fromhex(audit_hmac_secret)
                h = hmac.new(secret_bytes, message, hashlib.sha256)
                hmac_hex = h.hexdigest()

                # Write signature file
                with open(sig_file, "w") as f:
                    f.write(hmac_hex)

                os.chmod(sig_file, 0o600)
            except Exception:
                # If parsing fails, just write an empty signature to mark it sealed
                # (ops can investigate the log manually)
                with open(sig_file, "w") as f:
                    f.write("")
                os.chmod(sig_file, 0o600)
