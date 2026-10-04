// GH #2001: what an allowed PHP command-execution function can start, from
// the AppArmor status the admin Security page reads.
import { describe, expect, it } from "vitest";
import { fpmExecConfinement, type AppArmorStatus } from "./useSecurityAppArmor";

const status = (profiles: AppArmorStatus["profiles"], enabled = true): AppArmorStatus => ({
  enabled,
  profiles,
  denials: [],
  violations: [],
});

describe("fpmExecConfinement", () => {
  it("maps the jabali-fpm-app mode", () => {
    expect(fpmExecConfinement(status([{ name: "jabali-fpm-app", mode: "enforce" }]))).toBe("enforce");
    expect(fpmExecConfinement(status([{ name: "jabali-fpm-app", mode: "complain" }]))).toBe("complain");
    expect(fpmExecConfinement(status([{ name: "jabali-fpm-app", mode: "missing" }]))).toBe("none");
    expect(fpmExecConfinement(status([{ name: "jabali-fpm-app", mode: "kernel-gated" }]))).toBe("none");
    // Another profile's mode says nothing about PHP.
    expect(fpmExecConfinement(status([{ name: "jabali-agent", mode: "enforce" }]))).toBe("none");
    expect(fpmExecConfinement(status([], false))).toBe("none");
  });

  it("is unknown until a well-formed status arrives", () => {
    expect(fpmExecConfinement(undefined)).toBeUndefined();
    expect(fpmExecConfinement({} as AppArmorStatus)).toBeUndefined();
  });
});
