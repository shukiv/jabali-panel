// channelPolicy.test — pins the explicit audience policy (JAB-336). With a single
// shared Module, "both surfaces render the same fields" is tautological; the teeth
// are in the policy deltas each adapter supplies. These assert the resource paths
// (AC2), the admin-only owner column (AC4), and the tenant allowed-kind policy
// (AC5), including the JAB-326 widening + the deliberate webpush asymmetry.
import { describe, expect, it } from "vitest";

import { CHANNEL_KINDS } from "../../utils/channelKindConfig";
import { ADMIN_CHANNEL_POLICY, TENANT_KINDS, tenantChannelPolicy } from "./channelPolicy";

describe("channel policy (JAB-336)", () => {
  it("admin targets the admin resource, shows the owner column, full SMTP range (AC2, AC4)", () => {
    expect(ADMIN_CHANNEL_POLICY.resourcePath).toBe("admin/notifications/channels");
    expect(ADMIN_CHANNEL_POLICY.showOwnerColumn).toBe(true);
    expect(ADMIN_CHANNEL_POLICY.smtpPortFullRange).toBe(true);
    expect(ADMIN_CHANNEL_POLICY.forceOwnEmail).toBe(false);
    expect(ADMIN_CHANNEL_POLICY.formId).toBe("channel-form");
  });

  it("tenant targets the me resource, hides the owner column, forces own email (AC2, AC4, AC5)", () => {
    const p = tenantChannelPolicy(TENANT_KINDS);
    expect(p.resourcePath).toBe("me/notifications/channels");
    expect(p.showOwnerColumn).toBe(false);
    expect(p.smtpPortFullRange).toBe(false);
    expect(p.forceOwnEmail).toBe(true);
    expect(p.emailNote).toBeDefined();
    expect(p.formId).toBe("my-channel-form");
  });

  it("admin creatable kinds are the full admin set and exclude webpush (AC5 asymmetry pinned)", () => {
    expect(ADMIN_CHANNEL_POLICY.allowedKinds).toEqual(CHANNEL_KINDS);
    expect(ADMIN_CHANNEL_POLICY.allowedKinds).not.toContain("webpush");
  });

  it("tenant defaults to the safe kinds and never the risky ones (AC5)", () => {
    const p = tenantChannelPolicy();
    expect(p.allowedKinds).toEqual(TENANT_KINDS);
    expect(p.allowedKinds).toContain("webpush");
    for (const risky of ["email", "slack", "sms", "webhook"] as const) {
      expect(p.allowedKinds).not.toContain(risky);
    }
  });

  it("tenant honours an admin-widened allowlist and defaults to its first kind (JAB-326, AC5)", () => {
    const p = tenantChannelPolicy(["ntfy", "webhook", "email"]);
    expect(p.allowedKinds).toEqual(["ntfy", "webhook", "email"]);
    expect(p.defaultKind).toBe("ntfy");
  });

  it("tenant falls back to the safe kinds when the allowlist is empty (AC5)", () => {
    const p = tenantChannelPolicy([]);
    expect(p.allowedKinds).toEqual(TENANT_KINDS);
    expect(p.defaultKind).toBe("ntfy");
  });

  it("test-result copy: admin distinguishes delivered vs queued; tenant is fixed", () => {
    expect(ADMIN_CHANNEL_POLICY.testResult("Ops", true)).toMatch(/delivered/i);
    expect(ADMIN_CHANNEL_POLICY.testResult("Ops", false)).toMatch(/queued/i);
    expect(tenantChannelPolicy().testResult("Phone", undefined)).toMatch(/sent/i);
  });
});
