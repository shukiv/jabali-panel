// GH #2425: the Add SSH key form fills the key from an uploaded .pub file,
// and refuses a private key without putting it in the form.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  listSSHKeys: vi.fn(),
  createSSHKey: vi.fn(),
  deleteSSHKey: vi.fn(),
  getSSHConnection: vi.fn(),
  getMySSHForwarding: vi.fn(),
}));

import * as api from "../../../apiClient";
import { UserSSHKeysPage } from "./UserSSHKeysPage";

const mocked = api as unknown as Record<string, ReturnType<typeof vi.fn>>;
const KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEtZ4mO5QdupIvjCNJyOr7P2DZxYyXUEdeXXgUT32aj+";

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <App>{children}</App>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  mocked.listSSHKeys.mockResolvedValue({ items: [] });
  mocked.getSSHConnection.mockRejectedValue(new Error("no shell user"));
  mocked.getMySSHForwarding.mockRejectedValue(new Error("n/a"));
  mocked.createSSHKey.mockReset().mockResolvedValue({});
});

async function openAddDrawer() {
  render(<UserSSHKeysPage />, { wrapper });
  // The page's "Add Key" button opens the drawer; the drawer's own "Add Key"
  // submits it.
  fireEvent.click((await screen.findAllByRole("button", { name: "Add Key" }))[0]);
  await screen.findByText("Upload key file");
}

function upload(text: string, name: string) {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [new File([text], name, { type: "text/plain" })] } });
}

describe("Add SSH key: upload a key file (GH #2425)", () => {
  it("fills the key and the name from a .pub file", async () => {
    await openAddDrawer();
    upload(`${KEY} shuki@laptop\n`, "id_ed25519.pub");

    const keyBox = screen.getByPlaceholderText("ssh-ed25519 AAAA... user@host") as HTMLTextAreaElement;
    await waitFor(() => expect(keyBox.value).toBe(`${KEY} shuki@laptop`));
    expect((screen.getByPlaceholderText("e.g., My Laptop") as HTMLInputElement).value).toBe("shuki@laptop");

    fireEvent.click(screen.getAllByRole("button", { name: "Add Key" }).at(-1)!);
    await waitFor(() =>
      expect(mocked.createSSHKey).toHaveBeenCalledWith({ name: "shuki@laptop", public_key: `${KEY} shuki@laptop` }),
    );
  });

  it("refuses a private key and leaves the form empty", async () => {
    await openAddDrawer();
    // Built from parts so the secret scanner doesn't read this file as holding one.
    const armor = ["OPENSSH", "PRIVATE", "KEY"].join(" ");
    upload(`-----BEGIN ${armor}-----\nb3BlbnNzaA==\n-----END ${armor}-----\n`, "id_ed25519");

    expect(await screen.findByText(/That file is a private key/)).toBeTruthy();
    expect((screen.getByPlaceholderText("ssh-ed25519 AAAA... user@host") as HTMLTextAreaElement).value).toBe("");
    expect(mocked.createSSHKey).not.toHaveBeenCalled();
  });
});
