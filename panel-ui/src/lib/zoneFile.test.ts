import { describe, expect, it } from "vitest";
import { quoteTXT, zoneFileLine } from "./zoneFile";

describe("quoteTXT", () => {
  it("quotes a short value as one string", () => {
    expect(quoteTXT("v=spf1 mx ~all")).toBe('"v=spf1 mx ~all"');
    expect(quoteTXT("")).toBe('""');
  });

  it("splits a value past 255 characters into strings of at most 255 (GH #2475)", () => {
    const rsa = "v=DKIM1; k=rsa; p=" + "A".repeat(392);
    expect(quoteTXT(rsa)).toBe(`"${rsa.slice(0, 255)}" "${rsa.slice(255)}"`);
    expect(quoteTXT("a".repeat(255))).toBe(`"${"a".repeat(255)}"`);
  });

  it("escapes quotes and backslashes", () => {
    expect(quoteTXT('say "hi" \\ bye')).toBe('"say \\"hi\\" \\\\ bye"');
  });
});

describe("zoneFileLine", () => {
  it("renders the RSA DKIM hint as a multi-string TXT line", () => {
    const value = "v=DKIM1; k=rsa; p=" + "B".repeat(392);
    const line = zoneFileLine({ purpose: "DKIM (RSA)", name: "jabalirsa._domainkey.example.com.", type: "TXT", value });
    expect(line).toBe(`jabalirsa._domainkey.example.com.\tIN\tTXT\t"${value.slice(0, 255)}" "${value.slice(255)}"`);
  });

  it("leaves non-TXT values as they are", () => {
    expect(zoneFileLine({ purpose: "MX", name: "example.com.", type: "MX", value: "10 mail.example.com." })).toBe(
      "example.com.\tIN\tMX\t10 mail.example.com.",
    );
  });
});
