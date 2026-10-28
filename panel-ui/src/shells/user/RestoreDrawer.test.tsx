// GH #2303: the restore drawer offers every domain of the backup for its
// files (not only domains with custom DNS records), lists each domain under
// DNS (greyed out when the backup has no custom records for it), and can
// delete files added since the backup.
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RestoreDrawer } from "./RestoreDrawer";

vi.mock("../../apiClient", () => ({
  apiClient: { get: vi.fn(), post: vi.fn() },
}));

import { apiClient } from "../../apiClient";

const mocked = apiClient as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
};

function mockManifest(extra: Record<string, unknown>) {
  mocked.get.mockReset().mockImplementation((url: string) => {
    if (url === "/me/backups/b1/manifest") {
      return Promise.resolve({
        data: {
          kind: "account_backup",
          username: "alice",
          stages: [
            { name: "home", status: "ok" },
            { name: "db", status: "ok", items: ["alice_wp"] },
          ],
          ...extra,
        },
      });
    }
    // The job list the drawer polls after starting a restore.
    return Promise.resolve({ data: { data: [{ id: "j1", status: "succeeded", warnings_json: { applied: ["ok"] } }] } });
  });
}

function renderDrawer() {
  render(
    <App>
      <RestoreDrawer backupId="b1" open onClose={() => {}} />
    </App>,
  );
}

function checkboxFor(section: string, label: string): HTMLInputElement {
  const heading = screen.getByText(section);
  const group = heading.closest("div") as HTMLElement;
  const el = Array.from(group.querySelectorAll("label")).find((l) => l.textContent?.startsWith(label));
  if (!el) throw new Error(`no ${label} under ${section}`);
  return el.querySelector("input") as HTMLInputElement;
}

// antd's Checkbox.Group passes on only the values its checkboxes have
// registered, which each does in an effect after it first renders: a click
// that lands before that is dropped. Click until the checkbox takes it.
async function tick(section: string, label: string) {
  await waitFor(() => {
    const cb = checkboxFor(section, label);
    if (!cb.checked) fireEvent.click(cb);
    expect(checkboxFor(section, label).checked).toBe(true);
  });
}

beforeEach(() => {
  mocked.post.mockReset().mockResolvedValue({ data: { job_id: "j1" } });
});

describe("RestoreDrawer domains (GH #2303)", () => {
  it("offers every domain's files, even without custom DNS records", async () => {
    mockManifest({ domains: ["plain.example", "dns.example"], dns_domains: ["dns.example"] });
    renderDrawer();
    await screen.findByText("Domain files");
    expect(checkboxFor("Domain files", "plain.example").disabled).toBe(false);
    expect(checkboxFor("Domain files", "dns.example").disabled).toBe(false);
  });

  it("lists every domain under DNS, greyed out without custom records", async () => {
    mockManifest({ domains: ["plain.example", "dns.example"], dns_domains: ["dns.example"] });
    renderDrawer();
    await screen.findByText("DNS records");
    expect(checkboxFor("DNS records", "plain.example").disabled).toBe(true);
    expect(checkboxFor("DNS records", "dns.example").disabled).toBe(false);
    expect(screen.getByText(/no custom DNS records in this backup/i)).toBeTruthy();
  });

  it("sends delete_extra when asked to delete files added since the backup", async () => {
    mockManifest({ domains: ["plain.example"], dns_domains: [] });
    renderDrawer();
    await screen.findByText("Domain files");
    await tick("Domain files", "plain.example");
    fireEvent.click(screen.getByText(/Delete files added since the backup/i));
    fireEvent.click(screen.getByText(/I understand this overwrites/i));
    fireEvent.click(screen.getByText("Restore selected"));
    await waitFor(() => expect(mocked.post).toHaveBeenCalled(), { timeout: 5000 });
    const body = mocked.post.mock.calls[0][1];
    expect(body.domains).toEqual(["plain.example"]);
    expect(body.delete_extra).toBe(true);
  });

  it("leaves files added since the backup by default", async () => {
    mockManifest({ domains: ["plain.example"], dns_domains: [] });
    renderDrawer();
    await screen.findByText("Domain files");
    await tick("Domain files", "plain.example");
    fireEvent.click(screen.getByText(/I understand this overwrites/i));
    fireEvent.click(screen.getByText("Restore selected"));
    await waitFor(() => expect(mocked.post).toHaveBeenCalled(), { timeout: 5000 });
    expect(mocked.post.mock.calls[0][1].delete_extra).toBe(false);
  });
});
