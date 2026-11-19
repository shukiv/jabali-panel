import { describe, expect, it } from "vitest";
import { domain } from "./validation";

// IDN TLDs travel in punycode (.рф = xn--p1ai, .ישראל = xn--4dbrk0ce). The
// domain rule allowed letters only in the TLD, so a domain under one could not
// be entered.
describe("domain rule", () => {
  const re = (domain as { pattern: RegExp }).pattern;

  it.each(["example.com", "a.b.example.co.uk", "example.xn--p1ai", "xn----7sbbhn.xn--4dbrk0ce", "example.XN--P1AI"])(
    "accepts %s",
    (name) => {
      expect(re.test(name)).toBe(true);
    },
  );

  it.each(["localhost", "example.123", "example.xn--", "example.xn-p1ai", "ex_ample.com"])("rejects %s", (name) => {
    expect(re.test(name)).toBe(false);
  });
});
