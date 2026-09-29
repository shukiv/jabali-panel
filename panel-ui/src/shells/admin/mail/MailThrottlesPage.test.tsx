// The throttle form must ask for what the API accepts. POST
// /admin/mail/throttles validates scope_ref as a sender address (scope=user)
// or a sender domain (scope=domain) and rejects anything else. The form used
// to label the field "Scope ref (ULID)" with a "users.id" placeholder, so an
// admin who followed it got a 400. It also called the daily cap "logged only",
// but the reconciler enforces it as its own Stalwart throttle.
//
// Only apiClient is mocked; the real AntD Drawer, Form and Select run.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));
import { apiClient } from "../../../apiClient";
import { MailThrottlesPage } from "./MailThrottlesPage";

const mocked = apiClient as unknown as { get: ReturnType<typeof vi.fn> };

afterEach(cleanup);

function renderPage() {
  mocked.get.mockResolvedValue({ data: { items: [] } });
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <App>
        <MailThrottlesPage />
      </App>
    </QueryClientProvider>,
  );
}

async function openDrawerWithScope(scope: "user" | "domain") {
  fireEvent.click(screen.getByRole("button", { name: "New throttle" }));
  const drawer = await screen.findByRole("dialog");
  fireEvent.mouseDown(within(drawer).getByRole("combobox"));
  fireEvent.click(await screen.findByTitle(scope));
  return drawer;
}

describe("mail throttle form asks for what the API accepts", () => {
  it("asks for a sender address for a user throttle", async () => {
    renderPage();
    const drawer = await openDrawerWithScope("user");
    expect(await within(drawer).findByLabelText("Sender address")).toHaveAttribute("placeholder", "alice@example.com");
    expect(within(drawer).queryByText(/ULID/)).toBeNull();
  });

  it("asks for a sender domain for a domain throttle", async () => {
    renderPage();
    const drawer = await openDrawerWithScope("domain");
    expect(await within(drawer).findByLabelText("Sender domain")).toHaveAttribute("placeholder", "example.com");
  });

  // Stalwart refuses a throttle over 1,000,000 messages per window, so a
  // larger cap could never be applied.
  it("stops both caps at Stalwart's maximum", async () => {
    renderPage();
    const drawer = await openDrawerWithScope("user");
    expect(within(drawer).getByLabelText("Max per hour (0 = unlimited)")).toHaveAttribute("aria-valuemax", "1000000");
    expect(within(drawer).getByLabelText("Max per day (0 = unlimited)")).toHaveAttribute("aria-valuemax", "1000000");
  });

  it("does not call the daily cap unenforced", async () => {
    renderPage();
    const drawer = await openDrawerWithScope("user");
    expect(within(drawer).getByText("Max per day (0 = unlimited)")).toBeTruthy();
    expect(screen.queryByText(/logged only|not enforced/)).toBeNull();
  });
});
