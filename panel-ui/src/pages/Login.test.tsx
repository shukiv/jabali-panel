// LoginPage render + Kratos flow progression tests.
//
// The page fetches a Kratos login flow on mount, renders the flow's
// ui.nodes as AntD form fields, and submits to flow.ui.action. This
// test suite stubs the kratos wrapper so we can exercise the render
// path and the AAL1→AAL2 transition without a live Kratos instance.
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { BrowserRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as kratos from "../kratos";
import { ThemeModeProvider } from "../theme/ThemeModeContext";
import { LoginPage } from "./Login";

// LoginPage uses useQueryClient to prime the ["whoami"] AuthContext
// cache on successful login (eliminates the post-submit race where
// RequireAdmin briefly saw stale null). QueryClientProvider is the
// minimum wrapper that gives the hook something to read.
//
// LoginPage also reads `mode` from useThemeMode for the dark/light
// switcher in the page chrome — wrap in ThemeModeProvider so the
// hook resolves instead of throwing.
function renderLogin() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <ThemeModeProvider>
      <QueryClientProvider client={qc}>
        <BrowserRouter>
          <LoginPage />
        </BrowserRouter>
      </QueryClientProvider>
    </ThemeModeProvider>,
  );
}

function passwordFlow(): kratos.KratosFlow {
  return {
    id: "flow-aal1",
    type: "browser",
    expires_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    issued_at: new Date().toISOString(),
    request_url: "http://localhost/.ory/self-service/login/browser",
    ui: {
      action: "http://localhost/.ory/self-service/login?flow=flow-aal1",
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "default",
          attributes: {
            name: "csrf_token",
            type: "hidden",
            value: "csrf-abc",
            required: true,
          },
        },
        {
          type: "input",
          group: "password",
          attributes: {
            name: "identifier",
            type: "text",
            required: true,
            autocomplete: "email",
          },
          meta: { label: { text: "Email" } },
        },
        {
          type: "input",
          group: "password",
          attributes: {
            name: "password",
            type: "password",
            required: true,
            autocomplete: "current-password",
          },
          meta: { label: { text: "Password" } },
        },
      ],
    },
    requested_aal: "aal1",
  };
}

function totpFlow(): kratos.KratosFlow {
  return {
    id: "flow-aal2",
    type: "browser",
    expires_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    issued_at: new Date().toISOString(),
    request_url: "http://localhost/.ory/self-service/login/browser",
    ui: {
      action: "http://localhost/.ory/self-service/login?flow=flow-aal2",
      method: "POST",
      nodes: [
        {
          type: "input",
          group: "default",
          attributes: {
            name: "csrf_token",
            type: "hidden",
            value: "csrf-def",
          },
        },
        {
          type: "input",
          group: "totp",
          attributes: {
            name: "totp_code",
            type: "text",
            required: true,
            autocomplete: "one-time-code",
          },
          meta: { label: { text: "Authentication code" } },
        },
      ],
    },
    requested_aal: "aal2",
  };
}

// GH #917: a password flow that ALSO carries the passkey discoverable-login
// challenge Kratos injects when the passkey method is enabled.
function passkeyFlow(): kratos.KratosFlow {
  const f = passwordFlow();
  f.ui.nodes.push({
    type: "input",
    group: "passkey",
    attributes: {
      name: "passkey_challenge",
      type: "hidden",
      value: JSON.stringify({ publicKey: { challenge: "AQID", allowCredentials: [] } }),
    },
  });
  return f;
}

