// CapabilityRoute — route guard for opt-in features (gap-audit #1). Wraps a
// page element; while the capability check is in flight it shows a spinner,
// renders the page when the flag is on, and redirects to the shell dashboard
// when off. Without this, a direct deep-link to e.g. /jabali-panel/python-apps
// rendered the page and immediately hit a backend 403 (broken-looking screen)
// even though the sidebar correctly hid the entry.
//
// On the disabled redirect it also surfaces a one-time toast (gap-audit
// 2026-06-22 #2): a silent fall-through to the dashboard made "feature
// disabled" indistinguishable from "route broken". The toast tells the
// operator the deep-link is intentionally unavailable.
import type { ReactNode } from "react";
import { useEffect } from "react";

import { Spin } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts
import { Navigate } from "react-router";

import { useServerCapabilities, type ServerCapabilities } from "../hooks/useServerCapabilities";

interface CapabilityRouteProps {
  cap: keyof ServerCapabilities;
  fallback: string;
  children: ReactNode;
}

function DisabledRedirect({ fallback }: { fallback: string }) {
  useEffect(() => {
    feedback.message.info("That feature isn't enabled on this server.");
  }, []);
  return <Navigate to={fallback} replace />;
}

export function CapabilityRoute({ cap, fallback, children }: CapabilityRouteProps) {
  const { data, isLoading } = useServerCapabilities();
  if (isLoading) {
    return (
      <div style={{ padding: 48, textAlign: "center" }}>
        <Spin />
      </div>
    );
  }
  if (!data?.[cap]) {
    return <DisabledRedirect fallback={fallback} />;
  }
  return <>{children}</>;
}
