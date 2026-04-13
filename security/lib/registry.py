"""ComponentRegistry — extracted construction and lifecycle from SecurityDaemon."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from lib.behavior_tracker import BehaviorTracker
from lib.bruteforce.firewall import FirewallManager
from lib.cleanup.engine import CleanupEngine
from lib.cleanup.scheduler import ScanScheduler
from lib.config import JabaliConfig
from lib.crowdsec.client import CrowdSecClient
from lib.filter import PreFilter
from lib.hash_cache import HashCache
from lib.incidents import IncidentStore
from lib.proactive.process_killer import ProactiveProcessKiller
from lib.process_monitor import ProcessMonitor
from lib.quarantine import QuarantineManager
from lib.queue import ScanQueue
from lib.response import ResponseEngine
from lib.scanner import ScanOrchestrator
from lib.scoring import ScoringEngine
from lib.threat_intel.feed_manager import FeedManager
from lib.ufw.manager import UFWManager
from lib.waf.audit_log_parser import ModSecAuditLogParser
from lib.waf.rule_manager import WafRuleManager
from lib.watcher.inotify import InotifyWatcher
from lib.webshield.manager import WebShieldManager

logger = logging.getLogger("jabali-security")


@dataclass
class ComponentRegistry:
    """Holds every runtime component and manages their lifecycle."""

    config: JabaliConfig
    scan_queue: ScanQueue
    pre_filter: PreFilter
    watcher: InotifyWatcher
    hash_cache: HashCache
    scanner: ScanOrchestrator
    scoring: ScoringEngine
    behavior: BehaviorTracker
    incidents: IncidentStore
    firewall: FirewallManager
    quarantine: QuarantineManager
    response: ResponseEngine
    proc_monitor: ProcessMonitor
    proactive_killer: ProactiveProcessKiller

    # Optional components
    waf_rules: WafRuleManager | None = None
    waf_parser: ModSecAuditLogParser | None = None
    cleanup_engine: CleanupEngine | None = None
    scan_scheduler: ScanScheduler | None = None
    feed_manager: FeedManager | None = None
    webshield: WebShieldManager | None = None
    ufw: UFWManager | None = None
    crowdsec: CrowdSecClient | None = None

    @classmethod
    async def build(cls, config: JabaliConfig, disabled: set[str] | None = None) -> ComponentRegistry:
        disabled = disabled or set()

        data_dir = Path(config.data_dir)
        data_dir.mkdir(parents=True, exist_ok=True)

        scan_queue = ScanQueue()
        pre_filter = PreFilter(config)
        watcher = InotifyWatcher(
            watch_dirs=config.watch_dirs,
            pre_filter=pre_filter,
        )
        hash_cache = HashCache(persist_path=data_dir / "hash_cache.json")
        scanner = ScanOrchestrator(config)
        scoring = ScoringEngine(config)
        behavior = BehaviorTracker(ttl=config.behavior_ttl)

        incidents = IncidentStore(db_path=data_dir / "incidents.db")
        firewall = FirewallManager(backend=config.firewall_backend)

        cleanup_engine = _build_cleanup(config) if "cleanup" not in disabled else None
        quarantine = QuarantineManager(base_dir=config.quarantine_dir)
        response = ResponseEngine(config, quarantine, incidents, cleanup=cleanup_engine)

        proc_monitor = ProcessMonitor(
            poll_interval=config.process_poll_interval,
        )
        proactive_killer = ProactiveProcessKiller(
            enabled=config.process_kill_enabled,
            threshold=config.process_kill_threshold,
            min_uid=config.process_kill_min_uid,
            whitelist_commands=config.process_kill_whitelist,
        )

        waf_rules, waf_parser = (
            _build_waf(config) if "waf" not in disabled else (None, None)
        )
        scan_scheduler = (
            _build_scheduler(config, scanner, scoring) if "scheduler" not in disabled else None
        )
        feed_manager = (
            _build_threat_intel(config) if "threat_intel" not in disabled else None
        )
        webshield = (
            _build_webshield(config) if "webshield" not in disabled else None
        )
        ufw = (
            _build_ufw(config) if "ufw" not in disabled else None
        )
        crowdsec = (
            _build_crowdsec(config) if "crowdsec" not in disabled else None
        )

        return cls(
            config=config,
            scan_queue=scan_queue,
            pre_filter=pre_filter,
            watcher=watcher,
            hash_cache=hash_cache,
            scanner=scanner,
            scoring=scoring,
            behavior=behavior,
            incidents=incidents,
            firewall=firewall,
            quarantine=quarantine,
            response=response,
            proc_monitor=proc_monitor,
            proactive_killer=proactive_killer,
            waf_rules=waf_rules,
            waf_parser=waf_parser,
            cleanup_engine=cleanup_engine,
            scan_scheduler=scan_scheduler,
            feed_manager=feed_manager,
            webshield=webshield,
            ufw=ufw,
            crowdsec=crowdsec,
        )

    async def __aenter__(self) -> ComponentRegistry:
        await self.incidents.open()
        await self.firewall.initialize()
        await _sync_blocked_ips(self.incidents, self.firewall)

        return self

    async def __aexit__(self, _exc_type, _exc_val, _exc_tb) -> None:
        if self.waf_parser:
            await self.waf_parser.stop()
        if self.watcher is not None:
            await self.watcher.stop()
        if self.hash_cache is not None:
            self.hash_cache.save()
        if self.incidents is not None:
            await self.incidents.close()

    def populate_app(self, app, daemon=None) -> None:
        app["config"] = self.config
        app["daemon"] = daemon
        app["incidents"] = self.incidents
        app["quarantine"] = self.quarantine
        app["scanner"] = self.scanner
        app["scoring"] = self.scoring
        app["firewall"] = self.firewall
        app["waf_parser"] = self.waf_parser
        app["waf_rules"] = self.waf_rules
        app["proactive_killer"] = self.proactive_killer
        app["cleanup"] = self.cleanup_engine
        app["scheduler"] = self.scan_scheduler
        app["threat_intel"] = self.feed_manager
        app["webshield"] = self.webshield
        app["ufw"] = self.ufw
        app["crowdsec"] = self.crowdsec
        # GeoIP manager (lazy-loaded, lives alongside webshield)
        if self.config.geoip_enabled:
            from lib.webshield.geoip import GeoIPManager
            app["geoip"] = GeoIPManager(
                db_path=self.config.geoip_db_path,
                license_key=self.config.geoip_maxmind_license_key,
            )
        else:
            app["geoip"] = None

    def background_tasks(self, daemon) -> list:
        tasks = []
        tasks.append(self.watcher.start(self.scan_queue))
        for i in range(self.config.workers):
            tasks.append(daemon._scan_worker(i))
        tasks.append(self.proc_monitor.run(self.proactive_killer.handle_threats))
        if self.config.waf_enabled and self.waf_parser is not None:
            tasks.append(self.waf_parser.run(
                daemon._make_waf_callback(self.incidents)
            ))
        if self.scan_scheduler is not None:
            tasks.append(self.scan_scheduler.run())
        if self.feed_manager is not None:
            tasks.append(self.feed_manager.run_periodic_updates(
                interval_hours=self.config.threat_intel_update_interval,
            ))
        if self.crowdsec is not None:
            tasks.append(self.crowdsec.run_sync_loop(
                interval=self.config.crowdsec_sync_interval,
            ))
        return tasks


# ---------------------------------------------------------------------------
# Private builder functions
# ---------------------------------------------------------------------------



def _build_waf(config: JabaliConfig) -> tuple[WafRuleManager | None, ModSecAuditLogParser | None]:
    if not config.waf_enabled:
        return None, None

    rules = WafRuleManager(
        overrides_file=config.waf_overrides_file,
        rules_dir=config.waf_rules_dir,
        web_server=config.waf_web_server,
        nginx_include=config.waf_nginx_include,
    )
    parser = ModSecAuditLogParser(
        log_path=config.waf_audit_log,
        log_type=config.waf_audit_log_type,
    )
    return rules, parser


def _build_cleanup(config: JabaliConfig) -> CleanupEngine | None:
    if not config.cleanup_enabled:
        return None
    return CleanupEngine(
        enabled=True,
        auto=config.cleanup_auto,
        use_checksums=config.cleanup_cms_checksums,
    )


def _build_scheduler(
    config: JabaliConfig, scanner: ScanOrchestrator, scoring: ScoringEngine
) -> ScanScheduler | None:
    if not config.scheduled_scan_enabled:
        return None
    return ScanScheduler(
        config=config,
        scanner=scanner,
        scoring=scoring,
        enabled=True,
        interval_hours=config.scheduled_scan_interval,
        paths=config.scheduled_scan_paths,
    )


def _build_threat_intel(config: JabaliConfig) -> FeedManager | None:
    if not config.threat_intel_enabled:
        return None
    return FeedManager(
        data_dir=config.data_dir,
        enabled_feeds=config.threat_intel_feeds,
    )


def _build_webshield(config: JabaliConfig) -> WebShieldManager | None:
    if not config.webshield_enabled:
        return None
    return WebShieldManager(
        config_dir=config.webshield_nginx_conf_dir,
        rate_limiting=config.webshield_rate_limiting,
        rate_limit=config.webshield_rate_limit,
        rate_burst=config.webshield_rate_burst,
        challenge_enabled=config.webshield_challenge_enabled,
        bot_filtering=config.webshield_bot_filtering,
        geoip_enabled=config.geoip_enabled,
        geoip_db_path=config.geoip_db_path,
        geoip_blocked_countries=config.geoip_blocked_countries,
        geoip_allowed_countries=config.geoip_allowed_countries,
        geoip_action=config.geoip_action,
    )


def _build_crowdsec(config: JabaliConfig) -> CrowdSecClient | None:
    if config.crowdsec_enabled == "no":
        return None
    if not config.crowdsec_bouncer_key:
        if config.crowdsec_enabled == "yes":
            logger.warning("CrowdSec enabled but no bouncer key configured")
        return None
    logger.info("CrowdSec LAPI client initialized: %s", config.crowdsec_lapi_url)
    return CrowdSecClient(
        lapi_url=config.crowdsec_lapi_url,
        api_key=config.crowdsec_bouncer_key,
    )


def _build_ufw(config: JabaliConfig) -> UFWManager | None:
    if not config.ufw_enabled:
        return None
    logger.info("UFW management enabled -- firewall control available via REST API")
    return UFWManager()


async def _sync_blocked_ips(incidents: IncidentStore, firewall: FirewallManager) -> None:
    if incidents is None:
        return
    blocked = await incidents.get_blocked_ips()
    sync_count = 0
    now = datetime.now(timezone.utc)
    for entry in blocked:
        expires_at = entry.get("expires_at")
        if expires_at:
            try:
                exp = datetime.fromisoformat(expires_at)
                # Fix naive datetimes — assume UTC if no tzinfo
                if exp.tzinfo is None:
                    exp = exp.replace(tzinfo=timezone.utc)
                remaining = int((exp - now).total_seconds())
                if remaining <= 0:
                    # Already expired — clean up from DB and skip
                    await incidents.delete_blocked_ip(entry["ip"])
                    continue
                await firewall.block_ip(entry["ip"], remaining)
            except (ValueError, TypeError):
                # Unparseable expiry — treat as permanent
                await firewall.block_ip(entry["ip"], 0)
        else:
            # No expiry = permanent block
            await firewall.block_ip(entry["ip"], 0)
        sync_count += 1
    if sync_count:
        logger.info("Synced %d blocked IPs to firewall", sync_count)
