// GH #1579: the Rename domain modal gates submit on a valid, different FQDN
// plus an explicit acknowledgment, normalizes the name, and posts to
// /domains/:id/rename. The notices (experimental + WordPress-URL-auto-updated)
// are load-bearing per johnnyq's ask, so assert they render. Any app-URL
// rewrite warnings returned on the 200 body are surfaced to the user.
import { App } from "antd";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RenameDomainButton } from "./RenameDomainButton";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback";

const mocked = apiClient as unknown as { post: ReturnType<typeof vi.fn> };
const fb = feedback as unknown as {
  message: { warning: ReturnType<typeof vi.fn> };
};

const renderBtn = (onRenamed = vi.fn()) => {
  render(
    <App>
      <RenameDomainButton domain={{ id: "d1", name: "old.com" }} onRenamed={onRenamed} />
    </App>,
  );
  return onRenamed;
};

const openModal = () => {
  fireEvent.click(screen.getByRole("button", { name: /rename domain/i }));
  return within(screen.getByRole("dialog"));
};

const okButton = (dialog: ReturnType<typeof within>) =>
  dialog.getByRole("button", { name: /rename domain/i });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RenameDomainButton", () => {
  it("shows the experimental + WordPress-URL notices when opened", () => {
    renderBtn();
    const dialog = openModal();
    expect(dialog.getByText(/experimental feature/i)).toBeInTheDocument();
    // The phrase also appears in the acknowledgment checkbox, so scope to the
    // alert title node.
    expect(
      dialog.getByText(/WordPress site URL is updated automatically/i, {
        selector: ".ant-alert-title",
      }),
    ).toBeInTheDocument();
  });

  it("keeps submit disabled until a valid, different name AND the acknowledgment", () => {
    renderBtn();
    const dialog = openModal();
    expect(okButton(dialog)).toBeDisabled();

    // valid new name, but no acknowledgment yet
    fireEvent.change(dialog.getByPlaceholderText("new-domain.com"), {
      target: { value: "new.com" },
    });
    expect(okButton(dialog)).toBeDisabled();

    // acknowledge -> enabled
    fireEvent.click(dialog.getByRole("checkbox"));
    expect(okButton(dialog)).toBeEnabled();

    // same as current name -> disabled again
    fireEvent.change(dialog.getByPlaceholderText("new-domain.com"), {
      target: { value: "OLD.com" },
    });
    expect(okButton(dialog)).toBeDisabled();
  });

  it("posts the normalized name and calls onRenamed on success", async () => {
    mocked.post.mockResolvedValueOnce({ data: { id: "d1", name: "new.com" } });
    const onRenamed = renderBtn();
    const dialog = openModal();

    fireEvent.change(dialog.getByPlaceholderText("new-domain.com"), {
      target: { value: "  New.COM  " },
    });
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(okButton(dialog));

    await waitFor(() =>
      expect(mocked.post).toHaveBeenCalledWith("/domains/d1/rename", { name: "new.com" }),
    );
    await waitFor(() => expect(onRenamed).toHaveBeenCalled());
  });

  it("surfaces app-URL rewrite warnings returned on the response", async () => {
    mocked.post.mockResolvedValueOnce({
      data: { id: "d1", name: "new.com", warnings: ["could not update the WordPress site URL"] },
    });
    const onRenamed = renderBtn();
    const dialog = openModal();

    fireEvent.change(dialog.getByPlaceholderText("new-domain.com"), {
      target: { value: "new.com" },
    });
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(okButton(dialog));

    await waitFor(() =>
      expect(fb.message.warning).toHaveBeenCalledWith(
        "could not update the WordPress site URL",
      ),
    );
    await waitFor(() => expect(onRenamed).toHaveBeenCalled());
  });
});
