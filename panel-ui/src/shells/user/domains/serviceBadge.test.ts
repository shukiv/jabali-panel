import { describe, it, expect } from "vitest";
import { serviceBadge } from "../../../components/domains/types";

// GH #1449: the list badge distinguishes the three row kinds and stays silent
// for an ordinary full-service web domain.
describe("serviceBadge", () => {
  it("no badge for a plain web domain", () => {
    expect(serviceBadge({ web_disabled: false, dns_disabled: false, email_enabled: true })).toBeNull();
  });

  it("DNS only when web off and no mail", () => {
    expect(serviceBadge({ web_disabled: true, email_enabled: false })?.label).toBe("DNS only");
  });

  it("Mail only when web off and mail on", () => {
    expect(serviceBadge({ web_disabled: true, email_enabled: true })?.label).toBe("Mail only");
  });

  it("External DNS when a web domain opts out of hosted DNS", () => {
    expect(serviceBadge({ web_disabled: false, dns_disabled: true })?.label).toBe("External DNS");
  });
});
