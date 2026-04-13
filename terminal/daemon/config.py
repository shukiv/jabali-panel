"""Configuration loader for jabali-terminal daemon."""

import os
import re

from pydantic import BaseModel, Field, field_validator


class TerminalConfig(BaseModel):
    """Jabali Terminal daemon configuration."""

    socket_path: str = Field(
        default="/run/jabali-terminal/jabali-terminal.sock",
        description="Unix socket the daemon listens on",
    )
    audit_log_dir: str = Field(
        default="/var/log/jabali-terminal/sessions",
        description="Directory for per-session audit transcripts",
    )
    nonce_db_path: str = Field(
        default="/var/lib/jabali-terminal/nonces.db",
        description="SQLite persistent nonce store (SEC-REV-1)",
    )
    session_idle_seconds: int = Field(
        default=300,
        description="Idle timeout: fires when stdin AND stdout silent (SEC-REV-3)",
    )
    session_hard_seconds: int = Field(
        default=3600,
        description="Hard timeout: force-close after this long (SEC-REV-9)",
    )
    max_concurrent_sessions: int = Field(
        default=4,
        description="Max concurrent sessions across all admins",
    )
    allowed_ips: str = Field(
        default="",
        description="Comma-separated IP allow-list for panel client (empty = allow any)",
    )
    shell: str = Field(
        default="/bin/bash",
        description="Shell to exec under PTY (must be interactive login shell)",
    )
    hmac_secret: str = Field(
        default="",
        description="HMAC secret for session tokens (≥32 bytes hex = 64 chars)",
    )
    audit_hmac_secret: str = Field(
        default="",
        description="HMAC secret for sealing audit logs (≥32 bytes hex = 64 chars)",
    )

    @field_validator("hmac_secret", "audit_hmac_secret")
    @classmethod
    def validate_hmac_secrets(cls, v: str) -> str:
        """Validate HMAC secrets are ≥32 bytes (64 hex chars) and valid hex."""
        if not v:
            raise ValueError("HMAC secret cannot be empty")
        if len(v) < 64:
            raise ValueError(
                f"HMAC secret must be ≥64 hex chars (32 bytes); got {len(v)}"
            )
        if not re.match(r"^[0-9a-fA-F]+$", v):
            raise ValueError("HMAC secret must be valid hex characters only")
        return v

    @field_validator("shell")
    @classmethod
    def validate_shell_exists(cls, v: str) -> str:
        """Validate the shell executable exists."""
        if not os.path.exists(v):
            raise ValueError(f"Shell does not exist: {v}")
        if not os.access(v, os.X_OK):
            raise ValueError(f"Shell is not executable: {v}")
        return v

    @field_validator("session_idle_seconds", "session_hard_seconds")
    @classmethod
    def validate_timeouts_positive(cls, v: int) -> int:
        """Validate timeouts are positive."""
        if v <= 0:
            raise ValueError("Timeout must be > 0")
        return v

    @field_validator("max_concurrent_sessions")
    @classmethod
    def validate_max_sessions_positive(cls, v: int) -> int:
        """Validate max_concurrent_sessions is positive."""
        if v <= 0:
            raise ValueError("max_concurrent_sessions must be > 0")
        return v

    def get_allowed_ips(self) -> list[str]:
        """Parse comma-separated allowed IPs."""
        if not self.allowed_ips:
            return []
        return [ip.strip() for ip in self.allowed_ips.split(",") if ip.strip()]


def load_config(path: str = "/etc/jabali-terminal/jabali-terminal.conf") -> TerminalConfig:
    """Load configuration from shell-style KEY="value" file."""
    config_dict = {}

    if not os.path.exists(path):
        raise FileNotFoundError(f"Configuration file not found: {path}")

    with open(path) as f:
        for line_num, line in enumerate(f, start=1):
            line = line.strip()

            # Skip empty lines and comments
            if not line or line.startswith("#"):
                continue

            # Parse KEY="value" or KEY=value (shell-style). Unquoted values
            # are accepted for integers and simple tokens so the example
            # config can keep session_idle_seconds=300 readable, matching
            # the sibling jabali-security convention.
            match = re.match(r'^(\w+)=(?:"([^"]*)"|(\S+))\s*$', line)
            if not match:
                raise ValueError(
                    f"Invalid config line {line_num}: expected KEY=value, got: {line}"
                )

            key = match.group(1)
            value = match.group(2) if match.group(2) is not None else match.group(3)
            config_dict[key] = value

    return TerminalConfig(**config_dict)
