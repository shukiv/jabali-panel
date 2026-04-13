"""Tests for lib.ufw — validators, models, and UFWManager."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, patch

from lib.ufw.models import UfwAppProfile, UfwRule, UfwStatus
from lib.ufw.validators import (
    validate_action,
    validate_app_profile,
    validate_comment,
    validate_direction,
    validate_ip,
    validate_port,
    validate_protocol,
    validate_rule_number,
)

# ---------------------------------------------------------------------------
# Validators
# ---------------------------------------------------------------------------


class TestValidateIP:
    def test_valid_ipv4(self) -> None:
        assert validate_ip("192.168.1.1") == "192.168.1.1"

    def test_valid_ipv4_with_whitespace(self) -> None:
        assert validate_ip("  10.0.0.1  ") == "10.0.0.1"

    def test_valid_ipv6(self) -> None:
        assert validate_ip("::1") == "::1"

    def test_valid_ipv6_full(self) -> None:
        result = validate_ip("2001:0db8:0000:0000:0000:0000:0000:0001")
        assert result == "2001:db8::1"

    def test_valid_cidr_v4(self) -> None:
        assert validate_ip("10.0.0.0/8") == "10.0.0.0/8"

    def test_valid_cidr_v4_non_strict(self) -> None:
        # strict=False means host bits can be set
        assert validate_ip("192.168.1.100/24") == "192.168.1.0/24"

    def test_valid_cidr_v6(self) -> None:
        assert validate_ip("2001:db8::/32") == "2001:db8::/32"

    def test_invalid_string(self) -> None:
        assert validate_ip("not-an-ip") is None

    def test_empty_string(self) -> None:
        assert validate_ip("") is None

    def test_whitespace_only(self) -> None:
        assert validate_ip("   ") is None

    def test_injection_attempt(self) -> None:
        assert validate_ip("1.2.3.4; rm -rf /") is None

    def test_partial_ip(self) -> None:
        assert validate_ip("192.168") is None


class TestValidatePort:
    def test_valid_single_port(self) -> None:
        assert validate_port("80") == "80"

    def test_valid_port_high(self) -> None:
        assert validate_port("65535") == "65535"

    def test_valid_port_low(self) -> None:
        assert validate_port("1") == "1"

    def test_valid_range(self) -> None:
        assert validate_port("8000:8080") == "8000:8080"

    def test_valid_service_name(self) -> None:
        assert validate_port("ssh") == "ssh"

    def test_valid_service_name_with_hyphen(self) -> None:
        assert validate_port("submission-ssl") == "submission-ssl"

    def test_port_zero_rejected(self) -> None:
        assert validate_port("0") is None

    def test_port_over_65535_rejected(self) -> None:
        assert validate_port("65536") is None

    def test_reversed_range_rejected(self) -> None:
        assert validate_port("8080:8000") is None

    def test_equal_range_rejected(self) -> None:
        assert validate_port("80:80") is None

    def test_empty_string(self) -> None:
        assert validate_port("") is None

    def test_whitespace_only(self) -> None:
        assert validate_port("   ") is None

    def test_injection_attempt(self) -> None:
        assert validate_port("80; echo hacked") is None

    def test_negative_port(self) -> None:
        assert validate_port("-1") is None

    def test_service_name_starts_with_digit(self) -> None:
        assert validate_port("1abc") is None


class TestValidateProtocol:
    def test_tcp(self) -> None:
        assert validate_protocol("tcp") == "tcp"

    def test_udp(self) -> None:
        assert validate_protocol("udp") == "udp"

    def test_any(self) -> None:
        assert validate_protocol("any") == "any"

    def test_case_insensitive(self) -> None:
        assert validate_protocol("TCP") == "tcp"
        assert validate_protocol("Udp") == "udp"

    def test_with_whitespace(self) -> None:
        assert validate_protocol("  tcp  ") == "tcp"

    def test_invalid(self) -> None:
        assert validate_protocol("icmp") is None

    def test_empty(self) -> None:
        assert validate_protocol("") is None


class TestValidateAction:
    def test_allow(self) -> None:
        assert validate_action("allow") == "allow"

    def test_deny(self) -> None:
        assert validate_action("deny") == "deny"

    def test_reject(self) -> None:
        assert validate_action("reject") == "reject"

    def test_limit(self) -> None:
        assert validate_action("limit") == "limit"

    def test_case_insensitive(self) -> None:
        assert validate_action("ALLOW") == "allow"
        assert validate_action("Deny") == "deny"

    def test_with_whitespace(self) -> None:
        assert validate_action("  reject  ") == "reject"

    def test_invalid(self) -> None:
        assert validate_action("drop") is None
        assert validate_action("accept") is None

    def test_empty(self) -> None:
        assert validate_action("") is None


class TestValidateDirection:
    def test_in(self) -> None:
        assert validate_direction("in") == "in"

    def test_out(self) -> None:
        assert validate_direction("out") == "out"

    def test_case_insensitive(self) -> None:
        assert validate_direction("IN") == "in"
        assert validate_direction("Out") == "out"

    def test_with_whitespace(self) -> None:
        assert validate_direction("  in  ") == "in"

    def test_invalid(self) -> None:
        assert validate_direction("forward") is None
        assert validate_direction("fwd") is None

    def test_empty(self) -> None:
        assert validate_direction("") is None


class TestValidateAppProfile:
    def test_simple_name(self) -> None:
        assert validate_app_profile("OpenSSH") == "OpenSSH"

    def test_name_with_spaces(self) -> None:
        assert validate_app_profile("Nginx Full") == "Nginx Full"

    def test_name_with_dots(self) -> None:
        assert validate_app_profile("Apache.v2") == "Apache.v2"

    def test_name_with_hyphen_underscore(self) -> None:
        assert validate_app_profile("my-app_v2") == "my-app_v2"

    def test_too_long_rejected(self) -> None:
        name = "A" * 65
        assert validate_app_profile(name) is None

    def test_max_length_accepted(self) -> None:
        name = "A" * 64
        assert validate_app_profile(name) == name

    def test_special_chars_rejected(self) -> None:
        assert validate_app_profile("app;rm") is None
        assert validate_app_profile("app$(cmd)") is None
        assert validate_app_profile("app`cmd`") is None

    def test_empty_rejected(self) -> None:
        assert validate_app_profile("") is None

    def test_starts_with_space_after_strip(self) -> None:
        # After strip, starts with valid char
        assert validate_app_profile("  OpenSSH  ") == "OpenSSH"


class TestValidateRuleNumber:
    def test_valid_low(self) -> None:
        assert validate_rule_number(1) is True

    def test_valid_high(self) -> None:
        assert validate_rule_number(9999) is True

    def test_valid_middle(self) -> None:
        assert validate_rule_number(42) is True

    def test_zero_rejected(self) -> None:
        assert validate_rule_number(0) is False

    def test_negative_rejected(self) -> None:
        assert validate_rule_number(-1) is False

    def test_too_large_rejected(self) -> None:
        assert validate_rule_number(10000) is False

    def test_float_rejected(self) -> None:
        assert validate_rule_number(1.5) is False  # type: ignore[arg-type]

    def test_string_rejected(self) -> None:
        assert validate_rule_number("1") is False  # type: ignore[arg-type]


class TestValidateComment:
    def test_valid_comment(self) -> None:
        assert validate_comment("SSH rule") == "SSH rule"

    def test_printable_ascii(self) -> None:
        text = "Rule #1 for HTTP (port 80)"
        assert validate_comment(text) == text

    def test_control_chars_rejected(self) -> None:
        assert validate_comment("bad\x00comment") is None
        assert validate_comment("bad\ncomment") is None
        assert validate_comment("bad\tcomment") is None

    def test_too_long_rejected(self) -> None:
        text = "a" * 257
        assert validate_comment(text) is None

    def test_max_length_accepted(self) -> None:
        text = "a" * 256
        assert validate_comment(text) == text

    def test_empty_rejected(self) -> None:
        assert validate_comment("") is None

    def test_whitespace_only_rejected(self) -> None:
        assert validate_comment("   ") is None

    def test_with_leading_trailing_whitespace(self) -> None:
        assert validate_comment("  hello world  ") == "hello world"


# ---------------------------------------------------------------------------
# Pydantic models
# ---------------------------------------------------------------------------


class TestUfwRule:
    def test_basic_construction(self) -> None:
        rule = UfwRule(number=1, to="22/tcp", action="ALLOW", direction="IN")
        assert rule.number == 1
        assert rule.to == "22/tcp"
        assert rule.action == "ALLOW"

    def test_from_alias(self) -> None:
        """The 'from' field is aliased to from_ip to avoid Python keyword clash."""
        rule = UfwRule(
            number=1,
            to="22/tcp",
            action="ALLOW",
            **{"from": "192.168.1.0/24"},
        )
        assert rule.from_ip == "192.168.1.0/24"

    def test_from_ip_in_model_dump(self) -> None:
        rule = UfwRule(number=1, to="80/tcp", action="ALLOW")
        data = rule.model_dump()
        assert "from_ip" in data
        assert data["from_ip"] == ""

    def test_v6_default_false(self) -> None:
        rule = UfwRule(number=1, to="80", action="ALLOW")
        assert rule.v6 is False

    def test_v6_true(self) -> None:
        rule = UfwRule(number=1, to="80/tcp", action="ALLOW", v6=True)
        assert rule.v6 is True

    def test_raw_field(self) -> None:
        raw = "[  1] 22/tcp                     ALLOW IN    Anywhere"
        rule = UfwRule(number=1, to="22/tcp", action="ALLOW", raw=raw)
        assert rule.raw == raw


class TestUfwStatus:
    def test_default_values(self) -> None:
        status = UfwStatus()
        assert status.available is False
        assert status.active is False
        assert status.rules == []
        assert status.rules_count == 0

    def test_model_dump(self) -> None:
        status = UfwStatus(
            available=True,
            active=True,
            default_incoming="deny",
            default_outgoing="allow",
            default_routed="disabled",
            rules=[
                UfwRule(number=1, to="22/tcp", action="ALLOW"),
            ],
            rules_count=1,
        )
        data = status.model_dump()
        assert data["available"] is True
        assert data["active"] is True
        assert data["default_incoming"] == "deny"
        assert data["default_outgoing"] == "allow"
        assert data["default_routed"] == "disabled"
        assert len(data["rules"]) == 1
        assert data["rules_count"] == 1

    def test_empty_status(self) -> None:
        status = UfwStatus(available=True, active=False)
        data = status.model_dump()
        assert data["active"] is False
        assert data["rules"] == []


class TestUfwAppProfile:
    def test_basic_construction(self) -> None:
        profile = UfwAppProfile(name="OpenSSH")
        assert profile.name == "OpenSSH"
        assert profile.title == ""
        assert profile.description == ""
        assert profile.ports == ""

    def test_full_construction(self) -> None:
        profile = UfwAppProfile(
            name="Nginx Full",
            title="Web Server (Nginx, HTTP + HTTPS)",
            description="Small, but very powerful and efficient web server",
            ports="80,443/tcp",
        )
        assert profile.name == "Nginx Full"
        assert profile.ports == "80,443/tcp"

    def test_model_dump(self) -> None:
        profile = UfwAppProfile(name="Apache", ports="80/tcp")
        data = profile.model_dump()
        assert data["name"] == "Apache"
        assert data["ports"] == "80/tcp"


# ---------------------------------------------------------------------------
# UFWManager — subprocess mocking helpers
# ---------------------------------------------------------------------------


def _make_mock_proc(returncode: int, stdout: str, stderr: str = "") -> AsyncMock:
    """Create a mock process that returns given output."""
    proc = AsyncMock()
    proc.returncode = returncode
    proc.communicate = AsyncMock(
        return_value=(stdout.encode(), stderr.encode())
    )
    return proc


# ---------------------------------------------------------------------------
# UFWManager — parse ufw status verbose
# ---------------------------------------------------------------------------


class TestGetStatus:
    STATUS_VERBOSE_ACTIVE = (
        "Status: active\n"
        "Logging: on (low)\n"
        "Default: deny (incoming), allow (outgoing), disabled (routed)\n"
        "New profiles: skip\n"
        "\n"
        "To                         Action      From\n"
        "--                         ------      ----\n"
        "22/tcp                     ALLOW IN    Anywhere\n"
        "80/tcp                     ALLOW IN    Anywhere\n"
    )

    STATUS_VERBOSE_INACTIVE = "Status: inactive\n"

    async def test_active_status_with_defaults(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        # We need to mock two calls: get_status calls _run for status verbose,
        # then calls list_rules which calls _run for status numbered.
        status_proc = _make_mock_proc(0, self.STATUS_VERBOSE_ACTIVE)
        numbered_output = (
            "Status: active\n"
            "\n"
            "     To                         Action      From\n"
            "     --                         ------      ----\n"
            "[  1] 22/tcp                     ALLOW IN    Anywhere\n"
            "[  2] 80/tcp                     ALLOW IN    Anywhere\n"
        )
        numbered_proc = _make_mock_proc(0, numbered_output)

        with patch("asyncio.create_subprocess_exec", side_effect=[status_proc, numbered_proc]):
            status = await mgr.get_status()

        assert status.available is True
        assert status.active is True
        assert status.default_incoming == "deny"
        assert status.default_outgoing == "allow"
        assert status.default_routed == "disabled"
        assert len(status.rules) == 2
        assert status.rules_count == 2

    async def test_inactive_status(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        status_proc = _make_mock_proc(0, self.STATUS_VERBOSE_INACTIVE)
        # list_rules still called; returns empty for inactive
        numbered_proc = _make_mock_proc(0, "Status: inactive\n")

        with patch("asyncio.create_subprocess_exec", side_effect=[status_proc, numbered_proc]):
            status = await mgr.get_status()

        assert status.available is True
        assert status.active is False
        assert status.default_incoming == ""
        assert status.rules == []

    async def test_unavailable(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = False

        status = await mgr.get_status()
        assert status.available is False
        assert status.active is False


# ---------------------------------------------------------------------------
# UFWManager — parse ufw status numbered
# ---------------------------------------------------------------------------


class TestListRules:
    NUMBERED_OUTPUT_MIXED = (
        "Status: active\n"
        "\n"
        "     To                         Action      From\n"
        "     --                         ------      ----\n"
        "[  1] 22/tcp                     ALLOW IN    Anywhere\n"
        "[  2] 80/tcp                     DENY IN     192.168.1.0/24\n"
        "[  3] 443/tcp (v6)               ALLOW IN    Anywhere (v6)\n"
    )

    async def test_parses_mixed_ipv4_ipv6(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        proc = _make_mock_proc(0, self.NUMBERED_OUTPUT_MIXED)

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            rules = await mgr.list_rules()

        assert len(rules) == 3

        assert rules[0].number == 1
        assert rules[0].to == "22/tcp"
        assert rules[0].action == "ALLOW"
        assert rules[0].direction == "IN"
        assert rules[0].v6 is False

        assert rules[1].number == 2
        assert rules[1].to == "80/tcp"
        assert rules[1].action == "DENY"
        assert rules[1].from_ip == "192.168.1.0/24"

        assert rules[2].number == 3
        assert rules[2].to == "443/tcp"
        assert rules[2].action == "ALLOW"
        assert rules[2].v6 is True

    async def test_empty_rule_set(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        output = "Status: active\n\n     To   Action   From\n     --   ------   ----\n"
        proc = _make_mock_proc(0, output)

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            rules = await mgr.list_rules()

        assert rules == []

    async def test_unavailable_returns_empty(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = False

        rules = await mgr.list_rules()
        assert rules == []

    async def test_command_failure_returns_empty(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        proc = _make_mock_proc(1, "", "Permission denied")

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            rules = await mgr.list_rules()

        assert rules == []


# ---------------------------------------------------------------------------
# UFWManager — parse ufw app list
# ---------------------------------------------------------------------------


class TestListAppProfiles:
    APP_LIST_OUTPUT = (
        "Available applications:\n"
        "  Nginx Full\n"
        "  Nginx HTTP\n"
        "  Nginx HTTPS\n"
        "  OpenSSH\n"
    )

    async def test_parses_app_list(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        proc = _make_mock_proc(0, self.APP_LIST_OUTPUT)

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            profiles = await mgr.list_app_profiles()

        assert profiles == ["Nginx Full", "Nginx HTTP", "Nginx HTTPS", "OpenSSH"]

    async def test_unavailable_returns_empty(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = False

        profiles = await mgr.list_app_profiles()
        assert profiles == []


# ---------------------------------------------------------------------------
# UFWManager — parse ufw app info
# ---------------------------------------------------------------------------


class TestGetAppInfo:
    APP_INFO_OUTPUT = (
        "Profile: Nginx Full\n"
        "Title: Web Server (Nginx, HTTP + HTTPS)\n"
        "Description: Small, but very powerful and efficient web server\n"
        "\n"
        "Ports:\n"
        "  80,443/tcp\n"
    )

    async def test_parses_app_info(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        proc = _make_mock_proc(0, self.APP_INFO_OUTPUT)

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            profile = await mgr.get_app_info("Nginx Full")

        assert profile is not None
        assert profile.name == "Nginx Full"
        assert profile.title == "Web Server (Nginx, HTTP + HTTPS)"
        assert "powerful" in profile.description

    async def test_unavailable_returns_none(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = False

        profile = await mgr.get_app_info("OpenSSH")
        assert profile is None

    async def test_command_failure_returns_none(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        mgr._available = True

        proc = _make_mock_proc(1, "", "ERROR: No application profiles found")

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            profile = await mgr.get_app_info("Nonexistent")

        assert profile is None


# ---------------------------------------------------------------------------
# UFWManager — add_rule builds correct arg list
# ---------------------------------------------------------------------------


class TestAddRule:
    async def test_simple_allow_port(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.add_rule(action="allow", port="80")

        assert ok is True
        assert msg == "Rule added"
        mock_exec.assert_called_once_with(
            "ufw", "allow", "to", "any", "port", "80",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_deny_from_ip_with_port_and_protocol(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.add_rule(
                action="deny",
                from_ip="10.0.0.0/8",
                port="22",
                protocol="tcp",
                direction="in",
            )

        assert ok is True
        mock_exec.assert_called_once_with(
            "ufw", "in", "deny", "from", "10.0.0.0/8",
            "to", "any", "port", "22", "proto", "tcp",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_with_comment(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.add_rule(
                action="allow", port="443", protocol="tcp", comment="HTTPS access",
            )

        assert ok is True
        args = mock_exec.call_args[0]
        assert "comment" in args
        assert "HTTPS access" in args

    async def test_protocol_any_not_passed(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            await mgr.add_rule(action="allow", port="80", protocol="any")

        args = mock_exec.call_args[0]
        assert "proto" not in args

    async def test_command_failure(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(1, "", "ERROR: Could not add rule")

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            ok, msg = await mgr.add_rule(action="allow", port="80")

        assert ok is False
        assert "ERROR" in msg

    async def test_with_to_ip(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.add_rule(action="allow", to_ip="192.168.1.1")

        assert ok is True
        args = mock_exec.call_args[0]
        assert "to" in args
        assert "192.168.1.1" in args


# ---------------------------------------------------------------------------
# UFWManager — remove_rule uses --force
# ---------------------------------------------------------------------------


class TestRemoveRule:
    async def test_force_flag(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule deleted")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.remove_rule(3)

        assert ok is True
        assert msg == "Rule deleted"
        mock_exec.assert_called_once_with(
            "ufw", "--force", "delete", "3",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_failure(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(1, "", "Could not delete rule")

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            ok, msg = await mgr.remove_rule(999)

        assert ok is False


# ---------------------------------------------------------------------------
# UFWManager — enable / disable use --force
# ---------------------------------------------------------------------------


class TestEnableDisable:
    async def test_enable_uses_force(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Firewall is active and enabled on system startup")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.enable()

        assert ok is True
        mock_exec.assert_called_once_with(
            "ufw", "--force", "enable",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_disable_uses_force(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Firewall stopped and disabled on system startup")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.disable()

        assert ok is True
        mock_exec.assert_called_once_with(
            "ufw", "--force", "disable",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_enable_failure(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(1, "", "Permission denied")

        with patch("asyncio.create_subprocess_exec", return_value=proc):
            ok, msg = await mgr.enable()

        assert ok is False
        assert "Permission denied" in msg


# ---------------------------------------------------------------------------
# UFWManager — available property
# ---------------------------------------------------------------------------


class TestAvailable:
    def test_available_when_ufw_in_path(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        with patch("shutil.which", return_value="/usr/sbin/ufw"):
            assert mgr.available is True

    def test_unavailable_when_ufw_not_in_path(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        with patch("shutil.which", return_value=None):
            assert mgr.available is False

    def test_caches_result(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        with patch("shutil.which", return_value="/usr/sbin/ufw") as mock_which:
            _ = mgr.available
            _ = mgr.available
            mock_which.assert_called_once()


# ---------------------------------------------------------------------------
# UFWManager — reload
# ---------------------------------------------------------------------------


class TestReload:
    async def test_reload(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Firewall reloaded")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.reload()

        assert ok is True
        assert msg == "Firewall reloaded"
        mock_exec.assert_called_once_with(
            "ufw", "reload",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )


# ---------------------------------------------------------------------------
# UFWManager — app allow/deny
# ---------------------------------------------------------------------------


class TestAppAllowDeny:
    async def test_allow_app(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule added")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.allow_app("OpenSSH")

        assert ok is True
        mock_exec.assert_called_once_with(
            "ufw", "allow", "OpenSSH",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def test_deny_app(self) -> None:
        from lib.ufw.manager import UFWManager

        mgr = UFWManager()
        proc = _make_mock_proc(0, "Rule updated")

        with patch("asyncio.create_subprocess_exec", return_value=proc) as mock_exec:
            ok, msg = await mgr.deny_app("Nginx Full")

        assert ok is True
        mock_exec.assert_called_once_with(
            "ufw", "deny", "Nginx Full",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )


# ---------------------------------------------------------------------------
# UFWManager — _run error handling
# ---------------------------------------------------------------------------


class TestRunErrorHandling:
    async def test_oserror_returns_failure(self) -> None:
        from lib.ufw.manager import UFWManager

        with patch("asyncio.create_subprocess_exec", side_effect=OSError("No such file")):
            rc, stdout, stderr = await UFWManager._run("ufw", "status")

        assert rc == 1
        assert stdout == ""
        assert "Command execution failed" in stderr
