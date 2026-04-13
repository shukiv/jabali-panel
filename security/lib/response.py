"""Response engine — execute automated actions based on threat scores."""

from __future__ import annotations

import logging

from lib.cleanup.engine import CleanupEngine
from lib.config import JabaliConfig
from lib.incidents import IncidentStore
from lib.models import FileEvent, Incident, ThreatScore
from lib.notify import NotificationEngine
from lib.quarantine import QuarantineManager
from lib.scoring import ScoringEngine

logger = logging.getLogger(__name__)


class ResponseEngine:
    def __init__(
        self,
        config: JabaliConfig,
        quarantine: QuarantineManager,
        incidents: IncidentStore,
        cleanup: CleanupEngine | None = None,
    ) -> None:
        self._config = config
        self._quarantine = quarantine
        self._incidents = incidents
        self._cleanup = cleanup
        self._notify = NotificationEngine(config)

    async def handle(self, event: FileEvent, score: ThreatScore) -> Incident | None:
        """Create incident and execute the appropriate response action."""
        if score.action == "ignore":
            return None

        severity = ScoringEngine.severity_from_score(score.total)

        incident = Incident(
            file_event=event,
            findings=score.findings,
            total_score=score.total,
            severity=severity,
            action_taken=score.action,
            username=event.username,
        )

        # Try cleanup before quarantine if enabled
        if self._cleanup and self._cleanup.enabled and self._cleanup.auto:
            if score.action in ("quarantine", "suspend"):
                try:
                    cleanup_result = await self._cleanup.clean_file(event.path, score.findings)
                    if cleanup_result.success:
                        incident.action_taken = "cleaned"
                        # Save incident with correct action
                        await self._incidents.save(incident)
                        logger.info(
                            "CLEANED [%s] %s (%d changes)",
                            incident.id, event.path, len(cleanup_result.changes_made),
                        )
                        await self._notify.notify(incident)
                        return incident
                except Exception:
                    logger.exception("Cleanup failed for [%s] %s, falling through to quarantine", incident.id, event.path)

        # Save incident to database (with correct action_taken)
        await self._incidents.save(incident)

        # Execute action
        match score.action:
            case "log":
                logger.warning(
                    "INCIDENT [%s] score=%d severity=%s path=%s user=%s",
                    incident.id, score.total, severity, event.path, event.username,
                )
            case "quarantine":
                if self._config.auto_quarantine:
                    record = await self._quarantine.quarantine_file(event.path, incident)
                    if record:
                        await self._incidents.save_quarantine(record)
                        logger.warning(
                            "QUARANTINED [%s] %s -> %s",
                            incident.id, event.path, record.quarantine_path,
                        )
                    else:
                        logger.error("Failed to quarantine [%s] %s", incident.id, event.path)
                else:
                    logger.warning(
                        "WOULD QUARANTINE [%s] %s (auto_quarantine=no)",
                        incident.id, event.path,
                    )
            case "suspend":
                if self._config.auto_quarantine:
                    record = await self._quarantine.quarantine_file(event.path, incident)
                    if record:
                        await self._incidents.save_quarantine(record)
                    else:
                        logger.error("Failed to quarantine during suspend: %s", event.path)
                if self._config.auto_suspend and event.username:
                    logger.critical(
                        "SUSPEND USER [%s] %s (score=%d)",
                        incident.id, event.username, score.total,
                    )
                    # Phase 4+ will implement actual user suspension
                else:
                    logger.critical(
                        "WOULD SUSPEND [%s] user=%s (auto_suspend=no or no username)",
                        incident.id, event.username,
                    )

        # Send notification (if configured and severity meets threshold)
        await self._notify.notify(incident)

        return incident
