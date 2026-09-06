// DomainLogsPanel.test — GH #1543 / JAB-296. The single-domain Logs pane offers
// the three streams as buttons and, crucially, binds the domain id into every
// open. This pins AC2 (a per-domain request always carries domain_id) at the
// new call site: clicking "Error Log" must POST /logs/access with the domain
// id, never the admin aggregate (no domain_id).
import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

vi.mock("../../apiClient", () => ({
  apiClient: { post: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn() } },
}));
// Stub the modal so the WebSocket connection it would open is irrelevant here.
vi.mock("../LogStreamModal", () => ({
  LogStreamModal: () => null,
}));

import { apiClient } from "../../apiClient";
import { DomainLogsPanel } from "./DomainLogsPanel";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };

beforeEach(() => {
  vi.clearAllMocks();
  mocked.post.mockResolvedValue({
    data: { stream_key: "sk1", websocket_url: "wss://host/api/v1/logs/stream/sk1" },
  });
});

describe("DomainLogsPanel (GH #1543)", () => {
  it("opens each stream for THIS domain (domain_id always present, AC2)", async () => {
    render(<DomainLogsPanel domainId="d1" />);

    // antd folds each icon's aria-label into the button's accessible name, so
    // match the label as a substring rather than exactly.
    fireEvent.click(screen.getByRole("button", { name: /Error Log/ }));
    await vi.waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/logs/access", {
        log_type: "error",
        domain_id: "d1",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: /Access Log/ }));
    await vi.waitFor(() =>
      expect(mocked.post).toHaveBeenLastCalledWith("/logs/access", {
        log_type: "access",
        domain_id: "d1",
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: /Real Time/ }));
    await vi.waitFor(() =>
      expect(mocked.post).toHaveBeenLastCalledWith("/logs/access", {
        log_type: "goaccess",
        domain_id: "d1",
      }),
    );
  });
});
