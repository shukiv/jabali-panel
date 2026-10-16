import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { setPanelClock } from "../utils/datetime";
import { Timestamp } from "./Timestamp";

// GH #2164: a timestamp reads in the server's zone and the admin's format, and
// hovering it shows the exact UTC time.
afterEach(() => setPanelClock("UTC", undefined));

describe("Timestamp", () => {
  it("shows server time with the UTC instant on hover", () => {
    setPanelClock("Asia/Jerusalem", "DD/MM/YY HH:mm");
    const { container } = render(<Timestamp value="2026-10-16T03:36:12.5Z" />);
    const el = container.querySelector("time");
    expect(el?.textContent).toBe("16/10/26 06:36");
    expect(el?.getAttribute("dateTime")).toBe("2026-10-16T03:36:12Z");
    expect(el?.getAttribute("title")).toBe("2026-10-16T03:36:12Z (UTC)");
  });

  it("adds seconds or drops the time on request", () => {
    setPanelClock("UTC", "YYYY-MM-DD HH:mm");
    const { container, rerender } = render(<Timestamp value="2026-10-16T06:36:12Z" seconds />);
    expect(container.textContent).toBe("2026-10-16 06:36:12");
    rerender(<Timestamp value="2026-10-16T06:36:12Z" dateOnly />);
    expect(container.textContent).toBe("2026-10-16");
  });

  it("re-renders when the admin changes the format", () => {
    setPanelClock("UTC", "DD/MM/YY HH:mm");
    const { container } = render(<Timestamp value="2026-10-16T06:36:00Z" />);
    expect(container.textContent).toBe("16/10/26 06:36");
    act(() => setPanelClock("UTC", "MM/DD/YYYY hh:mm A"));
    expect(container.textContent).toBe("10/16/2026 06:36 AM");
  });

  it("reads left to right in a right-to-left page", () => {
    document.documentElement.dir = "rtl";
    try {
      const { container } = render(<Timestamp value="2026-10-16T06:36:00Z" />);
      const el = container.querySelector("time");
      expect(el?.getAttribute("dir")).toBe("ltr");
      // dir="ltr" isolates it; no isolate characters in the text.
      expect(el?.textContent).toBe("16/10/26 06:36");
    } finally {
      document.documentElement.dir = "";
    }
  });

  it("renders a dash and no <time> for no value", () => {
    const { container } = render(<Timestamp value={null} />);
    expect(container.textContent).toBe("—");
    expect(container.querySelector("time")).toBeNull();
  });
});
