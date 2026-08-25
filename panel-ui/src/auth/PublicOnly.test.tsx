// PublicOnly guard tests (GH #1184).
//
// PublicOnly wraps the public /login route. An authenticated visitor is
// normally bounced to their shell home — EXCEPT when Kratos redirected them
// back to /login with a `?flow=<id>` to complete a re-auth flow (a JAB-380
// recent-auth step-up for the admin File Manager / Root Terminal, or an aal2
// escalation). Bouncing then would strand the step-up on the dashboard, which
// was the #1184 "File Manager sends me back to the main menu" regression.
import { render, screen } from "@testing-library/react";
import { BrowserRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PublicOnly } from "./PublicOnly";

// Mock the auth hook so we can drive the authenticated/loading state.
const mockAuth = vi.fn();
vi.mock("./AuthContext", () => ({
  useAuth: () => mockAuth(),
}));

function renderAt(path: string) {
  window.history.pushState({}, "", path);
  return render(
    <BrowserRouter>
      <Routes>
        <Route
          path="/login"
          element={
            <PublicOnly>
              <div>LOGIN_FORM</div>
            </PublicOnly>
          }
        />
        <Route path="/jabali-admin" element={<div>ADMIN_HOME</div>} />
        <Route path="/jabali-panel" element={<div>USER_HOME</div>} />
      </Routes>
    </BrowserRouter>,
  );
}

describe("PublicOnly", () => {
  afterEach(() => {
    mockAuth.mockReset();
  });

  it("bounces an authenticated admin from bare /login to their shell home", () => {
    mockAuth.mockReturnValue({ user: { id: "u1" }, isAdmin: true, isLoading: false });
    renderAt("/login");
    expect(screen.getByText("ADMIN_HOME")).toBeInTheDocument();
    expect(screen.queryByText("LOGIN_FORM")).not.toBeInTheDocument();
  });

  it("lets an authenticated user complete a step-up flow at /login?flow=<id>", () => {
    // GH #1184: base session valid but Kratos needs a re-auth to satisfy the
    // step-up — render the flow instead of bouncing home.
    mockAuth.mockReturnValue({ user: { id: "u1" }, isAdmin: true, isLoading: false });
    renderAt("/login?flow=abc123");
    expect(screen.getByText("LOGIN_FORM")).toBeInTheDocument();
    expect(screen.queryByText("ADMIN_HOME")).not.toBeInTheDocument();
  });

  it("renders the login form for an unauthenticated visitor", () => {
    mockAuth.mockReturnValue({ user: null, isAdmin: false, isLoading: false });
    renderAt("/login");
    expect(screen.getByText("LOGIN_FORM")).toBeInTheDocument();
  });
});
