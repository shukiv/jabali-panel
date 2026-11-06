// The admin database console opens phpMyAdmin / Adminer with a single-use
// sign-in link. The tab must be open before that link is minted, so a blocked
// tab never costs a link, and the tab only goes to an http(s) address.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));
vi.mock("../docker-apps/api", () => ({ listInstalled: vi.fn().mockResolvedValue([]) }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { warning: vi.fn(), error: vi.fn(), success: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback";
import { AdminDbConsoleSection } from "./DatabaseAdminSections";

const post = apiClient.post as unknown as ReturnType<typeof vi.fn>;

function fakeTab() {
  return { opener: {} as unknown, location: { href: "" }, close: vi.fn() };
}

function renderSection() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AdminDbConsoleSection />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  post.mockReset();
  vi.mocked(feedback.message.warning).mockReset();
  vi.mocked(feedback.message.error).mockReset();
});

describe("AdminDbConsoleSection", () => {
  it("opens the tab before asking for the sign-in link, then sends it there", async () => {
    const tab = fakeTab();
    const open = vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    post.mockResolvedValue({ data: { redirect_url: "https://panel.example/phpmyadmin/sso.php?token=abc" } });
    renderSection();

    fireEvent.click(screen.getByRole("button", { name: /open phpmyadmin/i }));
    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    expect(tab.opener).toBeNull();

    await waitFor(() => expect(tab.location.href).toBe("https://panel.example/phpmyadmin/sso.php?token=abc"));
    expect(post).toHaveBeenCalledWith("/admin/databases/sso/phpmyadmin", {});
    expect(open.mock.invocationCallOrder[0]).toBeLessThan(post.mock.invocationCallOrder[0]);
  });

  it("does not ask for a sign-in link when the browser blocks the tab", () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: /open adminer/i }));
    expect(post).not.toHaveBeenCalled();
    expect(feedback.message.warning).toHaveBeenCalled();
  });

  it("closes the tab when the link cannot be minted", async () => {
    const tab = fakeTab();
    vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    post.mockRejectedValue(new Error("recent login required"));
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: /open adminer/i }));
    await waitFor(() => expect(tab.close).toHaveBeenCalled());
    expect(feedback.message.error).toHaveBeenCalledWith("Could not open Adminer: recent login required");
  });

  it("closes the tab for a link that is not a web address", async () => {
    const tab = fakeTab();
    vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    post.mockResolvedValue({ data: { redirect_url: "javascript:alert(1)" } });
    renderSection();
    fireEvent.click(screen.getByRole("button", { name: /open phpmyadmin/i }));
    await waitFor(() => expect(tab.close).toHaveBeenCalled());
    expect(tab.location.href).toBe("");
  });
});
