// GH #1236 — "Enable Docker Apps for users just spins forever until you refresh."
//
// The confirm dialog's onOk returned setForUsersFlag(true), which polls host
// state for up to 8 minutes. AntD keeps the confirm modal's OK button spinning
// until an onOk promise resolves, so the dialog looked stuck for the whole poll.
// The fix detaches the poll (onOk returns void) so the dialog closes at once and
// progress shows on the card's own Switch. This test pins that: after confirming,
// the dialog must close promptly rather than block on the long poll.
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { App } from "antd";

const get = vi.fn();
const patch = vi.fn();
vi.mock("../../../apiClient", () => ({
  apiClient: {
    get: (...a: unknown[]) => get(...a),
    patch: (...a: unknown[]) => patch(...a),
  },
}));

function mockApi() {
  get.mockImplementation((url: string) => {
    if (url === "/admin/settings")
      return Promise.resolve({
        data: {
          docker_marketplace_enabled: true,
          docker_apps_for_users_enabled: false,
          docker_tenant_apps: "",
        },
      });
    if (url === "/admin/docker-apps/catalog")
      return Promise.resolve({ data: { items: [] } });
    if (url === "/me/server-capabilities")
      // Never flips within the test — simulates a slow host setup. The dialog
      // must NOT wait on this.
      return Promise.resolve({ data: { docker_apps_user_enabled: false } });
    return Promise.resolve({ data: {} });
  });
  patch.mockResolvedValue({ data: {} });
}

beforeEach(mockApi);
afterEach(() => {
  cleanup();
  get.mockReset();
  patch.mockReset();
});

async function renderCard() {
  const { DockerMarketplaceCard } = await import("./DockerMarketplaceCard");
  return render(
    <App>
      <DockerMarketplaceCard />
    </App>,
  );
}

describe("GH #1236 — Enable Docker Apps for users doesn't hang the dialog", () => {
  it("closes the confirm dialog immediately instead of spinning on the 8-min poll", async () => {
    await renderCard();

    // Two switches render once the marketplace is enabled: [0] marketplace,
    // [1] "Enable Docker Apps for users".
    const switches = await screen.findAllByRole("switch");
    expect(switches.length).toBeGreaterThanOrEqual(2);
    fireEvent.click(switches[1]);

    // The confirm dialog's OK button ("Enable") appears — click it.
    const okBtn = await screen.findByRole("button", { name: /^Enable$/ });
    fireEvent.click(okBtn);

    // The detached flow runs (flag persisted immediately)…
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith("/admin/settings", {
        docker_apps_for_users_enabled: true,
      }),
    );

    // …and crucially the confirm's OK button is NOT left spinning on the
    // up-to-8-minute poll (the GH #1236 bug). With the buggy onOk-returns-promise
    // it would carry `ant-btn-loading`; the fix keeps it clear and moves the
    // progress spinner onto the card's own Switch instead.
    expect(screen.getByRole("button", { name: /^Enable$/ })).not.toHaveClass(
      "ant-btn-loading",
    );
    const switchesAfter = screen.getAllByRole("switch");
    expect(switchesAfter[1].className).toContain("ant-switch-loading");
  });
});
