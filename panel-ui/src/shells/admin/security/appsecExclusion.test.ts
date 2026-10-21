// GH #1649 — the prefill a WAF block gives an exclusion must be something the
// server's ValidateExclusion accepts: no port in the host, no query string in
// the path, and a rule that actually scored.
import { describe, expect, it } from "vitest";

import type { AppSecBlockPattern } from "../../../hooks/useSecurityCrowdsec";
import {
  EXCLUSION_HOST_RE,
  EXCLUSION_RULE_RE,
  exclusionNoteForbidden,
  exclusionPathForbidden,
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

// The rules ValidateExclusion accepts: a CRS id, or a CrowdSec virtual patch or
// generic rule by name. All of CRS, or the base config, is a WAF off switch.
describe("EXCLUSION_RULE_RE", () => {
  it("accepts a CRS id and a virtual-patch or generic rule name", () => {
    for (const id of [
      "942100",
      "crowdsecurity/vpatch-CVE-2025-29927",
      "crowdsecurity/generic-wordpress-uploads-php",
    ]) {
      expect(EXCLUSION_RULE_RE.test(id), id).toBe(true);
    }
  });
  it("refuses everything else", () => {
    for (const id of [
      "0942100",
      "crowdsecurity/crs",
      "crowdsecurity/base-config",
      "crowdsecurity/vpatch-",
      "crowdsecurity/vpatch-x\")",
      "otherauthor/vpatch-x",
    ]) {
      expect(EXCLUSION_RULE_RE.test(id), id).toBe(false);
    }
  });
});

// What ValidateExclusion refuses in a path or a note, caught before the request.
describe("exclusionPathForbidden / exclusionNoteForbidden", () => {
  it("refuses quotes, line breaks (YAML's too) and control characters", () => {
    for (const bad of ['a"b', "a\nb", "a\rb", "a\tb", "a\u0085b", "a\u2028b", "a\u2029b", "a\u001bb"]) {
      expect(exclusionPathForbidden(bad), JSON.stringify(bad)).toBe(true);
      expect(exclusionNoteForbidden(bad), JSON.stringify(bad)).toBe(true);
    }
    expect(exclusionPathForbidden("a\\b")).toBe(true);
  });
  it("accepts ordinary text in any script", () => {
    for (const ok of ["/wp-json/x/", "/a: b #c/it's/", "אתר הלקוח café"]) {
      expect(exclusionPathForbidden(ok), ok).toBe(false);
      expect(exclusionNoteForbidden(ok), ok).toBe(false);
    }
  });
});
