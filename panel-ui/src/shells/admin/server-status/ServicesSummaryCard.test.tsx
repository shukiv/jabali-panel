// ServicesSummaryCard — an inactive on-demand/disabled unit (e.g. jabali-webmail,
// which the reconciler starts only once a domain enables email) must render as a
// neutral "idle", not a red "inactive" that reads like a failure. A unit that IS
// configured to run but is inactive stays red.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { post: vi.fn() } }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), info: vi.fn(), error: vi.fn() } },
}));
vi.mock("../../../hooks/useServerCapabilities", () => ({
  useServerCapabilities: () => ({ data: { postgres_enabled: true, docker_marketplace_enabled: true } }),
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { ServicesSummaryCard } from "./ServicesSummaryCard";
import type { ServiceDetail } from "../../../hooks/useServerStatus";

const svc = (o: Partial<ServiceDetail>): ServiceDetail =>
  ({ unit: "x.service", active: "active", load_state: "loaded", sub_state: "", unit_file_state: "enabled", ...o }) as ServiceDetail;

function renderCard(services: ServiceDetail[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ServicesSummaryCard services={services} />
    </QueryClientProvider>,
  );
}

describe("ServicesSummaryCard status", () => {
  it("shows on-demand inactive (disabled) as idle, not inactive", () => {
    renderCard([svc({ unit: "jabali-webmail.service", active: "inactive", unit_file_state: "disabled" })]);
    expect(screen.getByText("idle")).toBeInTheDocument();
    expect(screen.queryByText("inactive")).toBeNull();
  });

  it("shows a run-configured but inactive unit as inactive (red)", () => {
    renderCard([svc({ unit: "nginx.service", active: "inactive", unit_file_state: "enabled" })]);
    expect(screen.getByText("inactive")).toBeInTheDocument();
    expect(screen.queryByText("idle")).toBeNull();
  });

  it("shows an active unit as active", () => {
    renderCard([svc({ unit: "stalwart.service", active: "active", unit_file_state: "enabled" })]);
    expect(screen.getByText("active")).toBeInTheDocument();
  });
});

// GH #1992: restarting nginx (or the panel, agent or redis) from the panel cut
// off its own request. The API now schedules those restarts a moment out and
// answers {scheduled: true}; the card must say the panel will drop for a few
// seconds instead of a bare "Done".
describe("ServicesSummaryCard restart of a unit the panel runs through", () => {
  it("warns before, and says the panel may be unreachable after, a scheduled restart", async () => {
    vi.mocked(apiClient.post).mockResolvedValueOnce({ data: { name: "nginx", scheduled: true } });
    renderCard([svc({ unit: "nginx.service" })]);

    fireEvent.click(screen.getByRole("button", { name: /Restart/ }));
    expect(await screen.findByText(/panel will be unreachable for a few seconds/i)).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Restart" }));

    await waitFor(() => expect(apiClient.post).toHaveBeenCalledWith("/admin/services/nginx/restart"));
    await waitFor(() =>
      expect(feedback.message.info).toHaveBeenCalledWith(expect.stringMatching(/unreachable for a few seconds/i)),
    );
    expect(feedback.message.success).not.toHaveBeenCalled();
  });

  it("keeps the plain confirm and Done for any other restart", async () => {
    vi.mocked(apiClient.post).mockResolvedValueOnce({ data: { name: "mariadb", active: "active" } });
    renderCard([svc({ unit: "mariadb.service" })]);

    fireEvent.click(screen.getByRole("button", { name: /Restart/ }));
    expect(await screen.findByText("Restart causes a brief drop in service. Continue?")).toBeInTheDocument();
  });
});
