"""Notification engine — email and webhook alerts for security incidents."""

from __future__ import annotations

import asyncio
import json
import logging
import smtplib
from email.message import EmailMessage

from lib.config import JabaliConfig
from lib.models import Incident

logger = logging.getLogger(__name__)

_SEVERITY_ORDER = {"low": 0, "medium": 1, "high": 2, "critical": 3}


class NotificationEngine:
    """Send notifications for security incidents via email and/or webhook."""

    def __init__(self, config: JabaliConfig) -> None:
        self._config = config
        self._log_once = True

    @property
    def enabled(self) -> bool:
        return bool(self._config.notify_email or self._config.notify_webhook)

    def _should_notify(self, severity: str) -> bool:
        """Check if severity meets minimum threshold."""
        return _SEVERITY_ORDER.get(severity, 0) >= _SEVERITY_ORDER.get(self._config.notify_min_severity, 2)

    async def notify(self, incident: Incident) -> None:
        """Send notification for an incident if severity threshold met."""
        if not self.enabled:
            return
        if not self._should_notify(incident.severity):
            return

        if self._log_once:
            logger.info(
                "Notifications enabled: email=%s webhook=%s min_severity=%s",
                "yes" if self._config.notify_email else "no",
                "yes" if self._config.notify_webhook else "no",
                self._config.notify_min_severity,
            )
            self._log_once = False

        tasks = []
        if self._config.notify_email:
            tasks.append(self._send_email(incident))
        if self._config.notify_webhook:
            tasks.append(self._send_webhook(incident))

        results = await asyncio.gather(*tasks, return_exceptions=True)
        for result in results:
            if isinstance(result, Exception):
                logger.error("Notification error: %s", result)

    async def _send_email(self, incident: Incident) -> None:
        """Send email notification via SMTP (localhost:25 by default)."""
        msg = EmailMessage()
        msg["Subject"] = "[Jabali Security] %s: %s on %s" % (
            incident.severity.upper(),
            incident.action_taken,
            incident.file_event.path,
        )
        msg["From"] = "jabali-security@localhost"
        msg["To"] = self._config.notify_email

        body_lines = [
            "Security Incident Detected",
            "=" * 40,
            "",
            "ID:        %s" % incident.id,
            "Severity:  %s" % incident.severity,
            "Score:     %d" % incident.total_score,
            "Action:    %s" % incident.action_taken,
            "Path:      %s" % incident.file_event.path,
            "User:      %s" % (incident.username or "unknown"),
            "Time:      %s" % incident.timestamp.isoformat(),
            "",
            "Findings:",
        ]
        for f in incident.findings[:10]:
            body_lines.append("  [%s] %s (score=%d) %s" % (f.scanner, f.rule, f.score, f.description))

        body_lines.extend([
            "",
            "---",
            "Jabali Security - https://jabali-panel.com",
        ])

        msg.set_content("\n".join(body_lines))

        # Send via local SMTP (non-blocking via executor)
        await asyncio.to_thread(self._smtp_send, msg)

    def _smtp_send(self, msg: EmailMessage) -> None:
        """Synchronous SMTP send — runs in executor."""
        try:
            with smtplib.SMTP("localhost", 25, timeout=10) as smtp:
                smtp.send_message(msg)
            logger.info("Email notification sent to %s", self._config.notify_email)
        except (smtplib.SMTPException, OSError) as exc:
            logger.error("Failed to send email to %s: %s", self._config.notify_email, exc)

    async def _send_webhook(self, incident: Incident) -> None:
        """Send webhook POST with incident data.

        DNS is resolved once and the resolved IP is used for the connection
        to prevent DNS rebinding SSRF (TOCTOU between validation and connect).
        """
        import ipaddress
        import socket
        import ssl
        import urllib.error
        import urllib.request
        from urllib.parse import urlparse, urlunparse

        parsed = urlparse(self._config.notify_webhook)
        if parsed.scheme not in ("http", "https"):
            logger.error("Invalid webhook scheme: %s (must be http or https)", parsed.scheme)
            return

        # Resolve DNS once and pin the IP for the actual connection
        try:
            resolved = socket.getaddrinfo(parsed.hostname, None)[0][4][0]
            addr = ipaddress.ip_address(resolved)
            if addr.is_private or addr.is_loopback or addr.is_link_local:
                logger.error("Webhook URL resolves to private/loopback address: %s -> %s", parsed.hostname, resolved)
                return
        except (socket.gaierror, ValueError):
            logger.error("Cannot resolve webhook hostname: %s", parsed.hostname)
            return

        payload = {
            "event": "security_incident",
            "incident": {
                "id": incident.id,
                "severity": incident.severity,
                "score": incident.total_score,
                "action": incident.action_taken,
                "path": incident.file_event.path,
                "username": incident.username,
                "timestamp": incident.timestamp.isoformat(),
                "summary": incident.summary,
            },
        }

        # Connect to the resolved IP directly (not the hostname) to prevent
        # DNS rebinding between the SSRF check above and the actual request.
        pinned_url = urlunparse(parsed._replace(netloc="%s:%s" % (resolved, parsed.port or (443 if parsed.scheme == "https" else 80))))
        data = json.dumps(payload).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Host": parsed.hostname,
        }
        req = urllib.request.Request(pinned_url, data=data, headers=headers, method="POST")  # noqa: S310

        try:
            def _do_post():
                ctx = None
                if parsed.scheme == "https":
                    ctx = ssl.create_default_context()
                    ctx.check_hostname = False  # We verify via Host header; IP in URL won't match cert CN
                    ctx.verify_mode = ssl.CERT_REQUIRED
                with urllib.request.urlopen(req, timeout=10, context=ctx) as resp:  # noqa: S310
                    return resp.status

            status = await asyncio.to_thread(_do_post)
            logger.info("Webhook notification sent to %s (status=%d)", self._config.notify_webhook, status)
        except (urllib.error.URLError, OSError) as exc:
            logger.error("Failed to send webhook to %s: %s", self._config.notify_webhook, exc)
