// InstallApplicationModal.credentials.test.tsx — the install drawer applies a
// descriptor's admin password pattern and reserved usernames before it POSTs,
// so a credential the app's installer refuses never starts an install. Runs the
// real AntD Drawer/Form; only apiClient, the registry hook and the password
// generator are mocked.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ThemeModeProvider } from "../../../theme/ThemeModeContext";
import type { AppDescriptor, ParamSpec } from "./appRegistry";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));
vi.mock("./appRegistry", () => ({ useAppRegistry: vi.fn() }));
vi.mock("../../../components/passwordGenerator", () => ({
  generatePassword: vi.fn(),
}));

import { apiClient } from "../../../apiClient";
import { generatePassword } from "../../../components/passwordGenerator";
import { InstallApplicationModal } from "./InstallApplicationModal";
import { useAppRegistry } from "./appRegistry";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

function renderWith(schema: Record<string, ParamSpec>) {
  const descriptor: AppDescriptor = {
    name: "demo",
    display_name: "Demo",
    default_subdirectory: "",
    requires_db: true,
    root_only: true,
    install_param_schema: schema,
  };
  vi.mocked(useAppRegistry).mockReturnValue({
    data: [descriptor],
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useAppRegistry>);
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ThemeModeProvider>
        <App>
          <InstallApplicationModal
            open
            onClose={() => {}}
            onSuccess={() => {}}
            presetAppType="demo"
            presetDomainId="d1"
          />
        </App>
      </ThemeModeProvider>
    </QueryClientProvider>,
  );
}

async function fill(label: string, value: string) {
  const input = await screen.findByLabelText(label);
  fireEvent.change(input, { target: { value } });
}

async function install() {
  const button = await screen.findByRole("button", { name: "Install Demo" });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocked.get.mockResolvedValue({ data: { data: [{ id: "d1", name: "example.test" }] } });
  mocked.post.mockResolvedValue({
    data: {
      id: "i1",
      app_type: "demo",
      domain_id: "d1",
      db_id: "db1",
      admin_username: "helpdesk",
      admin_email: "",
      admin_password: "generated",
    },
  });
});

describe("InstallApplicationModal admin credentials", () => {
  it("refuses a password the app's pattern rejects and does not POST", async () => {
    renderWith({ admin_password: { type: "password", pattern: "^(.{12,})?$" } });
    await fill("Admin password", "short");
    await install();
    expect(await screen.findByText(/does not meet this app's password rules/i)).toBeInTheDocument();
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("counts characters the way the API does, not UTF-16 units", async () => {
    // 3 emoji are 6 UTF-16 units but 3 characters: under the 5 minimum.
    renderWith({ admin_password: { type: "password", pattern: "^(.{5,20})?$" } });
    await fill("Admin password", "😀😀😀");
    await install();
    expect(await screen.findByText(/does not meet this app's password rules/i)).toBeInTheDocument();
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("accepts 11 emoji under a 20-character maximum and POSTs them", async () => {
    renderWith({ admin_password: { type: "password", pattern: "^(.{5,20})?$" } });
    const pw = "😀".repeat(11);
    await fill("Admin password", pw);
    await install();
    await waitFor(() => expect(mocked.post).toHaveBeenCalled());
    expect(mocked.post.mock.calls[0][1].params.admin_password).toBe(pw);
  });

  it("refuses a reserved username in any case and does not POST", async () => {
    renderWith({
      admin_username: { type: "string", reserved: ["admin", "osticket"] },
    });
    await fill("Admin username", "OsTicket");
    await install();
    expect(await screen.findByText(/reserved by this app/i)).toBeInTheDocument();
    expect(mocked.post).not.toHaveBeenCalled();
  });

  it("generates a password the app's pattern accepts", async () => {
    vi.mocked(generatePassword)
      .mockReturnValueOnce("has=equals1234AB")
      .mockReturnValueOnce("noequals1234ABcd");
    renderWith({ admin_password: { type: "password", pattern: "^[^=\\r\\n]*$" } });
    fireEvent.click(await screen.findByRole("button", { name: "Generate strong password" }));
    await waitFor(() =>
      expect(screen.getByLabelText("Admin password")).toHaveValue("noequals1234ABcd"),
    );
  });
});
