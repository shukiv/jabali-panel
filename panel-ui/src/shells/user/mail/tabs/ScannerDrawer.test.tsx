// GH #2503: a Files resource's scanner drawer turns the scanner on with the
// login, password and space; refuses up front where the server can't keep
// the login to its folder; and lists only the ways the login can connect.
import { App } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));
vi.mock("../../../../identity", () => ({
  getIdentity: vi.fn().mockResolvedValue({ username: "alice" }),
}));

import { apiClient } from "../../../../apiClient";
import type { ScannerState, SharedResource } from "../../../../hooks/useSharedResources";
import { ScannerDrawer } from "./ScannerDrawer";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
};

const RESOURCE: SharedResource = {
  id: "r1",
  domain_id: "d1",
  kind: "files",
  email: "scans@one.test",
  display_name: "Scans",
  host_account_id: "h1",
  collection_id: "c1",
  created_at: "",
  updated_at: "",
};

const OFF: ScannerState = { enabled: false, ingested_total: 0, isolated: false, ftp_access: false, sftp_access: false };

const ON: ScannerState = {
  enabled: true,
  username: "alice_scanner",
  drop_path: "/home/alice/domains/one.test/scans/scans",
  max_mb: 1024,
  ingested_total: 3,
  isolated: true,
  ftp_access: true,
  sftp_access: true,
};

function serve(state: ScannerState, caps: { iso: boolean; ftp: boolean; pkg?: boolean }) {
  mocked.get.mockImplementation((url: string) => {
    if (url === "/shared-resources/r1/scanner") return Promise.resolve({ data: state });
    if (url === "/me/server-capabilities") {
      return Promise.resolve({
        data: {
          ftp_accounts_enabled: caps.pkg ?? true,
          ftp_isolation_available: caps.iso,
          ftp_server_enabled: caps.ftp,
        },
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <App>{children}</App>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("ScannerDrawer", () => {
  it("turns the scanner on with the login, password and space, and shows the password once", async () => {
    serve(OFF, { iso: true, ftp: false });
    mocked.put.mockResolvedValue({ data: ON });
    render(<ScannerDrawer resource={RESOURCE} onClose={() => {}} />, { wrapper });

    const password = await screen.findByLabelText("Password");
    fireEvent.change(password, { target: { value: "correct-horse-battery" } });
    fireEvent.submit(document.getElementById("shared-resource-scanner-form")!);

    await waitFor(() =>
      expect(mocked.put).toHaveBeenCalledWith("/shared-resources/r1/scanner", {
        label: "scanner",
        password: "correct-horse-battery",
        max_mb: 1024,
      }),
    );
    expect(await screen.findByText("alice_scanner")).toBeTruthy();
    expect(screen.getByText("correct-horse-battery")).toBeTruthy();
  });

  it("refuses up front where the server can't keep the login to its folder", async () => {
    serve(OFF, { iso: false, ftp: false });
    render(<ScannerDrawer resource={RESOURCE} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("This server can't keep a scanner login to its folder yet")).toBeTruthy();
    const turnOn = screen.getByRole("button", { name: "Turn on" }) as HTMLButtonElement;
    expect(turnOn.disabled).toBe(true);
  });

  it("refuses up front when the package has no FTP/SFTP accounts", async () => {
    serve(OFF, { iso: true, ftp: true, pkg: false });
    render(<ScannerDrawer resource={RESOURCE} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText(/doesn't include FTP\/SFTP accounts/)).toBeTruthy();
    const turnOn = screen.getByRole("button", { name: "Turn on" }) as HTMLButtonElement;
    expect(turnOn.disabled).toBe(true);
  });

  it("lists FTPS only when the server's FTP is on", async () => {
    serve(ON, { iso: true, ftp: false });
    render(<ScannerDrawer resource={RESOURCE} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText(/SFTP: host/)).toBeTruthy();
    expect(screen.queryByText(/FTPS: host/)).toBeNull();
    // An isolated login starts in /data of its jail.
    expect(screen.getByText("/data")).toBeTruthy();
  });

  it("shows the last pass's problem", async () => {
    serve({ ...ON, last_error: "the folder is full" }, { iso: true, ftp: true });
    render(<ScannerDrawer resource={RESOURCE} onClose={() => {}} />, { wrapper });

    expect(await screen.findByText("the folder is full")).toBeTruthy();
    expect(screen.getByText(/FTPS: host/)).toBeTruthy();
  });
});
