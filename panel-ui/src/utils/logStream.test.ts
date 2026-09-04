// logStream.test.ts — the JAB-296 "neutral Module tests" for the log-stream
// WebSocket newline handling, the ring-buffer cap, and the GoAccess URL
// derivation, now that they are pure and out of the .tsx component.
import { describe, it, expect } from "vitest";
import {
  MAX_LOG_LINES,
  capLogLines,
  stripTrailingNewline,
  buildGoAccessHttpUrl,
} from "./logStream";

describe("capLogLines", () => {
  it("leaves a below-cap buffer untouched", () => {
    const lines = ["a", "b", "c"];
    expect(capLogLines(lines)).toEqual(lines);
    expect(capLogLines([])).toEqual([]);
  });
  it("leaves an exactly-at-cap buffer untouched", () => {
    const lines = Array.from({ length: MAX_LOG_LINES }, (_, i) => String(i));
    expect(capLogLines(lines)).toHaveLength(MAX_LOG_LINES);
    expect(capLogLines(lines)[0]).toBe("0");
  });
  it("keeps only the newest MAX_LOG_LINES when over the cap, dropping the oldest", () => {
    const lines = Array.from({ length: MAX_LOG_LINES + 5 }, (_, i) => String(i));
    const capped = capLogLines(lines);
    expect(capped).toHaveLength(MAX_LOG_LINES);
    // Oldest five dropped; newest retained.
    expect(capped[0]).toBe("5");
    expect(capped[capped.length - 1]).toBe(String(MAX_LOG_LINES + 4));
  });
});

describe("stripTrailingNewline", () => {
  it.each([
    ["line\n", "line"],
    ["line\r\n", "line"],
    ["line\r", "line"],
    ["line", "line"],
    ["", ""],
    ["\n", ""],
    ["\r\n", ""],
  ])("%j → %j", (input, want) => {
    expect(stripTrailingNewline(input)).toBe(want);
  });
  it("trims only ONE trailing newline and keeps inner newlines", () => {
    expect(stripTrailingNewline("a\nb\n")).toBe("a\nb");
    expect(stripTrailingNewline("a\nb\n\n")).toBe("a\nb\n");
  });
});

describe("buildGoAccessHttpUrl", () => {
  it("maps ws:// to http:// and appends /goaccess.html", () => {
    expect(buildGoAccessHttpUrl("ws://host/api/v1/logs/stream/abc")).toBe(
      "http://host/api/v1/logs/stream/abc/goaccess.html",
    );
  });
  it("maps wss:// to https://", () => {
    expect(buildGoAccessHttpUrl("wss://host/api/v1/logs/stream/abc")).toBe(
      "https://host/api/v1/logs/stream/abc/goaccess.html",
    );
  });
  it("does not double-append when the path already ends in /goaccess.html", () => {
    expect(buildGoAccessHttpUrl("wss://host/logs/stream/abc/goaccess.html")).toBe(
      "https://host/logs/stream/abc/goaccess.html",
    );
  });
  it("trims trailing slashes before appending", () => {
    expect(buildGoAccessHttpUrl("ws://host/logs/stream/abc/")).toBe(
      "http://host/logs/stream/abc/goaccess.html",
    );
  });
  it("returns null on an unparseable stream URL", () => {
    expect(buildGoAccessHttpUrl("not a url")).toBeNull();
    expect(buildGoAccessHttpUrl("")).toBeNull();
  });
});
