// GH #1238 — admin change-owner modal. Pins that the owner picker lists only
// selectable tenants (not admins, not the current owner) and that confirming
// POSTs to the admin endpoint. apiClient.get (users list) + post are mocked.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
import { apiClient } from "../../../apiClient";
import { DomainChownAction } from "./DomainChownAction";
import type { Domain } from "../../../components/domains/types";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

const domain: Domain = {
  id: "d1",
  user_id: "old",
  name: "example.com",
  doc_root: "/home/alice/example.com",
  is_enabled: true,
  nginx_custom_directives: "",
  created_at: "",
  updated_at: "",
};

const usersPayload = {
  data: {
    data: [
      { id: "new", username: "bob", is_admin: false, linux_uid: 1002 },
      { id: "old", username: "alice", is_admin: false, linux_uid: 1001 },
      { id: "adm", username: "root", is_admin: true, linux_uid: 0 },
    ],
    total: 3,
  },
};

afterEach(() => {
  cleanup();
  mocked.get.mockReset();
  mocked.post.mockReset();
});

function renderModal() {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <DomainChownAction domain={domain} open onClose={() => {}} />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1238 — DomainChownAction", () => {
  it("gates the button on a chosen owner and POSTs the new_owner_id on confirm", async () => {
    mocked.get.mockResolvedValue(usersPayload);
    renderModal();
    await screen.findByText("New owner");

    const okBtn = () => screen.getByRole("button", { name: /^Change owner$/ });
    // No owner picked → disabled.
    expect(okBtn()).toBeDisabled();

    // Open the picker and choose the only selectable tenant (bob). AntD Select
    // = mouseDown the combobox to open, then click the option text.
    fireEvent.mouseDown(screen.getByRole("combobox"));
    const option = await screen.findByText("bob");
    fireEvent.click(option);

    // The current owner (alice) and the admin (root) are not offered.
    expect(screen.queryByText("alice")).toBeNull();
    expect(screen.queryByText("root")).toBeNull();

    expect(okBtn()).not.toBeDisabled();

    mocked.post.mockResolvedValue({ data: {} });
    fireEvent.click(okBtn());

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/admin/domains/d1/chown", {
        new_owner_id: "new",
      }),
    );
  });
});
