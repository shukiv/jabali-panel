// @vitest-environment-options {"settings":{"disableIframePageLoading":true}}
//
// The GoAccess report is framed without allow-same-origin, so it runs in an
// opaque origin and the panel cannot read its scroll position. The report posts
// it out (panel-api appends that script); the modal keeps the latest and sends
// it back after each 10s refresh.
import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { LogStreamModal } from "./LogStreamModal";

const STREAM = "wss://panel.example:8443/api/v1/logs/stream/0123456789abcdef0123456789abcdef";

const openReport = () => {
  render(
    <LogStreamModal visible onClose={() => {}} streamUrl={STREAM} title="GoAccess" logType="goaccess" />,
  );
  const frame = document.querySelector<HTMLIFrameElement>("iframe");
  if (!frame) throw new Error("no GoAccess iframe");
  // happy-dom loads no frame here (disableIframePageLoading), so stand in for
  // the report's window.
  const post = vi.fn();
  const win = { postMessage: post } as unknown as Window;
  Object.defineProperty(frame, "contentWindow", { value: win });
  return { frame, win, post };
};

const fromFrame = (source: unknown, data: unknown) =>
  window.dispatchEvent(new MessageEvent("message", { data, source: source as MessageEventSource | null }));

describe("LogStreamModal GoAccess report", () => {
  it("frames the report with scripts but without the panel's origin", () => {
    const { frame } = openReport();
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
  });

  it("sends the report's last scroll position back after a refresh", () => {
    const { frame, win, post } = openReport();

    fromFrame(win, { type: "jabali-goaccess-scroll", top: 200, left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: 420, left: 15 });
    fireEvent.load(frame);

    expect(post).toHaveBeenCalledTimes(1);
    expect(post).toHaveBeenCalledWith({ type: "jabali-goaccess-restore", top: 420, left: 15 }, "*");
  });

  it("sends nothing while the report is at the top", () => {
    const { frame, post } = openReport();
    fireEvent.load(frame);
    expect(post).not.toHaveBeenCalled();
  });

  it("ignores messages that are not the report's scroll position", () => {
    const { frame, win, post } = openReport();

    fromFrame(window, { type: "jabali-goaccess-scroll", top: 300, left: 0 });
    fromFrame(null, { type: "jabali-goaccess-scroll", top: 300, left: 0 });
    fromFrame(win, { type: "other", top: 300, left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: "300", left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: -5, left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: Number.NaN, left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: Number.POSITIVE_INFINITY, left: 0 });
    fromFrame(win, { type: "jabali-goaccess-scroll", top: 300 });
    fromFrame(win, "jabali-goaccess-scroll");
    fromFrame(win, null);
    fireEvent.load(frame);

    expect(post).not.toHaveBeenCalled();
  });
});
