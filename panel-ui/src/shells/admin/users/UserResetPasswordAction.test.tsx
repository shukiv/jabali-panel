// GH #2058: the reset sets the panel, SSH and SFTP password. When the server
// account couldn't be updated, the panel still hands over the new panel
// password and says SSH/SFTP keep the old one.
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({ apiClient: { post: vi.fn() } }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { UserResetPasswordAction } from "./UserResetPasswordAction";

const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

const reset = async () => {
  render(<UserResetPasswordAction userId="u1" userLabel="alice" open onClose={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: /reset/i }));
  return screen.findByText("Tmp2058Password");
};

describe("UserResetPasswordAction", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows the new password without a warning when every login took it", async () => {
    mockPost.mockResolvedValue({ data: { password: "Tmp2058Password" } });
    await reset();
    expect(screen.queryByText(/SSH and SFTP still use the old password/)).not.toBeInTheDocument();
  });

  it("says SSH and SFTP keep the old password when the server account wasn't updated", async () => {
    mockPost.mockResolvedValue({ data: { password: "Tmp2058Password", os_synced: false } });
    await reset();
    expect(screen.getByText(/SSH and SFTP still use the old password/)).toBeInTheDocument();
  });
});
