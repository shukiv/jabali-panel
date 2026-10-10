// WebsiteMailCard.test.tsx — GH #2056: website mail through the local mail
// server or a smarthost. The password is write-only (never shown, an empty
// field keeps the stored one), a login is never sent without encryption, and
// a failed test or save shows the server's reason.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../apiClient", () => ({
  apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() },
}));

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("../../../lib/feedback", () => ({
  feedback: { message: { success: toast.success, error: toast.error, info: vi.fn(), warning: vi.fn() } },
}));

import { apiClient } from "../../../apiClient";
import { WebsiteMailCard } from "./WebsiteMailCard";

const mockGet = apiClient.get as ReturnType<typeof vi.fn>;
const mockPut = apiClient.put as ReturnType<typeof vi.fn>;
const mockPost = apiClient.post as ReturnType<typeof vi.fn>;

const base = {
  mode: "local",
  host: "",
  port: 587,
  tls: "starttls",
  username: "",
  password_set: false,
  mail_module_enabled: true,
  allowed_ports: [25, 465, 587, 2525],
} as {
  mode: string;
  host: string;
  port: number;
  tls: string;
  username: string;
  password_set: boolean;
  mail_module_enabled: boolean;
  allowed_ports: number[];
  senders?: number;
  skipped?: string[];
};

const serve = (over: Partial<typeof base>) => mockGet.mockResolvedValue({ data: { ...base, ...over } });
const refusal = (detail: string) => ({ response: { status: 422, data: { error: "smarthost_test_failed", detail } } });

describe("WebsiteMailCard", () => {
  beforeEach(() => vi.clearAllMocks());

  it("warns when sites can't send because the mail module is off", async () => {
    serve({ mail_module_enabled: false });
    render(<WebsiteMailCard />);
    expect(await screen.findByText(/The mail module is off, so websites can't send email/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: /A smarthost/ }));
    expect(screen.queryByText(/The mail module is off/)).not.toBeInTheDocument();
  });

  it("switches to a smarthost with the typed values", async () => {
    serve({});
    const saved = { ...base, mode: "smarthost", host: "smtp.example.com", username: "relay@example.com", password_set: true };
    mockPut.mockResolvedValue({ data: saved });
    render(<WebsiteMailCard />);
    fireEvent.click(await screen.findByRole("radio", { name: /A smarthost/ }));
    fireEvent.change(screen.getByLabelText("Smarthost host"), { target: { value: "  smtp.example.com " } });
    fireEvent.change(screen.getByLabelText("Smarthost username"), { target: { value: "relay@example.com" } });
    fireEvent.change(screen.getByLabelText("Smarthost password"), { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mockPut).toHaveBeenCalledWith("/admin/settings/website-mail", {
        mode: "smarthost",
        host: "smtp.example.com",
        port: 587,
        tls: "starttls",
        username: "relay@example.com",
        password: "s3cret",
      }),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Website mail now goes through the smarthost."));
    // The password field is emptied after saving: it is never shown back.
    expect(screen.getByLabelText("Smarthost password")).toHaveValue("");
    expect(screen.getByLabelText("Smarthost password")).toHaveAttribute("placeholder", "Stored. Leave empty to keep it.");
  });

  it("keeps the stored password when the field is left empty", async () => {
    serve({ mode: "smarthost", host: "smtp.example.com", username: "relay@example.com", password_set: true });
    mockPut.mockResolvedValue({ data: { ...base, mode: "smarthost", host: "smtp.example.com", username: "relay@example.com", password_set: true } });
    render(<WebsiteMailCard />);
    expect(await screen.findByLabelText("Smarthost password")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockPut).toHaveBeenCalled());
    expect(mockPut.mock.calls[0][1]).toMatchObject({ username: "relay@example.com", password: "" });
  });

  it("asks for the password again when the host changes", async () => {
    serve({ mode: "smarthost", host: "smtp.example.com", username: "relay@example.com", password_set: true });
    render(<WebsiteMailCard />);
    const pw = await screen.findByLabelText("Smarthost password");
    expect(pw).toHaveAttribute("placeholder", "Stored. Leave empty to keep it.");
    fireEvent.change(screen.getByLabelText("Smarthost host"), { target: { value: "smtp.other.example" } });
    expect(pw).toHaveAttribute("placeholder", "Enter the password again for this host and username");
    fireEvent.change(screen.getByLabelText("Smarthost host"), { target: { value: "SMTP.example.com" } });
    expect(pw).toHaveAttribute("placeholder", "Stored. Leave empty to keep it.");
  });

  it("never sends a login without encryption", async () => {
    serve({ mode: "smarthost", host: "10.0.0.25", port: 25, tls: "none", username: "relay", password_set: true });
    mockPost.mockResolvedValue({ data: { ok: true } });
    render(<WebsiteMailCard />);
    expect(await screen.findByLabelText("Smarthost username")).toBeDisabled();
    expect(screen.getByLabelText("Smarthost password")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalled());
    expect(mockPost.mock.calls[0][1]).toMatchObject({ tls: "none", username: "", password: "" });
    expect(await screen.findByText("The smarthost accepted the connection.")).toBeInTheDocument();
  });

  it("tests the form without saving and shows why a test failed", async () => {
    serve({ mode: "smarthost", host: "smtp.example.com", username: "relay@example.com", password_set: true });
    mockPost.mockRejectedValue(refusal("login failed: 535 5.7.8 Authentication credentials invalid"));
    render(<WebsiteMailCard />);
    fireEvent.click(await screen.findByRole("button", { name: "Test" }));
    expect(await screen.findByText("login failed: 535 5.7.8 Authentication credentials invalid")).toBeInTheDocument();
    expect(mockPost).toHaveBeenCalledWith("/admin/settings/website-mail/test", expect.objectContaining({ host: "smtp.example.com" }));
    expect(mockPut).not.toHaveBeenCalled();
  });

  it("says how many accounts can send, and who the server left out", async () => {
    serve({ mode: "smarthost", host: "smtp.example.com", senders: 3 });
    mockPut.mockResolvedValue({
      data: { ...base, mode: "smarthost", host: "smtp.example.com", senders: 2, skipped: ["ghost: no such system user"] },
    });
    render(<WebsiteMailCard />);
    expect(await screen.findByText(/3 accounts can send through the smarthost\./)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/2 accounts can send through the smarthost\./)).toBeInTheDocument();
    expect(screen.getByText("ghost: no such system user")).toBeInTheDocument();
  });

  it("shows why a save was refused", async () => {
    serve({ mode: "smarthost", host: "smtp.example.com" });
    mockPut.mockRejectedValue(refusal("TLS failed: the smarthost doesn't offer STARTTLS"));
    render(<WebsiteMailCard />);
    fireEvent.click(await screen.findByRole("button", { name: "Save" }));
    expect(await screen.findByText("TLS failed: the smarthost doesn't offer STARTTLS")).toBeInTheDocument();
    expect(toast.error).toHaveBeenCalledWith("TLS failed: the smarthost doesn't offer STARTTLS");
  });
});
