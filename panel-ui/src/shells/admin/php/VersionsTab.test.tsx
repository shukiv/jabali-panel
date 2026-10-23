// GH #2242: the PHP Manager warning had an X, but closing it lasted only until
// the next page load. Closed must stay closed in this browser.
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { VersionsTab } from "./VersionsTab";

vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: vi.fn().mockResolvedValue({ data: { default_version: "8.3", versions: [] } }),
  },
}));

const WARNING = "Modifying PHP versions can cause server downtime";

describe("VersionsTab warning (GH #2242)", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("shows the warning in full on the first visit", async () => {
    render(<VersionsTab />);
    expect(await screen.findByText(WARNING)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /hide/i })).toBeInTheDocument();
  });

  it("stays closed after the page is loaded again", async () => {
    const first = render(<VersionsTab />);
    await screen.findByText(WARNING);
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(screen.queryByText(WARNING)).not.toBeInTheDocument();
    first.unmount();

    render(<VersionsTab />);
    await screen.findByRole("table");
    expect(screen.queryByText(WARNING)).not.toBeInTheDocument();
  });
});
