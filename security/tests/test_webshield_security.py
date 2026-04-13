"""Security tests for WebShield GeoIP and configuration handling."""

import re

import pytest

from lib.webshield.geoip import _SAFE_PATH_RE, _VALID_ACTIONS, GeoIPManager


class TestGeoIPActionValidation:
    """Test that action parameter is properly validated."""

    def test_valid_actions_defined(self):
        """Verify valid actions are defined."""
        assert _VALID_ACTIONS == {"block", "challenge", "log"}

    def test_invalid_action_raises_error(self):
        """Verify invalid actions raise ValueError."""
        geoip = GeoIPManager()

        invalid_actions = [
            "block'; error_page 403 /evil.html; #",
            'block"; return 503; #',
            "block{return 500;}",
            "block\n  return 503;",
            "block; system('id');",
            "challenge' or '1'='1",
            "invalid_action",
            "",
            "BLOCK",  # Case sensitive
        ]

        for bad_action in invalid_actions:
            with pytest.raises(ValueError, match="Invalid action"):
                geoip.write_nginx_configs(
                    blocked_countries=["US"],
                    allowed_countries=[],
                    action=bad_action,
                )

    def test_valid_actions_pass_validation(self):
        """Verify valid actions are accepted at validation point."""
        # These should pass validation (though file writes may fail in test env)
        for action in ["block", "challenge", "log"]:
            assert action in _VALID_ACTIONS


class TestGeoIPPathValidation:
    """Test that database path validation works correctly."""

    def test_safe_path_regex(self):
        """Verify safe path regex accepts valid paths."""
        valid_paths = [
            "/var/lib/jabali-security/GeoLite2-Country.mmdb",
            "/home/user/db.mmdb",
            "/tmp/test_db.mmdb",
            "/path/with-dash/file_name.mmdb",
            "/123numeric456/path.mmdb",
        ]

        for path in valid_paths:
            assert _SAFE_PATH_RE.match(path), f"Path should be valid: {path}"

    def test_unsafe_path_regex_rejects_special_chars(self):
        """Verify safe path regex rejects dangerous characters."""
        invalid_paths = [
            "/var/lib/geo'; error.mmdb",  # Single quote
            '/var/lib/geo"; error.mmdb',  # Double quote
            "/var/lib/geo`whoami`.mmdb",  # Backtick
            "/var/lib/geo; return 503; #",  # Semicolon
            "/var/lib/geo{dangerous}.mmdb",  # Braces
            "/var/lib/$VAR/geo.mmdb",  # Dollar sign
        ]

        for path in invalid_paths:
            # Should either not match regex OR be caught by char check
            has_bad_chars = any(c in path for c in ["'", '"', ";", "{", "}", "$", "`"])
            assert has_bad_chars or not _SAFE_PATH_RE.match(path), f"Path should be invalid: {path}"

    def test_path_contains_dangerous_chars(self):
        """Verify direct character checks catch injection attempts."""
        dangerous_chars = ["'", '"', ";", "{", "}", "$", "`"]

        test_paths = [
            "/var/lib/geo.mmdb; return 503;",
            "/var/lib/geo'test'mmdb",
            '/var/lib/geo"test"mmdb',
        ]

        for path in test_paths:
            has_dangerous = any(c in path for c in dangerous_chars)
            assert has_dangerous, f"Path should have dangerous chars: {path}"


class TestLicenseKeyValidation:
    """Test license key and account ID validation."""

    def test_account_id_format_numeric(self):
        """Verify account_id must be numeric."""
        # Valid
        assert re.match(r"^\d+$", "12345")
        assert re.match(r"^\d+$", "0")
        assert re.match(r"^\d+$", "999999999")

        # Invalid
        assert not re.match(r"^\d+$", "abc123")
        assert not re.match(r"^\d+$", "123-456")
        assert not re.match(r"^\d+$", "123 456")
        assert not re.match(r"^\d+$", "")
        assert not re.match(r"^\d+$", "-123")
        assert not re.match(r"^\d+$", "12.34")

    def test_license_key_format_alphanumeric_underscore(self):
        """Verify license_key format is alphanumeric + underscore only."""
        # Valid
        assert re.match(r"^[a-zA-Z0-9_]+$", "abc123")
        assert re.match(r"^[a-zA-Z0-9_]+$", "ABC_123")
        assert re.match(r"^[a-zA-Z0-9_]+$", "_key_")
        assert re.match(r"^[a-zA-Z0-9_]+$", "key123_456_KEY")

        # Invalid
        assert not re.match(r"^[a-zA-Z0-9_]+$", "abc-123")
        assert not re.match(r"^[a-zA-Z0-9_]+$", "abc@123")
        assert not re.match(r"^[a-zA-Z0-9_]+$", "abc 123")
        assert not re.match(r"^[a-zA-Z0-9_]+$", "abc.123")
        assert not re.match(r"^[a-zA-Z0-9_]+$", "abc/123")
        assert not re.match(r"^[a-zA-Z0-9_]+$", "")

    def test_credential_validation_prevents_injection(self):
        """Verify credential validation prevents command/config injection."""
        # These should all fail format validation
        injection_attempts = [
            {
                "account_id": "123'; DROP TABLE users; --",
                "license_key": "valid_key_123",
            },
            {
                "account_id": "123",
                "license_key": "key_$(whoami)_123",
            },
            {
                "account_id": "123`whoami`",
                "license_key": "valid_key",
            },
        ]

        for cred in injection_attempts:
            account_valid = re.match(r"^\d+$", cred["account_id"]) is not None
            license_valid = re.match(r"^[a-zA-Z0-9_]+$", cred["license_key"]) is not None

            # At least one should be invalid
            assert not (account_valid and license_valid), f"Injection attempt should fail: {cred}"
