// PublicOnly — wraps the public /login route.
//
// An already-authenticated visitor to /login is normally bounced to their
// shell home instead of seeing the form. The one exception is a `?flow=<id>`
// in the URL: Kratos redirected the user back to /login to COMPLETE a re-auth
// flow it created — a JAB-380 recent-auth step-up (admin File Manager / Root
// Terminal) or an aal2 escalation. The base session is still valid, so bouncing
// home would abandon that flow (authenticated_at is never re-stamped and the
// step-up never satisfies), stranding the user on the dashboard — the GH #1184
// "File Manager sends me back to the main menu" regression. When a flow is
// present we render the LoginPage so the user can finish it; Login.tsx then
// honours `post_login_return_to` back to the originating page.
import type { ReactNode } from "react";
import { Navigate } from "react-router";
import { Spin } from "antd";

import { useAuth } from "./AuthContext";

export function PublicOnly({ children }: { children: ReactNode }) {
  const { user, isAdmin, isLoading } = useAuth();
  if (isLoading) {
    return (
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          minHeight: "100vh",
        }}
      >
        <Spin size="large" />
      </div>
    );
  }
  if (user) {
    const hasFlow = new URLSearchParams(window.location.search).has("flow");
    if (!hasFlow) {
      return <Navigate to={isAdmin ? "/jabali-admin" : "/jabali-panel"} replace />;
    }
  }
  return <>{children}</>;
}
