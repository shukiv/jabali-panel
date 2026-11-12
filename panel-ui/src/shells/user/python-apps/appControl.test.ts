import { describe, expect, it } from "vitest";
import { controlErrorMessage, controlSuccessMessage } from "./usePythonApps";

// The app controls' toasts. Rebuild builds the app again on the next pass and
// restarts it when the build is done, so its success says when, not "done";
// a rebuild while a build runs is a 409 build_running, which says to wait.

describe("controlSuccessMessage", () => {
  it("says when a rebuild happens", () => {
    expect(controlSuccessMessage("rebuild")).toMatch(/within a minute/);
    expect(controlSuccessMessage("rebuild")).toMatch(/restarts/);
  });
  it("keeps the other actions short", () => {
    expect(controlSuccessMessage("restart")).toBe("restart requested");
  });
});

describe("controlErrorMessage", () => {
  it("tells the owner to wait for a running build", () => {
    const err = { response: { status: 409, data: { error: "build_running", detail: "a build of this app is already running" } }, message: "Request failed with status code 409" };
    expect(controlErrorMessage("rebuild", err)).toMatch(/already building/i);
  });
  it("shows the server's detail, not axios' status line", () => {
    const err = { response: { status: 400, data: { error: "invalid_action", detail: "action must be start, stop, restart or rebuild" } }, message: "Request failed with status code 400" };
    expect(controlErrorMessage("reboot", err)).toBe("reboot failed: action must be start, stop, restart or rebuild");
  });
  it("falls back to the error's message", () => {
    expect(controlErrorMessage("stop", new Error("Network Error"))).toBe("stop failed: Network Error");
    expect(controlErrorMessage("stop", undefined)).toBe("stop failed");
  });
});
