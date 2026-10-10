// ModulesCard.test.tsx — M353 module install-on-enable status rendering.
// The card must reflect the real install state per module (not just the flag):
// an enabled module that isn't installed+active shows "not installed" + Retry,
// while an installed+active one shows "active". This is the whole point of Step
// 4 — surfacing that a flag-on module can still be down, and offering recovery.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), patch: vi.fn(), post: vi.fn() },
}));

const errorToast = vi.hoisted(() => vi.fn());
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: vi.fn(), error: errorToast, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { ModulesCard } from "./ModulesCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPatch = apiClient.patch as ReturnType<typeof vi.fn>;

type Flags = Partial<Record<"dns_enabled" | "mail_enabled" | "security_enabled" | "quota_enabled" | "api_enabled", boolean>>;

// serve answers the card's two GETs with these flags and module statuses.
const serve = (flags: Flags, modules: Record<string, Record<string, unknown>>) => {
  mockGet.mockImplementation((url: string) => {
    if (url === "/admin/settings") {
      return Promise.resolve({
        data: { dns_enabled: true, mail_enabled: true, security_enabled: true, quota_enabled: true, api_enabled: true, ...flags },
      });
    }
    if (url === "/admin/settings/modules/status") return Promise.resolve({ data: { modules } });
    return Promise.resolve({ data: {} });
  });
};

const up = { installed: true, active: true };
const mailSwitch = () => screen.getByRole("switch", { name: /Mail server/ });

describe("ModulesCard install status", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows 'active' for an installed+active module and 'not installed' + Retry for an enabled-but-down one", async () => {
    mockGet.mockImplementation((url: string) => {
      if (url === "/admin/settings") {
        return Promise.resolve({
          data: { dns_enabled: true, mail_enabled: true, security_enabled: false, quota_enabled: true, api_enabled: true },
        });
      }
      if (url === "/admin/settings/modules/status") {
        return Promise.resolve({
          data: {
            modules: {
              dns: { installed: true, active: true },
              mail: { installed: false, active: false }, // enabled but down
              quota: { installed: true, active: true },
            },
          },
        });
      }
      return Promise.resolve({ data: {} });
    });

    render(<ModulesCard />);

    // dns is installed+active -> "active"; mail is enabled-but-down -> Retry.
    await waitFor(() => expect(screen.getAllByText("active").length).toBeGreaterThan(0));
    expect(screen.getByText("not installed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });
});

// GH #2056: a failed install showed "installing" for five minutes, then "not
// installed", with the reason only in the panel log.
describe("ModulesCard install errors", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows why the last install failed and where its log is", async () => {
    serve({}, {
      dns: up,
      mail: {
        installed: false, active: false, installing: false,
        last_error: "the DNS module must be installed first. Enable DNS, then mail.",
        last_error_at: "2026-10-09T10:00:00Z",
        install_log: "/var/log/jabali/install-2026-10-09_10-00-00.log",
      },
      security: up, quota: up,
    });
    render(<ModulesCard />);
    expect(await screen.findByText(/the DNS module must be installed first\. Enable DNS, then mail\./)).toBeInTheDocument();
    expect(screen.getByText("/var/log/jabali/install-2026-10-09_10-00-00.log")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });

  it("shows an install the agent reports as running, even one this page didn't start", async () => {
    serve({}, { dns: { installed: false, active: false, installing: true }, mail: up, security: up, quota: up });
    render(<ModulesCard />);
    expect(await screen.findByText(/installing/)).toBeInTheDocument();
    expect(screen.queryByText("not installed")).not.toBeInTheDocument();
  });

  it("stops waiting and shows the reason as soon as the agent reports a new failed install", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const old = { installed: false, active: false, last_error: "an older failure", last_error_at: "2026-10-01T00:00:00Z" };
      const at = (mail: object) => ({ dns: up, mail, security: up, quota: up });
      const sequence = [
        at(old), // page load
        at(old), // the install hasn't started yet: the older failure is still the last one
        at({ ...old, installing: true }), // running; the record still holds the older failure
        at({ installed: false, active: false, installing: true, last_error: "the first attempt failed", last_error_at: "2026-10-09T19:59:00Z" }), // a second install queued behind a failed one
        at({ installed: false, active: false, last_error: "stalwart download failed (exit 22)", last_error_at: "2026-10-09T20:00:00Z" }),
      ];
      let n = 0;
      mockGet.mockImplementation((url: string) => {
        if (url === "/admin/settings") return Promise.resolve({ data: { dns_enabled: true, mail_enabled: false, security_enabled: true, quota_enabled: true, api_enabled: true } });
        if (url === "/admin/settings/modules/status") return Promise.resolve({ data: { modules: sequence[Math.min(n++, sequence.length - 1)] } });
        return Promise.resolve({ data: {} });
      });
      mockPatch.mockResolvedValue({ data: {} });
      render(<ModulesCard />);
      await waitFor(() => expect(mailSwitch()).not.toBeDisabled());
      fireEvent.click(mailSwitch());
      for (let poll = 1; poll <= 3; poll += 1) {
        await vi.advanceTimersByTimeAsync(5000);
        expect(screen.getByText(/installing/)).toBeInTheDocument();
        expect(screen.queryByText(/Last install failed/)).not.toBeInTheDocument();
      }
      await vi.advanceTimersByTimeAsync(5000); // the new failure
      expect(await screen.findByText("Last install failed: stalwart download failed (exit 22)")).toBeInTheDocument();
      expect(screen.queryByText(/installing/)).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows the server's reason when turning a module on is refused", async () => {
    serve({ mail_enabled: false }, { dns: up, mail: { installed: false, active: false }, security: up, quota: up });
    mockPatch.mockRejectedValue({ response: { status: 409, data: { error: "dns_not_ready", detail: "Mail needs the DNS module running." } } });
    render(<ModulesCard />);
    await waitFor(() => expect(mailSwitch()).not.toBeDisabled());
    fireEvent.click(mailSwitch());
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith(expect.stringContaining("Mail needs the DNS module running.")));
  });

  it("shows the server's reason when a Retry is refused", async () => {
    // DNS stopped after the card last looked: the card offers Retry, the panel refuses it.
    serve({}, { dns: up, mail: { installed: false, active: false }, security: up, quota: up });
    (apiClient.post as ReturnType<typeof vi.fn>).mockRejectedValue({ response: { status: 409, data: { error: "dns_not_ready", detail: "Wait until DNS shows active, then turn on mail." } } });
    render(<ModulesCard />);
    fireEvent.click(await screen.findByRole("button", { name: /retry/i }));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith(expect.stringContaining("Wait until DNS shows active, then turn on mail.")));
  });
});

