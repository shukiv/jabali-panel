"""Async daemon supervisor -- the heart of Jabali Security."""

from __future__ import annotations

import asyncio
import grp
import logging
import os
import signal
import time
from datetime import datetime, timezone
from pathlib import Path

from aiohttp import web

from api.app import create_app
from lib.config import JabaliConfig
from lib.constants import VERSION
from lib.registry import ComponentRegistry

logger = logging.getLogger("jabali-security")


class SecurityDaemon:
    """Main supervisor that coordinates all async tasks."""

    def __init__(self, config: JabaliConfig, disabled: set[str] | None = None) -> None:
        self.config = config
        self._disabled = disabled
        self._start_time: datetime | None = None
        self._registry: ComponentRegistry | None = None

    async def run(self) -> None:
        """Start all subsystems and run until shutdown."""
        self._start_time = datetime.now(timezone.utc)

        self._registry = await ComponentRegistry.build(self.config, disabled=self._disabled)

        async with self._registry:
            app = create_app()
            self._registry.populate_app(app, daemon=self)
            app["start_time"] = time.time()
            api_runner = web.AppRunner(app)
            await api_runner.setup()

            loop = asyncio.get_running_loop()
            stop_event = asyncio.Event()
            for sig in (signal.SIGTERM, signal.SIGINT):
                loop.add_signal_handler(sig, stop_event.set)

            logger.info("Jabali Security %s starting...", VERSION)
            logger.info(
                "Watching dirs: %s | Workers: %d | Engines: %s",
                ", ".join(self.config.watch_dirs),
                self.config.workers,
                ", ".join(self._registry.scanner.scanner_names) or "none",
            )

            # Unix socket (primary)
            socket_path = self.config.api_socket
            if socket_path:
                # Remove stale socket from previous crash
                Path(socket_path).unlink(missing_ok=True)
                unix_site = web.UnixSite(api_runner, socket_path)
                await unix_site.start()
                # Set socket permissions: <owner>:www-data 0660
                os.chmod(socket_path, 0o660)
                try:
                    www_gid = grp.getgrnam("www-data").gr_gid
                    os.chown(socket_path, -1, www_gid)  # keep owner, set group
                except (KeyError, PermissionError):
                    pass  # www-data group may not exist
                logger.info("REST API listening on unix:%s", socket_path)

            # TCP fallback (optional, for debugging)
            if self.config.api_bind:
                tcp_site = web.TCPSite(api_runner, self.config.api_bind, self.config.api_port)
                await tcp_site.start()
                logger.info("REST API also listening on %s:%d (TCP fallback)", self.config.api_bind, self.config.api_port)

            bg_tasks: list[asyncio.Task] = []
            try:
                for coro in self._registry.background_tasks(self):
                    bg_tasks.append(asyncio.create_task(coro))

                await stop_event.wait()
                logger.info("Shutdown signal received...")
            finally:
                for task in bg_tasks:
                    task.cancel()
                results = await asyncio.gather(*bg_tasks, return_exceptions=True)
                for task, result in zip(bg_tasks, results):
                    if isinstance(result, BaseException) and not isinstance(
                        result, (asyncio.CancelledError, KeyboardInterrupt)
                    ):
                        logger.error(
                            "Task %s failed during shutdown: %s",
                            task.get_name(), result, exc_info=result,
                        )
                await api_runner.cleanup()
                if socket_path:
                    Path(socket_path).unlink(missing_ok=True)
                logger.info("Jabali Security stopped.")

    async def _scan_worker(self, worker_id: int) -> None:
        """Worker: read file -> behavior check -> scan -> score -> respond."""
        reg = self._registry
        logger.info("Scan worker %d started", worker_id)
        while True:
            event = await reg.scan_queue.get()
            try:
                # Record behavior and get behavioral findings
                behavior_findings = await reg.behavior.record_event(event)

                # Read file content (with post-read size guard)
                try:
                    content = await asyncio.to_thread(Path(event.path).read_bytes)
                except (FileNotFoundError, PermissionError, OSError):
                    # File gone -- still evaluate behavior findings if any
                    if behavior_findings:
                        score = reg.scoring.evaluate(event, behavior_findings)
                        if score.action != "ignore":
                            await reg.response.handle(event, score)
                    continue
                if len(content) > self.config.max_file_size:
                    continue

                # Check hash cache -- skip known-clean files
                file_hash = reg.hash_cache.get_hash(content)
                if reg.hash_cache.is_known_clean(file_hash) and not behavior_findings:
                    continue

                # Run all content scanners
                content_findings = await reg.scanner.scan(event.path, content)

                # Merge content + behavior findings
                all_findings = content_findings + behavior_findings

                if not all_findings:
                    reg.hash_cache.mark_clean(file_hash)
                    continue

                # Score and respond
                score = reg.scoring.evaluate(event, all_findings)
                reg.hash_cache.mark_dirty(file_hash)

                if score.action != "ignore":
                    await reg.response.handle(event, score)

            except Exception:
                logger.exception("Worker %d: error processing %s", worker_id, event.path)
            finally:
                reg.scan_queue.task_done()

    @staticmethod
    def _make_waf_callback(incidents):
        """Build the async callback that persists WAF events to the database."""
        async def _on_waf_event(event):
            await incidents.save_waf_event(event)

        return _on_waf_event


