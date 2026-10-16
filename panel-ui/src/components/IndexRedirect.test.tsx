// A shell root (/jabali-admin, /jabali-panel) sends the visitor on to its
// dashboard. Signing in navigates to the shell root twice: Login.tsx calls
// navigate() and PublicOnly, seeing the new user, renders its own
// <Navigate>. When the second one lands while the index redirect's own
// navigation is still pending, the router settles on the shell root again.
// A plain <Navigate> is still mounted with the same target, so its effect
// never runs again and the page stays blank under the header until a reload.
import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { lazy, Suspense, useEffect, useRef } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { BrowserRouter, Outlet, Route, Routes, useNavigate } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../apiClient", () => ({
  apiClient: { get: vi.fn() },
}));

import { apiClient } from "../apiClient";
import { AuthProvider } from "../auth/AuthContext";
import { PublicOnly } from "../auth/PublicOnly";
import { RequireAdmin } from "../auth/RequireAdmin";
import * as kratos from "../kratos";
import { LoginPage } from "../pages/Login";
import { queryClient } from "../query";
import { ThemeModeProvider } from "../theme/ThemeModeContext";
import { IndexRedirect } from "./IndexRedirect";

const mockedGet = apiClient.get as unknown as ReturnType<typeof vi.fn>;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  mockedGet.mockReset();
  queryClient.clear();
  window.history.replaceState({}, "", "/");
});

const delay = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

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
          attributes: { name: "csrf_token", type: "hidden", value: "csrf-abc", required: true },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "identifier", type: "text", required: true, autocomplete: "email" },
          meta: { label: { text: "Email" } },
        },
        {
          type: "input",
          group: "password",
          attributes: { name: "password", type: "password", required: true, autocomplete: "current-password" },
          meta: { label: { text: "Password" } },
        },
      ],
    },
    requested_aal: "aal1",
  };
}

// SecondBounce stands in for PublicOnly's <Navigate>: one more navigation to
// the shell root, in the same effect flush as the index redirect's own.
function SecondBounce() {
  const navigate = useNavigate();
  const done = useRef(false);
  useEffect(() => {
    if (done.current) return;
    done.current = true;
    navigate("/jabali-admin", { replace: true });
  }, [navigate]);
  return null;
}

describe("IndexRedirect", () => {
  it("reaches the dashboard when a second navigation to the shell root lands", async () => {
    window.history.pushState({}, "", "/jabali-admin");
    render(
      <BrowserRouter>
        <Routes>
          <Route
            path="/jabali-admin"
            element={
              <>
                <Outlet />
                <SecondBounce />
              </>
            }
          >
            <Route index element={<IndexRedirect to="dashboard" />} />
            <Route path="dashboard" element={<div>DASHBOARD</div>} />
          </Route>
        </Routes>
      </BrowserRouter>,
    );
    await waitFor(() => expect(screen.getByText("DASHBOARD")).toBeInTheDocument());
    expect(window.location.pathname).toBe("/jabali-admin/dashboard");
  });

  it("lands a fresh sign-in on the dashboard", async () => {
    // The real sign-in: LoginPage submits, clearIdentity refetches whoami
    // (PublicOnly sees the user and bounces to /jabali-admin), then
    // getIdentity answers and LoginPage navigates to /jabali-admin again
    // while the dashboard chunk is still loading.
    vi.spyOn(kratos, "initLoginFlow").mockResolvedValue(passwordFlow());
    let signedIn = false;
    vi.spyOn(kratos, "submitLoginFlow").mockImplementation(async () => {
      signedIn = true;
      return { kind: "success" } as never;
    });
    const admin = { id: "a1", email: "admin@example.com", is_admin: true };
    let meAfterSignIn = 0;
    mockedGet.mockImplementation((url: string) => {
      if (url !== "/me") return Promise.resolve({ status: 200, data: {} });
      if (!signedIn) return Promise.reject(Object.assign(new Error("401"), { response: { status: 401 } }));
      // The whoami refetch answers first, getIdentity a little later.
      meAfterSignIn++;
      return delay(meAfterSignIn === 1 ? 5 : 40).then(() => ({ status: 200, data: admin }));
    });
    const Dashboard = lazy(() => delay(150).then(() => ({ default: () => <div>DASHBOARD</div> })));

    window.history.pushState({}, "", "/login");
    render(
      <ThemeModeProvider>
        <QueryClientProvider client={queryClient}>
          <AuthProvider>
            <BrowserRouter>
              <Suspense fallback={<div>LOADING</div>}>
                <Routes>
                  <Route
                    path="/jabali-admin"
                    element={
                      <RequireAdmin>
                        <Outlet />
                      </RequireAdmin>
                    }
                  >
                    <Route index element={<IndexRedirect to="dashboard" />} />
                    <Route path="dashboard" element={<Dashboard />} />
                  </Route>
                  <Route
                    path="/login"
                    element={
                      <PublicOnly>
                        <LoginPage />
                      </PublicOnly>
                    }
                  />
                </Routes>
              </Suspense>
            </BrowserRouter>
          </AuthProvider>
        </QueryClientProvider>
      </ThemeModeProvider>,
    );
    fireEvent.change(await screen.findByLabelText(/email/i), { target: { value: "admin@example.com" } });
    fireEvent.change(screen.getByLabelText(/password/i), { target: { value: "pw-very-good" } });
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => expect(screen.getByText("DASHBOARD")).toBeInTheDocument(), { timeout: 3000 });
    expect(window.location.pathname).toBe("/jabali-admin/dashboard");
  });

  it("is what every index route in App.tsx redirects with", () => {
    // A bare <Navigate> as an index element is the bug above; keep App.tsx
    // on IndexRedirect.
    const app = readFileSync(resolve(__dirname, "../App.tsx"), "utf8");
    expect(app).not.toMatch(/<Route\s+index\s+element=\{<Navigate\b/);
    expect(app).toMatch(/<Route\s+index\s+element=\{<IndexRedirect to="dashboard" \/>\}/);
  });
});
