// PanelSSLRestart.test.tsx — the Panel SSL views ride out a panel restart.
// Issuing the panel's Let's Encrypt cert, or regenerating its self-signed one,
// restarts jabali-panel to serve it. For those seconds nginx answers 502, and
// the tab's refetch used to turn a successful issue into "Failed to load
// panel SSL state — Request failed with status code 502".
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { PanelSSLCard } from "../../shells/admin/settings/PanelSSLCard";
import { setPanelClock } from "../../utils/datetime";
import { PanelSSLTab } from "./PanelSSLTab";
import { panelCertIssueMessage } from "./panelCertStatus";

const get = (apiClient as unknown as { get: ReturnType<typeof vi.fn> }).get;

const LOAD_FAILED = "Failed to load panel SSL state";

function hostCert() {
  return {
    kind: "hostname",
    id: 1,
    hostname: "panel.example.com",
    status: "issued",
    cert_pem_path: "/etc/jabali/tls/panel.crt",
    attempt_count: 0,
    staging: false,
    use_le: true,
    updated_at: "2026-11-17T02:10:00Z",
    routable: true,
  };
}

const OK = { status: 200, data: { certs: [hostCert()] } };

// The error apiClient's interceptor hands on: an Error carrying axios's fields.
function httpError(status: number) {
  return Object.assign(new Error(`Request failed with status code ${status}`), {
    isAxiosError: true,
    response: { status, data: "<html>Bad Gateway</html>" },
  });
}
function networkError() {
  return Object.assign(new Error("Network Error"), { isAxiosError: true, code: "ERR_NETWORK" });
}

function renderWith(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <App>{node}</App>
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return qc;
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers({ shouldAdvanceTime: true });
  setPanelClock("UTC", "DD/MM/YY HH:mm");
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("SSL Manager → Panel SSL during a panel restart", () => {
  it("keeps the cards through the 502s and loads again when the panel is back", async () => {
    get.mockResolvedValueOnce(OK);
    renderWith(<PanelSSLTab />);
    await screen.findByText("panel.example.com", { selector: ".ant-card-head code" });

    get.mockRejectedValueOnce(httpError(502)).mockRejectedValueOnce(httpError(502)).mockRejectedValueOnce(httpError(502));
    get.mockResolvedValue(OK);
    fireEvent.click(screen.getByRole("button", { name: /refresh/i }));

    for (let i = 0; i < 4; i++) {
      await advance(2000);
      expect(screen.queryByText(LOAD_FAILED)).toBeNull();
      expect(screen.getByText("panel.example.com", { selector: ".ant-card-head code" })).toBeTruthy();
    }
    expect(get).toHaveBeenCalledTimes(5);
    expect(screen.queryByText(/Couldn't refresh/)).toBeNull();
  });

  it("retries connections dropped while nginx reloads", async () => {
    // Two in a row: the ordinary single retry would give up on the second.
    get.mockRejectedValueOnce(networkError()).mockRejectedValueOnce(networkError()).mockResolvedValue(OK);
    renderWith(<PanelSSLTab />);
    await advance(2000);
    expect(screen.queryByText(LOAD_FAILED)).toBeNull();
    await advance(2000);
    expect(await screen.findByText("panel.example.com", { selector: ".ant-card-head code" })).toBeTruthy();
    expect(screen.queryByText(LOAD_FAILED)).toBeNull();
    expect(get).toHaveBeenCalledTimes(3);
  });

  it("still says it failed when the panel doesn't come back within 30 seconds", async () => {
    get.mockRejectedValue(httpError(502));
    renderWith(<PanelSSLTab />);
    await advance(25_000);
    expect(screen.queryByText(LOAD_FAILED)).toBeNull();
    await advance(10_000);
    expect(screen.getByText(LOAD_FAILED)).toBeTruthy();
    expect(get).toHaveBeenCalledTimes(16);
  });

  it("doesn't hold an ordinary error for 30 seconds", async () => {
    get.mockRejectedValue(httpError(500));
    renderWith(<PanelSSLTab />);
    await advance(1500);
    expect(screen.getByText(LOAD_FAILED)).toBeTruthy();
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("keeps the last loaded cards, with a warning, when a refresh fails", async () => {
    get.mockResolvedValueOnce(OK);
    renderWith(<PanelSSLTab />);
    await screen.findByText("panel.example.com", { selector: ".ant-card-head code" });

    get.mockRejectedValue(httpError(500));
    fireEvent.click(screen.getByRole("button", { name: /refresh/i }));
    await advance(1500);
    expect(screen.getByText(/Couldn't refresh the panel SSL state/)).toBeTruthy();
    expect(screen.getByText("panel.example.com", { selector: ".ant-card-head code" })).toBeTruthy();
    expect(screen.queryByText(LOAD_FAILED)).toBeNull();
  });
});

describe("Server Settings → Panel SSL card during a panel restart", () => {
  it("keeps the status lines through the 502s", async () => {
    get.mockResolvedValueOnce(OK);
    const qc = renderWith(<PanelSSLCard />);
    await screen.findByText("panel.example.com");

    get.mockRejectedValueOnce(httpError(502)).mockRejectedValueOnce(httpError(502)).mockResolvedValue(OK);
    await act(async () => {
      void qc.refetchQueries();
    });
    for (let i = 0; i < 3; i++) {
      await advance(2000);
      expect(screen.queryByText(LOAD_FAILED)).toBeNull();
      expect(screen.getByText("panel.example.com")).toBeTruthy();
    }
    expect(get).toHaveBeenCalledTimes(4);
  });

  it("keeps the last loaded lines, with a warning, when a refresh fails", async () => {
    get.mockResolvedValueOnce(OK);
    const qc = renderWith(<PanelSSLCard />);
    await screen.findByText("panel.example.com");

    get.mockRejectedValue(httpError(500));
    await act(async () => {
      void qc.refetchQueries();
    });
    await advance(1500);
    expect(screen.getByText(/Couldn't refresh the panel SSL state/)).toBeTruthy();
    expect(screen.getByText("panel.example.com")).toBeTruthy();
    expect(screen.queryByText(LOAD_FAILED)).toBeNull();
  });
});

describe("panelCertIssueMessage after a Let's Encrypt issue", () => {
  it("says the panel restarts to serve the hostname cert, and to reload", () => {
    const msg = panelCertIssueMessage("hostname", hostCert() as never);
    expect(msg).toBe(
      "Panel certificate issued. The panel restarts in a few seconds to serve it: reload the page, and your browser shows the new certificate.",
    );
  });

  it("is unchanged for the mail cert, which doesn't restart the panel", () => {
    expect(panelCertIssueMessage("mail", { ...hostCert(), kind: "mail" } as never)).toBe("mail cert: issued");
  });
});
