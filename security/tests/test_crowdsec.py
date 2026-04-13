"""Tests for lib.crowdsec — scenario weighting and cache semantics."""

from __future__ import annotations

from lib.crowdsec.client import CrowdSecClient, scenario_weight
from lib.crowdsec.models import CrowdSecDecision


class TestScenarioWeight:
    def test_ssh_bruteforce(self) -> None:
        assert scenario_weight("crowdsecurity/ssh-bf") == 60

    def test_http_backdoors_highest(self) -> None:
        # Backdoors should outweigh probing
        assert scenario_weight("crowdsecurity/http-backdoors-attempts") > scenario_weight(
            "crowdsecurity/http-probing"
        )

    def test_sqli_and_xss_equal_severity(self) -> None:
        # SQLi and XSS are both active exploitation, same weight class
        assert scenario_weight("crowdsecurity/http-sqli-probing") == scenario_weight(
            "crowdsecurity/http-xss-probing"
        )

    def test_unknown_scenario_falls_back_to_default(self) -> None:
        assert scenario_weight("crowdsecurity/unknown-scenario-xyz") == 40


class TestApplyStream:
    def _client(self) -> CrowdSecClient:
        # Don't start() — we only exercise _apply_stream and cache
        return CrowdSecClient(lapi_url="http://127.0.0.1:8080", api_key="test")

    def test_new_decision_added_to_cache(self) -> None:
        c = self._client()
        c._apply_stream({
            "new": [{
                "id": 1, "origin": "crowdsec", "type": "ban", "scope": "Ip",
                "value": "1.2.3.4", "duration": "4h0m0s",
                "scenario": "crowdsecurity/ssh-bf",
            }],
            "deleted": None,
        })
        decisions = c.check_ip("1.2.3.4")
        assert len(decisions) == 1
        assert decisions[0].scenario == "crowdsecurity/ssh-bf"

    def test_cidr_value_normalized_to_ip_key(self) -> None:
        c = self._client()
        c._apply_stream({
            "new": [{"id": 2, "value": "5.6.7.8/32", "scenario": "crowdsecurity/http-probing"}],
            "deleted": None,
        })
        # CIDR /32 suffix stripped — lookup by plain IP works
        assert len(c.check_ip("5.6.7.8")) == 1

    def test_deleted_decision_evicts_ip(self) -> None:
        c = self._client()
        c._apply_stream({"new": [{"id": 3, "value": "9.9.9.9"}], "deleted": None})
        assert c.check_ip("9.9.9.9")
        c._apply_stream({"new": None, "deleted": [{"id": 3, "value": "9.9.9.9"}]})
        assert c.check_ip("9.9.9.9") == []

    def test_null_arrays_are_safe(self) -> None:
        c = self._client()
        # LAPI returns null when there are no changes
        c._apply_stream({"new": None, "deleted": None})
        assert c.active_decisions_count == 0

    def test_empty_value_skipped(self) -> None:
        c = self._client()
        c._apply_stream({"new": [{"id": 4, "value": ""}], "deleted": None})
        assert c.active_decisions_count == 0


class TestCheckIpScore:
    def _decision(self, scenario: str, origin: str = "crowdsec") -> CrowdSecDecision:
        return CrowdSecDecision(
            id=1, origin=origin, type="ban", scope="Ip",
            value="1.2.3.4", duration="4h0m0s", scenario=scenario,
        )

    def test_no_decisions_returns_zero(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        assert c.check_ip_score("1.2.3.4") == 0

    def test_local_decision_uses_scenario_weight(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._decisions["1.2.3.4"] = [self._decision("crowdsecurity/ssh-bf")]
        # ssh-bf weight is 60
        assert c.check_ip_score("1.2.3.4") == 60

    def test_capi_origin_gets_confidence_bonus(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._decisions["1.2.3.4"] = [self._decision("crowdsecurity/ssh-bf", origin="CAPI")]
        # ssh-bf (60) + CAPI bonus (20) = 80
        assert c.check_ip_score("1.2.3.4") == 80

    def test_score_capped_at_100(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._decisions["1.2.3.4"] = [self._decision("crowdsecurity/http-backdoors", origin="CAPI")]
        # backdoors (80) + CAPI bonus (20) = 100 — must not exceed
        assert c.check_ip_score("1.2.3.4") == 100

    def test_multiple_decisions_take_max(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._decisions["1.2.3.4"] = [
            self._decision("crowdsecurity/http-probing"),   # 30
            self._decision("crowdsecurity/ssh-bf"),         # 60
        ]
        assert c.check_ip_score("1.2.3.4") == 60


class TestDecisionCounts:
    def test_local_decisions_exclude_capi(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._apply_stream({
            "new": [
                {"id": 1, "value": "1.1.1.1", "origin": "crowdsec", "scenario": "a"},
                {"id": 2, "value": "2.2.2.2", "origin": "CAPI", "scenario": "b"},
            ],
            "deleted": None,
        })
        assert c.active_decisions_count == 2
        assert c.local_decisions_count == 1

    def test_blocked_ips_list(self) -> None:
        c = CrowdSecClient(lapi_url="http://x", api_key="k")
        c._apply_stream({
            "new": [
                {"id": 1, "value": "1.1.1.1"},
                {"id": 2, "value": "2.2.2.2"},
            ],
            "deleted": None,
        })
        assert set(c.blocked_ips) == {"1.1.1.1", "2.2.2.2"}
