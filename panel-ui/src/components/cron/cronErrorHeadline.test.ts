import { describe, it, expect } from "vitest";

import { CRON_ERROR_HEADLINES, cronErrorHeadline } from "./cronErrorHeadline";

// The full cronvalidate ErrCode set the API can surface as `code`
// (internal/cronvalidate/cron.go + httptrigger.go). If cronvalidate gains a
// code, add it here AND to CRON_ERROR_HEADLINES — otherwise the new code falls
// through to the raw backend detail, which is exactly the GH #1686 item-5 bug.
const ALL_CODES = [
  // cron.go
  "empty",
  "too_long",
  "binary_not_allowed",
  "metachar_reject",
  "bad_path_arg",
  "invalid_name",
  "bad_schedule_syntax",
  "schedule_too_frequent",
  // httptrigger.go
  "http_not_curl_wget",
  "http_no_url",
  "http_multiple_urls",
  "http_bad_url",
  "http_bad_scheme",
  "http_userinfo",
  "http_foreign_host",
  "http_bad_port",
  "http_dangerous_flag",
  "http_unexpected_arg",
];

describe("CRON_ERROR_HEADLINES", () => {
  it("has a friendly headline for every cronvalidate code (incl. http_*)", () => {
    for (const code of ALL_CODES) {
      const headline = CRON_ERROR_HEADLINES[code];
      expect(headline, `missing headline for code ${code}`).toBeTruthy();
      // The headline must be a human sentence, not the raw code echoed back.
      expect(headline).not.toBe(code);
      expect(headline.length).toBeGreaterThan(code.length);
    }
  });

  it("does not carry stale codes cronvalidate never emits", () => {
    for (const code of Object.keys(CRON_ERROR_HEADLINES)) {
      expect(ALL_CODES, `unknown code ${code} in map`).toContain(code);
    }
  });
});

describe("cronErrorHeadline", () => {
  it("maps a code to its friendly headline and routes by API field", () => {
    const r = cronErrorHeadline({
      error: "validation_failed",
      field: "command",
      code: "binary_not_allowed",
      detail: 'first token must be "wp", got "ls"',
    });
    expect(r.headline).toBe(CRON_ERROR_HEADLINES.binary_not_allowed);
    expect(r.field).toBe("command");
  });

  it("routes http_* codes to the command field (they are command errors)", () => {
    const r = cronErrorHeadline({
      error: "validation_failed",
      field: "command",
      code: "http_foreign_host",
      detail: "host evil.example is not one of this account's domains",
    });
    expect(r.headline).toBe(CRON_ERROR_HEADLINES.http_foreign_host);
    expect(r.field).toBe("command");
  });

  it("routes schedule and name codes to their fields", () => {
    expect(
      cronErrorHeadline({ field: "schedule", code: "bad_schedule_syntax", detail: "x" }).field,
    ).toBe("schedule");
    expect(
      cronErrorHeadline({ field: "name", code: "invalid_name", detail: "x" }).field,
    ).toBe("name");
  });

  it("falls back to the backend detail for an unmapped code", () => {
    const r = cronErrorHeadline({ error: "validation_failed", field: "command", detail: "some new backend detail" });
    expect(r.headline).toBe("some new backend detail");
  });

  it("falls back to a generic message when nothing is provided", () => {
    expect(cronErrorHeadline(undefined).headline).toBe("Failed to save cron job");
    expect(cronErrorHeadline({}, "custom fallback").headline).toBe("custom fallback");
  });
});
