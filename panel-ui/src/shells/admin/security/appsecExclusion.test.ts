// GH #1649 — the prefill a WAF block gives an exclusion must be something the
// server's ValidateExclusion accepts: no port in the host, no query string in
// the path, and a rule that actually scored.
import { describe, expect, it } from "vitest";

import type { AppSecBlockPattern } from "../../../hooks/useSecurityCrowdsec";
import {
  EXCLUSION_HOST_RE,
  hostForExclusion,
  pathForExclusion,
  prefillFromPattern,
} from "./appsecExclusion";

const pattern = (over: Partial<AppSecBlockPattern> = {}): AppSecBlockPattern => ({
  rule_ids: ["901340", "942100", "949110"],
  detections: ["942100", "932200"],
  other: [{ id: "2410974272", note: "" }],
  infra: [{ id: "901340", note: "" }],
  host: "Shop.Example.com:8443",
  uri: "/cart/add?id=1&q=' or 1=1#frag",
  count: 7,
  distinct_ips: 5,
  first_at: "2026-09-27T10:00:00Z",
  last_at: "2026-09-27T11:00:00Z",
  ...over,
});

describe("hostForExclusion", () => {
  it("lowercases and drops a port and a trailing dot", () => {
    expect(hostForExclusion(" Shop.Example.com:8443 ")).toBe("shop.example.com");
    expect(hostForExclusion("shop.example.com.")).toBe("shop.example.com");
  });

  it("leaves an IPv6 literal for the operator to replace", () => {
    const h = hostForExclusion("[2001:db8::1]:443");
    expect(EXCLUSION_HOST_RE.test(h)).toBe(false);
  });
});

describe("pathForExclusion", () => {
  it("drops the query string and fragment", () => {
    expect(pathForExclusion("/cart/add?id=1#x")).toBe("/cart/add");
    expect(pathForExclusion("/a#b?c")).toBe("/a");
  });

  it("always starts with a slash", () => {
    expect(pathForExclusion("")).toBe("/");
    expect(pathForExclusion("?x=1")).toBe("/");
    expect(pathForExclusion("wp-json/x")).toBe("/wp-json/x");
  });
});

describe("prefillFromPattern", () => {
  it("targets the first rule that scored, never an infrastructure rule", () => {
    const p = prefillFromPattern(pattern());
    expect(p.host).toBe("shop.example.com");
    expect(p.uri_prefix).toBe("/cart/add");
    expect(p.rule_id).toBe("942100");
    expect(p.ruleOptions).toEqual(["942100", "932200"]);
    expect(p.note).not.toMatch(/["\r\n]/);
  });

  it("leaves the rule empty when nothing scored", () => {
    const p = prefillFromPattern(pattern({ detections: [] }));
    expect(p.rule_id).toBe("");
    expect(p.ruleOptions).toEqual([]);
  });
});
