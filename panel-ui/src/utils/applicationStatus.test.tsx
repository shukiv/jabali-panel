// applicationStatus.test.tsx — pins the shared Application Inventory status
// vocabulary (JAB-334) so the admin and tenant lists can't drift on which
// states exist, how they render, or which keep the list polling.
import { describe, it, expect } from "vitest";
import { isValidElement } from "react";
import {
  APPLICATION_STATUS_META,
  APPLICATION_TRANSITIONAL,
  isTransitionalStatus,
  anyTransitional,
  applicationStatusMeta,
  type ApplicationStatus,
} from "./applicationStatus";

const ALL_STATUSES: ApplicationStatus[] = [
  "pending",
  "installing",
  "cloning",
  "deleting",
  "ready",
  "failed",
];

describe("APPLICATION_STATUS_META", () => {
  it("covers every status with a color, label, and a real icon element", () => {
    for (const s of ALL_STATUSES) {
      const meta = APPLICATION_STATUS_META[s];
      expect(meta).toBeDefined();
      expect(typeof meta.color).toBe("string");
      expect(meta.label.length).toBeGreaterThan(0);
      expect(isValidElement(meta.icon)).toBe(true);
    }
  });
  it("labels the settled states green/red and the rest as work-in-progress", () => {
    expect(APPLICATION_STATUS_META.ready).toMatchObject({ color: "success", label: "Ready", spinning: false });
    expect(APPLICATION_STATUS_META.failed).toMatchObject({ color: "error", label: "Failed", spinning: false });
    expect(APPLICATION_STATUS_META.installing).toMatchObject({ color: "processing", spinning: true });
  });
});

describe("transitional rule", () => {
  it("marks exactly pending/installing/cloning/deleting as transitional", () => {
    expect([...APPLICATION_TRANSITIONAL].sort()).toEqual(
      ["cloning", "deleting", "installing", "pending"],
    );
    expect(isTransitionalStatus("pending")).toBe(true);
    expect(isTransitionalStatus("deleting")).toBe(true);
    expect(isTransitionalStatus("ready")).toBe(false);
    expect(isTransitionalStatus("failed")).toBe(false);
  });

  it("INVARIANT: spinning === transitional for every status", () => {
    for (const s of ALL_STATUSES) {
      expect(APPLICATION_STATUS_META[s].spinning).toBe(isTransitionalStatus(s));
    }
  });

  it("anyTransitional is true iff a row is still settling", () => {
    expect(anyTransitional([])).toBe(false);
    expect(anyTransitional([{ status: "ready" }, { status: "failed" }])).toBe(false);
    expect(anyTransitional([{ status: "ready" }, { status: "installing" }])).toBe(true);
  });
});

describe("applicationStatusMeta", () => {
  it("returns the status's own metadata", () => {
    expect(applicationStatusMeta("ready").label).toBe("Ready");
  });
  it("falls back to pending for an unknown status", () => {
    // Both lists used `STATUS_META[s] ?? STATUS_META.pending`; a status the
    // backend hasn't taught the frontend renders as Pending, not a crash.
    const unknown = "resizing" as ApplicationStatus;
    expect(applicationStatusMeta(unknown)).toBe(APPLICATION_STATUS_META.pending);
  });
});
