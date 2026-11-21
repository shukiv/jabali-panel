// ReverseProxyPortControl.test — GH #2544: change a reverse-proxy domain's
// port after create, and show the server's reason when it refuses a port.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const put = vi.hoisted(() => vi.fn());
vi.mock("../../apiClient", () => ({ apiClient: { put } }));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { ReverseProxyPortControl } from "./ReverseProxyPortControl";

function renderControl() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ReverseProxyPortControl domainId="d1" domainName="app.example" port={3000} />
    </QueryClientProvider>,
  );
}

function setPort(value: string) {
  fireEvent.click(screen.getByRole("button", { name: "Change port" }));
  const input = screen.getByLabelText("Port");
  fireEvent.change(input, { target: { value } });
  fireEvent.blur(input);
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
}

beforeEach(() => put.mockReset());

describe("ReverseProxyPortControl (GH #2544)", () => {
  it("shows the current port", () => {
    renderControl();
    expect(screen.getByText("Port 3000")).toBeInTheDocument();
  });

  it("saves a new port to the domain's reverse-proxy-port endpoint", async () => {
    put.mockResolvedValue({});
    renderControl();
    setPort("5000");
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith("/domains/d1/reverse-proxy-port", { port: 5000 }),
    );
  });

  it("shows the server's reason when the port is refused", async () => {
    put.mockRejectedValueOnce({
      response: { data: { error: "reverse_proxy_port_in_use", message: "that port is already assigned to another domain — choose another" } },
    });
    renderControl();
    setPort("5000");
    expect(await screen.findByText(/already assigned to another domain/)).toBeInTheDocument();
  });

  it("saving the same port sends nothing", async () => {
    renderControl();
    setPort("3000");
    await new Promise((r) => setTimeout(r, 50));
    expect(put).not.toHaveBeenCalled();
  });
});
