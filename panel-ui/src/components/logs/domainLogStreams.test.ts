// domainLogStreams.test.ts — neutral Module tests for the request-shaping and
// vocabulary of the per-domain log viewer (JAB-296).
import { describe, it, expect } from "vitest";
import {
  buildLogStreamPayload,
  isAggregateRow,
  ALL_DOMAINS_ROW,
  LOG_STREAM_TITLES,
  LOG_STREAM_LABELS,
} from "./domainLogStreams";

describe("buildLogStreamPayload", () => {
  it("includes domain_id for a per-domain request (admin row or any tenant request)", () => {
    // AC1: the identified request carries the domain.
    const payload = buildLogStreamPayload("access", "d1");
    expect(payload).toEqual({ log_type: "access", domain_id: "d1" });
    expect(payload).toHaveProperty("domain_id", "d1");
  });

  it("omits domain_id for an aggregate request (no domainId at all)", () => {
    // AC1: the aggregate request has no domain identity.
    const payload = buildLogStreamPayload("error");
    expect(payload).toEqual({ log_type: "error" });
    expect(payload).not.toHaveProperty("domain_id");
  });

  it("treats an empty-string id as a domain request, NOT an aggregate (AC2)", () => {
    // The aggregate is the ABSENCE of a domainId, not a falsy one. A caller
    // that passes "" still gets domain_id in the body, so a tenant column ctx
    // (which always calls with a row id) can never emit an aggregate request.
    const payload = buildLogStreamPayload("access", "");
    expect(payload).toEqual({ log_type: "access", domain_id: "" });
    expect(payload).toHaveProperty("domain_id");
  });
});

describe("isAggregateRow", () => {
  it("is true only for the synthetic aggregate row", () => {
    expect(isAggregateRow(ALL_DOMAINS_ROW)).toBe(true);
    expect(isAggregateRow({ id: "d1", name: "example.com", status: "active" })).toBe(false);
    // An empty id alone does not make a row aggregate — the discriminant is
    // the explicit flag, not the id.
    expect(isAggregateRow({ id: "", name: "x", status: "active" })).toBe(false);
  });
});

describe("copy (AC6)", () => {
  it("pins the modal titles", () => {
    expect(LOG_STREAM_TITLES).toEqual({
      access: "Access Log Stream",
      error: "Error Log Stream",
      goaccess: "GoAccess Real-Time Dashboard",
    });
  });
  it("pins the action labels", () => {
    expect(LOG_STREAM_LABELS).toEqual({
      access: "Access Log",
      error: "Error Log",
      goaccess: "Real Time",
    });
  });
});
