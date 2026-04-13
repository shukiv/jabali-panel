"""Tests for lib.webshield.config_generator — NginxConfigGenerator."""

from __future__ import annotations

from pathlib import Path

from lib.webshield.config_generator import NginxConfigGenerator


class TestHTTPConfig:
    def test_contains_rate_limit_zone(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limit=15, rate_limiting=True)
        config = gen.generate_http_config()
        assert "limit_req_zone" in config
        assert "rate=15r/s" in config

    def test_default_rate_limit(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limiting=True)
        config = gen.generate_http_config()
        assert "rate=10r/s" in config

    def test_no_rate_limit_when_disabled(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limiting=False)
        config = gen.generate_http_config()
        assert "limit_req_zone" not in config

    def test_bot_detection_map_present(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_http_config()
        assert "map $http_user_agent $jabali_bot_action" in config

    def test_bot_map_has_block_entries(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_http_config()
        assert "'block'" in config

    def test_bot_map_has_challenge_entries(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_http_config()
        assert "'challenge'" in config

    def test_bot_map_has_default_pass(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_http_config()
        assert "default 'pass'" in config


class TestServerConfig:
    def test_contains_limit_req_with_burst(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_burst=30, rate_limiting=True)
        config = gen.generate_server_config()
        assert "limit_req zone=jabali_ratelimit burst=30" in config

    def test_default_burst(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limiting=True)
        config = gen.generate_server_config()
        assert "burst=20" in config

    def test_no_rate_limit_when_disabled(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limiting=False)
        config = gen.generate_server_config()
        assert "limit_req" not in config

    def test_bot_blocking_condition(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_server_config()
        assert "if ($jabali_bot_action = 'block')" in config
        assert "return 403" in config

    def test_bot_challenge_condition(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        config = gen.generate_server_config()
        assert "if ($jabali_bot_action = 'challenge')" in config

    def test_rate_limit_status_429(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path), rate_limiting=True)
        config = gen.generate_server_config()
        assert "limit_req_status 429" in config


class TestBlockedIPsConfig:
    def test_geo_format(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        result = gen.generate_blocked_ips_conf(["1.2.3.4", "5.6.7.8"])
        assert "1.2.3.4 1;" in result
        assert "5.6.7.8 1;" in result

    def test_empty_list(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        result = gen.generate_blocked_ips_conf([])
        assert "auto-generated" in result
        # Only the header line
        lines = [line for line in result.strip().splitlines() if not line.startswith("#")]
        assert lines == []

    def test_cidr_in_blocked_list(self, tmp_path) -> None:
        gen = NginxConfigGenerator(str(tmp_path))
        result = gen.generate_blocked_ips_conf(["10.0.0.0/8"])
        assert "10.0.0.0/8 1;" in result


class TestWriteConfigs:
    def test_creates_files_on_disk(self, tmp_path: Path) -> None:
        config_dir = tmp_path / "nginx" / "jabali"
        gen = NginxConfigGenerator(str(config_dir), rate_limiting=True)
        written = gen.write_configs()
        assert len(written) == 2
        http_conf = config_dir / "jabali-webshield-http.conf"
        server_conf = config_dir / "jabali-webshield-server.conf"
        assert http_conf.is_file()
        assert server_conf.is_file()
        assert "limit_req_zone" in http_conf.read_text(encoding="utf-8")
        assert "limit_req zone=" in server_conf.read_text(encoding="utf-8")

    def test_bot_only_no_rate_limit(self, tmp_path: Path) -> None:
        config_dir = tmp_path / "nginx" / "jabali"
        gen = NginxConfigGenerator(str(config_dir), rate_limiting=False)
        written = gen.write_configs()
        assert len(written) == 2
        http_conf = config_dir / "jabali-webshield-http.conf"
        server_conf = config_dir / "jabali-webshield-server.conf"
        assert "limit_req_zone" not in http_conf.read_text(encoding="utf-8")
        assert "limit_req" not in server_conf.read_text(encoding="utf-8")
        assert "jabali_bot_action" in server_conf.read_text(encoding="utf-8")

    def test_creates_parent_directories(self, tmp_path: Path) -> None:
        config_dir = tmp_path / "deep" / "nested" / "dir"
        gen = NginxConfigGenerator(str(config_dir))
        gen.write_configs()
        assert config_dir.is_dir()

    def test_returns_written_paths(self, tmp_path: Path) -> None:
        config_dir = tmp_path / "conf"
        gen = NginxConfigGenerator(str(config_dir))
        written = gen.write_configs()
        for path_str in written:
            assert Path(path_str).is_file()
