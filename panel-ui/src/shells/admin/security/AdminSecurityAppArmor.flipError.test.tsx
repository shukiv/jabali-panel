// AdminSecurityAppArmor.flipError.test.tsx — GH #2001. When a profile flip
// fails, the toast carries the agent's reason (aa-complain or aa-enforce's own
// output, returned in the error body's detail). It used to say only "Flip
// failed — check agent logs", and the agent doesn't log it.
// apiClient and the toast are mocked; the real AntD table and modal render.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AdminSecurityAppArmor } from "./AdminSecurityAppArmor";
import { appArmorFlipErrorMessage } from "../../../hooks/useSecurityAppArmor";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};
const toastError = feedback.message.error as unknown as ReturnType<typeof vi.fn>;

const REASON = "aa-complain jabali-fpm-app: ERROR: Cannot reload profile: exit status 1";

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <App>
        <AdminSecurityAppArmor />
      </App>
    </QueryClientProvider>,
  );
}

describe("AppArmor flip failure (GH #2001)", () => {
  beforeEach(() => {
    mocked.get.mockReset().mockResolvedValue({
      data: {
        enabled: true,
        profiles: [{ name: "jabali-fpm-app", mode: "enforce" }],
        denials: [],
        violations: [],
      },
    });
    mocked.post.mockReset().mockRejectedValue({
      response: { status: 502, data: { status: "error", error: "internal", detail: REASON } },
      message: "Request failed with status code 502",
    });
    toastError.mockReset();
  });

  it("shows the agent's reason in the toast", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Flip to complain/ }));
    const dialog = await screen.findByRole("dialog");
    const ok = dialog.querySelector(".ant-modal-footer .ant-btn-primary") as HTMLElement;
    fireEvent.click(ok);

    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(mocked.post).toHaveBeenCalledWith("/admin/security/apparmor/profiles/jabali-fpm-app/mode", { mode: "complain" });
    expect(String(toastError.mock.calls[0][0])).toContain(REASON);
    expect(within(dialog).queryByText(/check agent logs/)).toBeNull();
  });
});

describe("appArmorFlipErrorMessage", () => {
  it("prefers the detail, then the error code, then the message", () => {
    expect(appArmorFlipErrorMessage({ response: { data: { detail: "d", error: "e" } }, message: "m" })).toBe("Flip failed: d");
    expect(appArmorFlipErrorMessage({ response: { data: { error: "deadline_exceeded" } }, message: "m" })).toBe("Flip failed: deadline_exceeded");
    expect(appArmorFlipErrorMessage({ message: "Network Error" })).toBe("Flip failed: Network Error");
    expect(appArmorFlipErrorMessage(undefined)).toBe("Flip failed");
  });

  it("cuts a long tool output", () => {
    const msg = appArmorFlipErrorMessage({ response: { data: { detail: "x".repeat(2000) } } });
    expect(msg.length).toBeLessThan(700);
    expect(msg.endsWith("…")).toBe(true);
  });
});
