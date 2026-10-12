// AdminSecurityAppArmor.acknowledge.test.tsx — GH #2078. A profile kept in
// complain mode on purpose can be acknowledged, so the Server Status alert
// leaves it out until its mode changes. The mode stays visible on this page.
// apiClient and the toast are mocked; the real AntD table renders.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminSecurityAppArmor } from "./AdminSecurityAppArmor";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  delete: ReturnType<typeof vi.fn>;
};
const toastError = feedback.message.error as unknown as ReturnType<typeof vi.fn>;

const serve = (acknowledged: string[] | undefined) =>
  mocked.get.mockResolvedValue({
    data: {
      enabled: true,
      profiles: [
        { name: "jabali-fpm-app", mode: "complain" },
        { name: "jabali-sendmail", mode: "complain" },
        { name: "jabali-panel", mode: "enforce" },
      ],
      denials: [],
      violations: [],
      ...(acknowledged ? { acknowledged } : {}),
    },
  });

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <App>
        <AdminSecurityAppArmor />
      </App>
    </QueryClientProvider>,
  );
}

const row = async (name: string) => (await screen.findByText(name)).closest("tr") as HTMLElement;

describe("AppArmor complain-mode acknowledgement (GH #2078)", () => {
  beforeEach(() => {
    mocked.get.mockReset();
    mocked.post.mockReset().mockResolvedValue({ data: {} });
    mocked.delete.mockReset().mockResolvedValue({ data: {} });
    toastError.mockReset();
  });

  it("acknowledges a complain profile", async () => {
    serve([]);
    renderPage();
    fireEvent.click(within(await row("jabali-fpm-app")).getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith("/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge"));
    expect(mocked.delete).not.toHaveBeenCalled();
  });

  it("shows an acknowledged profile's mode and tag, and withdraws the acknowledgement", async () => {
    serve(["jabali-fpm-app"]);
    renderPage();
    const fpm = await row("jabali-fpm-app");
    expect(within(fpm).getByText("complain")).toBeInTheDocument();
    expect(within(fpm).getByText("acknowledged")).toBeInTheDocument();
    expect(within(await row("jabali-sendmail")).queryByText("acknowledged")).toBeNull();
    fireEvent.click(within(fpm).getByRole("button", { name: "Show warning again" }));
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith("/admin/security/apparmor/profiles/jabali-fpm-app/acknowledge"));
  });

  it("offers no acknowledgement for an enforcing profile", async () => {
    serve([]);
    renderPage();
    expect(within(await row("jabali-panel")).queryByRole("button", { name: "Acknowledge" })).toBeNull();
  });

  it("offers none on a panel that doesn't report acknowledgements", async () => {
    serve(undefined);
    renderPage();
    expect(within(await row("jabali-fpm-app")).queryByRole("button", { name: "Acknowledge" })).toBeNull();
  });

  it("shows the panel's reason when the acknowledgement is refused", async () => {
    serve([]);
    mocked.post.mockRejectedValue({
      response: { status: 422, data: { error: "not_in_complain_mode", detail: "Only a profile in complain mode can be acknowledged; jabali-fpm-app is enforce." } },
    });
    renderPage();
    fireEvent.click(within(await row("jabali-fpm-app")).getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(String(toastError.mock.calls[0][0])).toContain("jabali-fpm-app is enforce");
  });
});
