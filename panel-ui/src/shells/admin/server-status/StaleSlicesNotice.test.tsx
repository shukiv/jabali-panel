// StaleSlicesNotice renders the JAB-373 AC#5 stale-serve badge only for slices
// the aggregator flagged `stale`, and stays invisible when every slice is fresh
// or when the older envelope omits `meta` entirely.
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StaleSlicesNotice } from "./StaleSlicesNotice";

describe("StaleSlicesNotice", () => {
  it("renders nothing when meta is absent (older envelope / demo redaction)", () => {
    const { container } = render(<StaleSlicesNotice />);
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing when every served slice is fresh", () => {
    const { container } = render(
      <StaleSlicesNotice
        meta={{
          cpu: { observed_at: "2026-09-23T00:00:00Z", stale: false },
          host: { observed_at: "2026-09-23T00:00:01Z" },
        }}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("names each stale slice and omits the fresh ones", () => {
    render(
      <StaleSlicesNotice
        meta={{
          cpu: { observed_at: "2026-09-23T00:00:00Z", stale: true, error: "timeout" },
          network: { observed_at: "2026-09-23T00:00:01Z", stale: true },
          host: { observed_at: "2026-09-23T00:00:02Z", stale: false },
        }}
      />,
    );
    expect(screen.getByTestId("stale-slice-cpu")).toBeInTheDocument();
    expect(screen.getByTestId("stale-slice-network")).toBeInTheDocument();
    expect(screen.queryByTestId("stale-slice-host")).not.toBeInTheDocument();
    // Uses the human label, not the raw slice key.
    expect(screen.getByText("CPU")).toBeInTheDocument();
    expect(screen.getByText("Network")).toBeInTheDocument();
  });
});
