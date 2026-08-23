import { describe, expect, it } from "vitest";

import { userLabel } from "./userLabel";

describe("userLabel", () => {
  it("leads with the username and appends the display name", () => {
    expect(
      userLabel({ username: "dana", email: "dana@lior.co.il", name_first: "Dana Levi" }),
    ).toBe("dana (Dana Levi)");
  });

  it("joins legacy split names", () => {
    expect(
      userLabel({ username: "dana", email: "dana@lior.co.il", name_first: "Dana", name_last: "Levi" }),
    ).toBe("dana (Dana Levi)");
  });

  it("returns the bare username when no name is set", () => {
    expect(userLabel({ username: "dana", email: "dana@lior.co.il" })).toBe("dana");
  });

  it("falls back to email for accounts without a username (admins)", () => {
    expect(userLabel({ username: null, email: "root@acme.dev", name_first: "" })).toBe("root@acme.dev");
  });
});
