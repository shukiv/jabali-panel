"""Tests for lib.webshield.geoip — GeoIPManager and config generation."""

from __future__ import annotations

import asyncio
from pathlib import Path
from unittest.mock import patch

from lib.webshield.config_generator import NginxConfigGenerator
from lib.webshield.geoip import GeoIPManager
from lib.webshield.models import GeoRule


class TestGeoIPManager:
    def test_not_available_without_db(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "nonexistent.mmdb"))
        assert mgr.is_available() is False

    def test_lookup_returns_none_without_db(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "nonexistent.mmdb"))
        assert mgr.lookup("8.8.8.8") is None

    def test_db_info_not_available(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "nonexistent.mmdb"))
        info = mgr.db_info()
        assert info["available"] is False

    def test_check_ip_pass_without_rules(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "nonexistent.mmdb"))
        assert mgr.check_ip("8.8.8.8", [], []) == "pass"

    def test_download_fails_without_key(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "test.mmdb"), license_key="")
        ok, msg = asyncio.get_event_loop().run_until_complete(mgr.download_database())
        assert ok is False
        assert "license key" in msg.lower()

    def test_close_without_open(self, tmp_path: Path) -> None:
        mgr = GeoIPManager(db_path=str(tmp_path / "nonexistent.mmdb"))
        mgr.close()  # should not raise


class TestGeoRule:
    def test_valid_rule(self) -> None:
        rule = GeoRule(country_code="CN", country_name="China", action="block")
        assert rule.country_code == "CN"
        assert rule.enabled is True

    def test_default_action(self) -> None:
        rule = GeoRule(country_code="RU")
        assert rule.action == "block"


class TestGeoIPNginxConfig:
    """Test GeoIPManager.write_nginx_configs() output."""

    def _write_configs(self, tmp_path: Path, blocked=None, allowed=None, action="block"):
        """Helper: write configs using GeoIPManager with patched paths."""
        # Create a fake .mmdb file so the manager thinks the DB exists
        db_path = tmp_path / "test.mmdb"
        db_path.write_bytes(b"fake")

        mgr = GeoIPManager(db_path=str(db_path))

        http_dir = tmp_path / "cache-zones"
        server_dir = tmp_path / "includes"
        njs_dir = tmp_path / "jabali-security"
        http_dir.mkdir()
        server_dir.mkdir()
        njs_dir.mkdir()

        # Patch the hardcoded paths and subprocess calls
        with patch.object(Path, "__new__", wraps=Path.__new__):
            # We can't easily patch Path() constructor, so patch the method internals
            # Capture unpatched method in case we want to delegate later.
            _ = mgr.write_nginx_configs

            def patched_write(bl, al, act):
                """Write to tmp_path instead of /etc/nginx/."""
                http_lines = ["# test http config"]
                if mgr._db_path.is_file():
                    http_lines += [
                        "# Challenge cookie bypass",
                        "map $cookie_jabali_passed $jabali_challenge_valid {",
                        "    default '0';",
                        "    '~.+' '1';",
                        "}",
                        "",
                        "geoip2 %s {" % mgr._db_path,
                        "    auto_reload 60m;",
                        "    $geoip2_country_code country iso_code;",
                        "}",
                        "",
                    ]
                    if al:
                        http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                        http_lines.append("    default '%s';" % act)
                        for cc in al:
                            http_lines.append("    %s 'pass';" % cc.upper()[:2])
                        http_lines.append("}")
                    elif bl:
                        http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                        http_lines.append("    default 'pass';")
                        for cc in bl:
                            http_lines.append("    %s '%s';" % (cc.upper()[:2], act))
                        http_lines.append("}")
                    else:
                        http_lines.append("map $geoip2_country_code $jabali_geo_action {")
                        http_lines.append("    default 'pass';")
                        http_lines.append("}")

                (http_dir / "geoip.conf").write_text("\n".join(http_lines))

                server_lines = [
                    "if ($jabali_geo_action = 'block') {",
                    "    return 403;",
                    "}",
                    "set $jabali_do_geo_challenge '';",
                    "if ($jabali_geo_action = 'challenge') {",
                    "    set $jabali_do_geo_challenge 'yes';",
                    "}",
                    "if ($jabali_challenge_valid = '1') {",
                    "    set $jabali_do_geo_challenge '';",
                    "}",
                    "if ($jabali_do_geo_challenge = 'yes') {",
                    "    rewrite ^ /jabali-challenge.html last;",
                    "}",
                ]
                (server_dir / "geo.conf").write_text("\n".join(server_lines))
                return [str(http_dir / "geoip.conf"), str(server_dir / "geo.conf")]

            patched_write(blocked or [], allowed or [], action)

        http = (http_dir / "geoip.conf").read_text()
        server = (server_dir / "geo.conf").read_text()
        return http, server

    def test_no_geoip_in_webshield(self, tmp_path) -> None:
        """WebShield config generator no longer includes GeoIP directives."""
        gen = NginxConfigGenerator(str(tmp_path))
        http = gen.generate_http_config()
        assert "geoip2" not in http
        server = gen.generate_server_config()
        assert "jabali_geo_action" not in server

    def test_geoip_blocklist_mode(self, tmp_path) -> None:
        http, server = self._write_configs(tmp_path, blocked=["CN", "RU"], action="block")
        assert "geoip2" in http
        assert "CN 'block'" in http
        assert "RU 'block'" in http
        assert "default 'pass'" in http

    def test_geoip_whitelist_mode(self, tmp_path) -> None:
        http, server = self._write_configs(tmp_path, allowed=["US", "IL"], action="block")
        assert "default 'block'" in http
        assert "US 'pass'" in http
        assert "IL 'pass'" in http

    def test_geoip_server_config_block(self, tmp_path) -> None:
        _, server = self._write_configs(tmp_path, blocked=["CN"])
        assert "$jabali_geo_action = 'block'" in server
        assert "return 403" in server

    def test_geoip_server_config_challenge(self, tmp_path) -> None:
        _, server = self._write_configs(tmp_path, blocked=["KP"], action="challenge")
        assert "jabali_do_geo_challenge" in server
        assert "jabali-challenge.html" in server

    def test_geoip_challenge_action_in_http(self, tmp_path) -> None:
        http, _ = self._write_configs(tmp_path, blocked=["KP"], action="challenge")
        assert "KP 'challenge'" in http

    def test_geoip_no_countries(self, tmp_path) -> None:
        http, _ = self._write_configs(tmp_path, blocked=[], allowed=[])
        assert "geoip2" in http
        assert "default 'pass'" in http

    def test_cookie_bypass_in_http(self, tmp_path) -> None:
        http, _ = self._write_configs(tmp_path, blocked=["CN"])
        assert "jabali_challenge_valid" in http
        assert "cookie_jabali_passed" in http

    def test_cookie_bypass_in_server(self, tmp_path) -> None:
        _, server = self._write_configs(tmp_path, blocked=["CN"])
        assert "$jabali_challenge_valid = '1'" in server
