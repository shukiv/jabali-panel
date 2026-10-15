import { describe, expect, it } from "vitest";
import { versionsForFramework } from "./usePythonApps";

// GH #2156: a framework install offers only the runtimes at or above the
// entry's python_min (sic's Django 6 needs 3.12).
describe("versionsForFramework", () => {
  const installed = ["3.9", "3.11", "3.12", "3.13"];

  it("drops runtimes below python_min, comparing the minor as a number", () => {
    expect(versionsForFramework(installed, "3.12")).toEqual(["3.12", "3.13"]);
    expect(versionsForFramework(installed, "3.10")).toEqual(["3.11", "3.12", "3.13"]);
  });

  it("keeps every runtime when there is no python_min", () => {
    expect(versionsForFramework(installed, undefined)).toEqual(installed);
    expect(versionsForFramework(installed, "")).toEqual(installed);
  });

  it("returns none when nothing installed is new enough", () => {
    expect(versionsForFramework(["3.11"], "3.12")).toEqual([]);
  });
});
