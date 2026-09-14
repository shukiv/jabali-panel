// DNSOrphanSweepCard — GH #1620. The admin DNS tab toggle for the automatic
// PowerDNS orphan sweep. These lock the wire contract: the card reflects the
// server's dns_orphan_autosweep_enabled on load and PATCHes exactly that field
// when toggled, so it can't silently drift from the backend setting.
import { render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn() },
}));

const successToast = vi.hoisted(() => vi.fn());
vi.mock("../../../lib/feedback", () => ({
  feedback: {
    message: { success: successToast, error: vi.fn(), warning: vi.fn() },
  },
}));

import { apiClient } from "../../../apiClient";
import { DNSOrphanSweepCard } from "./DNSOrphanSweepCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.clearAllMocks();
});

describe("DNSOrphanSweepCard", () => {
  it("reflects dns_orphan_autosweep_enabled from GET /admin/settings", async () => {
    mockGet.mockResolvedValue({ data: { dns_orphan_autosweep_enabled: true } });
    render(<DNSOrphanSweepCard />);

    await waitFor(() => {
      expect(screen.getByRole("switch")).toBeChecked();
    });
  });

  it("PATCHes dns_orphan_autosweep_enabled when toggled on", async () => {
    mockGet.mockResolvedValue({ data: { dns_orphan_autosweep_enabled: false } });
    mockPatch.mockResolvedValue({ data: {} });
    render(<DNSOrphanSweepCard />);

    const toggle = await screen.findByRole("switch");
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => expect(mockPatch).toHaveBeenCalledTimes(1));
    expect(mockPatch).toHaveBeenCalledWith("/admin/settings", { dns_orphan_autosweep_enabled: true });
    await waitFor(() => expect(successToast).toHaveBeenCalledWith("DNS orphan sweep enabled"));
  });
});
