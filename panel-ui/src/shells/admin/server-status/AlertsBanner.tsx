// AlertsBanner — top-of-page critical/warning roll-up. Empty list =
// banner hidden, no spacing reserved.
import { Alert, Space } from "antd";
import { Link } from "react-router";

import type { Alert as ServerAlert } from "../../../hooks/useServerStatus";

interface Props {
  alerts: ServerAlert[];
}

export function AlertsBanner({ alerts }: Props) {
  if (!alerts || alerts.length === 0) return null;
  return (
    <Space direction="vertical" size={8} style={{ width: "100%", marginBottom: 16 }}>
      {alerts.map((a, i) => {
        const link = deriveLink(a);
        return (
          <Alert
            key={i}
            type={a.level === "critical" ? "error" : "warning"}
            showIcon
            message={a.detail}
            description={link ? <Link to={link.path}>{link.label} →</Link> : null}
          />
        );
      })}
    </Space>
  );
}

// deriveLink turns alert kinds into deep-links to the relevant
// remediation page. Step 4 plan calls these out explicitly.
function deriveLink(a: ServerAlert): { path: string; label: string } | null {
  switch (a.kind) {
    case "queue":
      // Both notification-stream "stuck" + DLQ-non-empty alerts land
      // here. The DLQ tab on /jabali-admin/notifications shows the
      // failed envelopes + the replay/discard controls; for the
      // stream-stuck variant the same page renders a banner with the
      // suspected cause. Either way, the dispatcher's UI is the
      // remediation entry point.
      return { path: "/jabali-admin/notifications/dlq", label: "Open Notification DLQ" };
    case "service":
      // pdns-recursor inactive → operator usually needs the security
      // page (firewall, restart) or the updates page. Default to
      // server-status itself; the row is visible on the same page.
      return null;
    case "disk":
      return null;
    case "load":
      return null;
    case "agent":
      // Agent sub-call timeouts: nothing to link to; shown as info.
      return null;
    case "updates":
      return { path: "/jabali-admin/updates", label: "Open Updates" };
    case "crowdsec":
      return { path: "/jabali-admin/security", label: "Open Security" };
    case "apparmor":
      // JAB-379: a jabali AppArmor profile is complain/missing/unconfined
      // rather than enforce. The Security page's AppArmor section shows each
      // profile's mode + the per-profile enforce/complain flip control.
      return { path: "/jabali-admin/security", label: "Open Security" };
    default:
      return null;
  }
}