// GH #2056: mail's install needs DNS installed and running, so the Mail switch
// doesn't start an install that is sure to fail.
describe("ModulesCard mail needs DNS", () => {
  beforeEach(() => vi.clearAllMocks());

  it("keeps Mail off while DNS is off", async () => {
    serve({ dns_enabled: false, mail_enabled: false }, { dns: { installed: false, active: false }, mail: { installed: false, active: false }, security: up, quota: up });
    render(<ModulesCard />);
    expect(await screen.findByText(/Turn on DNS first/)).toBeInTheDocument();
    expect(mailSwitch()).toBeDisabled();
  });

  it("keeps Mail off while DNS is on but not running yet", async () => {
    serve({ mail_enabled: false }, { dns: { installed: true, active: false }, mail: { installed: false, active: false }, security: up, quota: up });
    render(<ModulesCard />);
    expect(await screen.findByText(/DNS isn't running yet/)).toBeInTheDocument();
    expect(mailSwitch()).toBeDisabled();
  });

  it("lets Mail be turned on once DNS is running", async () => {
    serve({ mail_enabled: false }, { dns: up, mail: { installed: false, active: false }, security: up, quota: up });
    render(<ModulesCard />);
    await waitFor(() => expect(mailSwitch()).not.toBeDisabled());
    expect(screen.queryByText(/Turn on DNS first|DNS isn't running yet/)).not.toBeInTheDocument();
  });

  it("always lets Mail be turned off", async () => {
    serve({ dns_enabled: false, mail_enabled: true }, { dns: { installed: false, active: false }, mail: up, security: up, quota: up });
    render(<ModulesCard />);
    await waitFor(() => expect(mailSwitch()).toBeChecked());
    expect(mailSwitch()).not.toBeDisabled();
  });

  it("holds mail's Retry until DNS is running", async () => {
    serve({}, { dns: { installed: true, active: false }, mail: { installed: false, active: false }, security: up, quota: up });
    render(<ModulesCard />);
    const retries = await screen.findAllByRole("button", { name: /retry/i });
    // dns (down) and mail (down) both offer Retry; only mail's waits for DNS.
    expect(retries).toHaveLength(2);
    expect(retries[0]).not.toBeDisabled();
    expect(retries[1]).toBeDisabled();
    expect(screen.getByText(/DNS isn't running yet/)).toBeInTheDocument();
  });
});

