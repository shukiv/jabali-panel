import { describe, expect, it } from "vitest";

import { mailHostedElsewhere } from "./types";

// Same cases as models.MailHostedElsewhere on the server.
describe("mailHostedElsewhere", () => {
  it("is false for a domain whose mail Jabali hosts", () => {
    expect(mailHostedElsewhere({ email_enabled: true, mail_provider: "jabali" })).toBe(false);
    expect(mailHostedElsewhere({ email_enabled: true, mail_provider: "" })).toBe(false);
    expect(mailHostedElsewhere({ email_enabled: true })).toBe(false);
  });

  it("is true when mail is off", () => {
    expect(mailHostedElsewhere({ email_enabled: false, mail_provider: "jabali" })).toBe(true);
    expect(mailHostedElsewhere({ email_enabled: false, mail_provider: "none" })).toBe(true);
    expect(mailHostedElsewhere({})).toBe(true);
  });

  it("is true when another provider hosts the mail", () => {
    expect(mailHostedElsewhere({ email_enabled: true, mail_provider: "google" })).toBe(true);
    expect(mailHostedElsewhere({ email_enabled: true, mail_provider: "m365" })).toBe(true);
    expect(mailHostedElsewhere({ email_enabled: true, mail_provider: "custom" })).toBe(true);
  });
});
