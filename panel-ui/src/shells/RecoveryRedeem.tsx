// RecoveryRedeem — the public /recovery landing for billing-SSO login links
// (JAB-232, ADR-0165 addendum). The panel-api mints a URL of the form
// /recovery?flow=<id>&code=<code>; the previous shipped behaviour had no
// /recovery route at all, so the catch-all bounced the visitor to /login
// unauthenticated. This page reads flow+code, submits the Kratos code
// redemption (which creates the target user's session), and on success does a
// FULL navigation to "/" so AuthProvider re-runs whoami and LandingRedirect
// drops the now-authenticated user on their shell dashboard.
//
// No auth guard: the whole point is to run before a session exists.

import { useEffect, useState } from "react";
import { Button, Result, Spin } from "antd";

import { redeemRecoveryCode } from "../kratos";

export function RecoveryRedeem() {
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const flow = params.get("flow") ?? "";
    const code = params.get("code") ?? "";
    let cancelled = false;
    (async () => {
      const res = await redeemRecoveryCode(flow, code);
      if (cancelled) return;
      if (res.kind === "ok") {
        // Session cookie is set on the 422 response; a full navigation (not a
        // client-side route change) guarantees AuthProvider re-initialises with
        // the new session before LandingRedirect routes to the dashboard.
        window.location.assign("/");
        return;
      }
      setError(res.message);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const center: React.CSSProperties = {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    justifyContent: "center",
    minHeight: "100vh",
    gap: 16,
  };

  if (error) {
    return (
      <div style={center}>
        <Result
          status="warning"
          title="Couldn't sign you in"
          subTitle={error}
          extra={
            <Button type="primary" href="/login">
              Go to login
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div style={center}>
      <Spin size="large" />
      <div style={{ color: "rgba(0,0,0,0.45)" }}>Signing you in…</div>
    </div>
  );
}
