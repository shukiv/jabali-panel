// MyProfile render tests — Kratos settings flow groups.
//
// Covers: loading state, password-only (no 2FA), TOTP enrolment (QR),
// TOTP enrolled (unlink button), recovery-codes reveal.
// All tests start with ?flow=flow-s1 in the URL so initSettingsFlow
// is bypassed; only getSettingsFlow is exercised.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as identityMod from "../../identity";
import * as kratos from "../../kratos";
import { MyProfile } from "./MyProfile";

vi.mock("./MyProfileUsageCard", () => ({ MyProfileUsageCard: () => null }));

const FAKE_IDENTITY: identityMod.Identity = {
  id: "user-123",
  email: "test@example.com",
  isAdmin: false,
};

function baseFlow(extra: kratos.KratosNode[]): kratos.KratosFlow {
  return {
    id: "flow-s1",
    type: "browser",
    expires_at: new Date(Date.now() + 600_000).toISOString(),
    issued_at: new Date().toISOString(),
    request_url: "http://localhost/.ory/self-service/settings/browser",
    ui: {
      action: "http://localhost/.ory/self-service/settings?flow=flow-s1",
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "default",
          attributes: { name: "csrf_token", type: "hidden", value: "csrf-abc" },
        },
        ...extra,
      ],
    },
  };
}

function renderProfile(search = "?flow=flow-s1") {
  return render(
    <MemoryRouter initialEntries={[`/profile${search}`]}>
      <Routes>
        <Route path="*" element={<MyProfile />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("MyProfile 2FA", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(identityMod, "getIdentity").mockResolvedValue(FAKE_IDENTITY);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows spinner while the settings flow is loading", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockReturnValue(new Promise(() => {}));
    const { container } = renderProfile();
    // AntD Spin renders .ant-spin while loading
    await waitFor(() =>
      expect(container.querySelector(".ant-spin")).not.toBeNull(),
    );
  });

  it("renders password group and no 2FA cards when only password group present", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "password",
          attributes: { name: "password", type: "password", required: true },
          meta: { label: { text: "New password" } },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "method", type: "submit", value: "password" },
          meta: { label: { text: "Save" } },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(screen.getByText("Update password")).toBeInTheDocument(),
    );
    expect(screen.queryByAltText("TOTP QR code")).not.toBeInTheDocument();
    expect(
      screen.queryByText("Save these recovery codes — shown once"),
    ).not.toBeInTheDocument();
  });

  it("shows TOTP QR image and base32 secret during first enrolment", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        // totpEnrolmentDisplay casts node.attributes to {src?:string} for img nodes
        {
          type: "img",
          group: "totp",
          attributes: { src: "data:image/png;base64,QRQRQR" } as unknown as kratos.KratosNodeInputAttributes,
        } as unknown as kratos.KratosNode,
        {
          type: "text",
          group: "totp",
          attributes: {
            id: "totp_secret_key",
            text: { text: "ABCD1234EFGH5678", id: 1050003 },
          } as unknown as kratos.KratosNodeInputAttributes,
        } as unknown as kratos.KratosNode,
        {
          type: "input",
          group: "totp",
          attributes: { name: "totp_code", type: "text", required: true },
          meta: { label: { text: "Verification code" } },
        },
        {
          type: "input",
          group: "totp",
          attributes: { name: "method", type: "submit", value: "totp" },
          meta: { label: { text: "Enrol" } },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(screen.getByAltText("TOTP QR code")).toBeInTheDocument(),
    );
    expect(screen.getByText("ABCD1234EFGH5678")).toBeInTheDocument();
    expect(screen.getByText("Scan this QR with your authenticator app")).toBeInTheDocument();
  });

  it("shows Disable TOTP danger button when TOTP is already enrolled", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "totp",
          attributes: { name: "totp_unlink", type: "submit", value: "true" },
          meta: { label: { text: "Disable TOTP" } },
        },
      ]),
    );
    renderProfile();
    // submitButtonLabel("totp", "totp_unlink") → "Disable two-factor"
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /disable two-factor/i }),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByAltText("TOTP QR code")).not.toBeInTheDocument();
  });

  it("shows recovery codes panel with individual codes after regeneration", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "text",
          group: "lookup_secret",
          attributes: {
            id: "lookup_secret_codes",
            text: {
              id: 1050015,
              text: "codes",
              context: {
                secrets: [{ text: "aaaaa-bbbbb" }, { text: "ccccc-ddddd" }],
              },
            },
          } as unknown as kratos.KratosNodeInputAttributes,
        } as unknown as kratos.KratosNode,
        {
          type: "input",
          group: "lookup_secret",
          attributes: {
            name: "lookup_secret_regenerate",
            type: "submit",
            value: "true",
          },
          meta: { label: { text: "Regenerate" } },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(
        screen.getByText("Save these recovery codes — shown once"),
      ).toBeInTheDocument(),
    );
    // RecoveryCodesReveal renders codes.join("\n") as one text node — use regex
    expect(screen.getByText(/aaaaa-bbbbb/)).toBeInTheDocument();
    expect(screen.getByText(/ccccc-ddddd/)).toBeInTheDocument();
  });

  it("shows the passkey enrolment card with an Add button (GH #917)", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "passkey",
          attributes: {
            name: "passkey_create_data",
            type: "hidden",
            value: JSON.stringify({ publicKey: { user: { id: "AQID" }, challenge: "AQID" } }),
          },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(screen.getByText("Passkeys (passwordless)")).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /add a passkey/i })).toBeInTheDocument();
  });

  it("lists a Remove button for each registered passkey (GH #917)", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "passkey",
          attributes: {
            name: "passkey_create_data",
            type: "hidden",
            value: JSON.stringify({ publicKey: {} }),
          },
        },
        {
          type: "input",
          group: "passkey",
          attributes: { name: "passkey_remove", type: "submit", value: "cred-abc" },
          meta: { label: { text: "Remove" } },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^remove$/i })).toBeInTheDocument(),
    );
  });

  it("shows the security-key (webauthn 2FA) card with a name field + Add button", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "webauthn",
          attributes: {
            name: "webauthn_register_displayname",
            type: "text",
          },
        },
        {
          type: "input",
          group: "webauthn",
          attributes: {
            name: "webauthn_register_trigger",
            type: "button",
            value: JSON.stringify({ publicKey: { user: { id: "AQID" }, challenge: "AQID" } }),
          },
        },
      ]),
    );
    renderProfile();
    await waitFor(() =>
      expect(screen.getByText("Security keys (two-factor)")).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /add a security key/i })).toBeInTheDocument();
  });

  it("submits the typed totp_code on TOTP enrolment (regression #140)", async () => {
    vi.spyOn(kratos, "getSettingsFlow").mockResolvedValue(
      baseFlow([
        {
          type: "input",
          group: "totp",
          attributes: { name: "totp_code", type: "text", required: true },
        },
        {
          type: "input",
          group: "totp",
          attributes: { name: "method", type: "submit", value: "totp" },
        },
      ]),
    );
    const submitSpy = vi
      .spyOn(kratos, "submitSettingsFlow")
      .mockResolvedValue({ kind: "success" } as kratos.KratosSubmitResult);

    renderProfile();
    const input = await screen.findByRole("textbox");
    fireEvent.change(input, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Save TOTP" }));

    await waitFor(() => expect(submitSpy).toHaveBeenCalled());
    const body = submitSpy.mock.calls[0][1];
    expect(body.totp_code).toBe("123456");
    expect(body.method).toBe("totp");
  });

});
