// buildApplicationActions.test.ts — AC4: role-specific actions stay explicit.
// The admin context (no clone) yields only login + delete; the tenant context
// adds the privileged cache + clone actions. Absence, not a flag, is what keeps
// the admin list from ever rendering a purge/warmup/clone.
import { describe, it, expect } from "vitest";
import {
  buildApplicationActions,
  type SharedActionCtx,
  type TenantActionCtx,
} from "./buildApplicationActions";

const noop = () => {};

const adminCtx: SharedActionCtx = {
  canLogin: true,
  onLogin: noop,
  loginLoading: false,
  onDelete: noop,
  deleting: false,
  deleteDescription: "admin desc",
};

const tenantCtx: TenantActionCtx = {
  ...adminCtx,
  deleteDescription: "tenant desc",
  cacheEnabled: true,
  onPurge: noop,
  purging: false,
  onWarmup: noop,
  warming: false,
  canClone: true,
  onClone: noop,
};

const keys = (ctx: SharedActionCtx | TenantActionCtx) =>
  buildApplicationActions(ctx).map((a) => a.key);

describe("buildApplicationActions", () => {
  it("admin (no clone context) gets only login + delete", () => {
    expect(keys(adminCtx)).toEqual(["login", "delete"]);
  });

  it("tenant gets login + cache + clone + delete", () => {
    expect(keys(tenantCtx)).toEqual(["login", "purge", "warmup", "clone", "delete"]);
  });

  it("hides login when the row cannot log in", () => {
    const actions = buildApplicationActions({ ...adminCtx, canLogin: false });
    expect(actions.find((a) => a.key === "login")?.hidden).toBe(true);
  });

  it("hides purge + warmup when caching is off", () => {
    const actions = buildApplicationActions({ ...tenantCtx, cacheEnabled: false });
    expect(actions.find((a) => a.key === "purge")?.hidden).toBe(true);
    expect(actions.find((a) => a.key === "warmup")?.hidden).toBe(true);
  });

  it("shows purge + warmup when caching is on", () => {
    const actions = buildApplicationActions(tenantCtx);
    expect(actions.find((a) => a.key === "purge")?.hidden).toBe(false);
    expect(actions.find((a) => a.key === "warmup")?.hidden).toBe(false);
  });

  it("disables clone when the install cannot be cloned", () => {
    const actions = buildApplicationActions({ ...tenantCtx, canClone: false });
    const clone = actions.find((a) => a.key === "clone");
    expect(clone?.disabled).toBe(true);
    expect(clone?.tooltip).toMatch(/only available for healthy WordPress/i);
  });

  it("passes the audience-specific delete copy through", () => {
    const admin = buildApplicationActions(adminCtx).find((a) => a.key === "delete");
    const tenant = buildApplicationActions(tenantCtx).find((a) => a.key === "delete");
    expect(admin?.confirm?.description).toBe("admin desc");
    expect(tenant?.confirm?.description).toBe("tenant desc");
  });
});