describe("LoginPage", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders the fields from the password-group flow", async () => {
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(passwordFlow());

    renderLogin();

    await waitFor(() => {
      expect(screen.getByLabelText(/email/i)).toBeInTheDocument();
    });
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /sign in/i }),
    ).toBeInTheDocument();
  });

  it("shows an error when flow initialisation fails", async () => {
    vi.spyOn(kratos, "initLoginFlow").mockRejectedValue(new Error("boom"));

    renderLogin();

    await waitFor(() => {
      expect(
        screen.getByText(/could not start the sign-in flow/i),
      ).toBeInTheDocument();
    });
  });

  it("switches to TOTP input when the flow continues to AAL2", async () => {
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(passwordFlow());
    vi.spyOn(kratos, "submitLoginFlow").mockResolvedValue({
      kind: "continue",
      flow: totpFlow(),
    });

    renderLogin();

    await waitFor(() =>
      expect(screen.getByLabelText(/email/i)).toBeInTheDocument(),
    );
    fireEvent.change(screen.getByLabelText(/email/i), {
      target: { value: "alice@example.com" },
    });
    fireEvent.change(screen.getByLabelText(/password/i), {
      target: { value: "pw-very-good" },
    });
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => {
      expect(screen.getByLabelText(/authentication code/i)).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /verify/i })).toBeInTheDocument();
  });

  // AAL2 flow whose only method is a webauthn security key (button-triggered).
  function webauthn2faFlow(): kratos.KratosFlow {
    const f = totpFlow();
    f.ui.nodes = [
      f.ui.nodes[0], // csrf
      {
        type: "input",
        group: "webauthn",
        attributes: {
          name: "webauthn_login_trigger",
          type: "button",
          value: JSON.stringify({ publicKey: { challenge: "AQID", allowCredentials: [] } }),
        },
      },
      {
        type: "input",
        group: "webauthn",
        attributes: { name: "webauthn_login", type: "hidden" },
      },
    ];
    return f;
  }

  it("offers a security-key button on an AAL2 webauthn step and submits method=webauthn", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    const get = vi.fn().mockResolvedValue({
      id: "wk",
      type: "public-key",
      rawId: new Uint8Array([1]).buffer,
      response: {
        authenticatorData: new Uint8Array([2]).buffer,
        clientDataJSON: new Uint8Array([3]).buffer,
        signature: new Uint8Array([4]).buffer,
        userHandle: null,
      },
    });
    Object.defineProperty(globalThis.navigator, "credentials", { value: { get }, configurable: true });
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(webauthn2faFlow());
    const submit = vi
      .spyOn(kratos, "submitLoginFlow")
      .mockResolvedValue({ kind: "error", message: "x" });

    renderLogin();
    const btn = await screen.findByRole("button", { name: /use your security key/i });
    // No spurious "no credential method" warning for the ceremony-only step.
    expect(screen.queryByText(/no credential method/i)).not.toBeInTheDocument();
    fireEvent.click(btn);

    await waitFor(() => expect(submit).toHaveBeenCalled());
    const body = submit.mock.calls[0][1] as Record<string, string>;
    expect(body.method).toBe("webauthn");
    expect(JSON.parse(body.webauthn_login).id).toBe("wk");
  });

  it("offers a passkey sign-in button when the flow advertises passkey", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(passkeyFlow());

    renderLogin();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /sign in with a passkey/i })).toBeInTheDocument(),
    );
  });

  it("runs the passkey ceremony and submits method=passkey", async () => {
    vi.stubGlobal("PublicKeyCredential", function () {});
    const get = vi.fn().mockResolvedValue({
      id: "cred",
      type: "public-key",
      rawId: new Uint8Array([1]).buffer,
      response: {
        authenticatorData: new Uint8Array([2]).buffer,
        clientDataJSON: new Uint8Array([3]).buffer,
        signature: new Uint8Array([4]).buffer,
        userHandle: new Uint8Array([5]).buffer,
      },
    });
    Object.defineProperty(globalThis.navigator, "credentials", {
      value: { get },
      configurable: true,
    });
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(passkeyFlow());
    const submit = vi
      .spyOn(kratos, "submitLoginFlow")
      .mockResolvedValue({ kind: "continue", flow: totpFlow() });

    renderLogin();

    const btn = await screen.findByRole("button", { name: /sign in with a passkey/i });
    fireEvent.click(btn);

    await waitFor(() => expect(submit).toHaveBeenCalled());
    const body = submit.mock.calls[0][1] as Record<string, string>;
    expect(body.method).toBe("passkey");
    expect(body.identifier).toBe("");
    expect(JSON.parse(body.passkey_login).id).toBe("cred");
    expect(get).toHaveBeenCalledTimes(1);
  });

  it("surfaces top-level flow errors into an alert", async () => {
    const flow = passwordFlow();
    flow.ui.messages = [
      {
        id: 4000006,
        text: "The provided credentials are invalid.",
        type: "error",
      },
    ];
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(flow);

    renderLogin();

    await waitFor(() => {
      expect(
        screen.getByText(/provided credentials are invalid/i),
      ).toBeInTheDocument();
    });
  });
});
