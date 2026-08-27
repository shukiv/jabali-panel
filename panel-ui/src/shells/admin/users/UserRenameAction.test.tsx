// GH #1238 — admin rename modal. Pins the client-side validation gate and that
// confirming POSTs the rename to the admin endpoint. Only apiClient is mocked.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { post: vi.fn() } }));
import { apiClient } from "../../../apiClient";
import { UserRenameAction } from "./UserRenameAction";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };

afterEach(() => {
  cleanup();
  mocked.post.mockReset();
});

function renderModal() {
  const qc = new QueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <App>
        <UserRenameAction
          userId="u1"
          currentUsername="alice"
          userLabel="alice"
          open
          onClose={() => {}}
        />
      </App>
    </QueryClientProvider>,
  );
}

describe("GH #1238 — UserRenameAction", () => {
  it("gates the Rename button on a valid, changed username and POSTs on confirm", async () => {
    renderModal();
    await screen.findByText("Rename user");

    const input = screen.getByPlaceholderText("e.g. acme");
    const renameBtn = () => screen.getByRole("button", { name: /^Rename$/ });

    // Invalid (uppercase/space) → disabled.
    fireEvent.change(input, { target: { value: "Bad Name" } });
    expect(renameBtn()).toBeDisabled();

    // Unchanged (same as current) → disabled.
    fireEvent.change(input, { target: { value: "alice" } });
    expect(renameBtn()).toBeDisabled();

    // Valid + changed → enabled.
    fireEvent.change(input, { target: { value: "acme" } });
    expect(renameBtn()).not.toBeDisabled();

    mocked.post.mockResolvedValue({ data: {} });
    fireEvent.click(renameBtn());

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/admin/users/u1/rename", {
        new_username: "acme",
      }),
    );
  });

  it("does not POST while the username is invalid", async () => {
    renderModal();
    await screen.findByText("Rename user");
    const input = screen.getByPlaceholderText("e.g. acme");
    fireEvent.change(input, { target: { value: "9bad" } }); // must start with a letter/_
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    // onPressEnter → handleSubmit, but okDisabled short-circuits it.
    expect(mocked.post).not.toHaveBeenCalled();
  });
});
