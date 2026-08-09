// ServerHealthIndicator — admin-only header badge that surfaces the
// worst-level alert from /admin/server-status. Click navigates to the
// full Server Status page. Hidden when status is "ok" (no exclamation
// in the chrome for the steady state).
//
// Subscribes to the shared `["admin","server-status"]` query at a 30s
// cadence rather than fetching on its own timer. This component mounts on
// EVERY admin page, so a hand-rolled setInterval + raw apiClient.get meant
// a second, uncached fan-out of the heavy aggregate stacked on top of
// whatever the page itself was polling — and it kept firing in background
// tabs left open overnight. React Query dedupes across observers and
// pauses in background tabs (refetchIntervalInBackground: false).
// Endpoint is admin-gated; on the user shell this component MUST NOT
// mount (caller in JabaliHeader checks isAdminShell).

import { useTranslation } from "react-i18next";
import { Badge, Button, Tooltip } from "antd";
import { ExclamationCircleOutlined, WarningOutlined } from "@icons";
import { useNavigate } from "react-router";
import { useServerStatus } from "../hooks/useServerStatus";

// poll cadence — matches NotificationBell. Keep <60s so an operator
// who just fixed a disk doesn't stare at a stale red badge. A page with a
// faster cadence (Server Status, Dashboard) wins while it is mounted.
const POLL_MS = 30_000;

export function ServerHealthIndicator(): JSX.Element | null {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { data } = useServerStatus({ refetchInterval: POLL_MS });

  const alerts = data?.alerts ?? [];
  // A fetch error leaves `data` at its last value, so the badge holds the
  // previous state instead of blinking — same intent as the old catch{}.
  if (alerts.length === 0) return null;

  const worst: "warning" | "critical" = alerts.some((a) => a.level === "critical")
    ? "critical"
    : "warning";
  const count = alerts.length;
  // Show up to 3 alert messages in the tooltip; collapse the rest.
  const head = alerts
    .slice(0, 3)
    .map((a) => `[${a.level}] ${a.kind}${a.detail ? ": " + a.detail : ""}`);
  const tooltip = head.join("\n") + (alerts.length > 3 ? `\n+${alerts.length - 3} more` : "");

  const Icon = worst === "critical" ? ExclamationCircleOutlined : WarningOutlined;
  const color = worst === "critical" ? "#ff4d4f" : "#faad14";

  return (
    <Tooltip title={tooltip} placement="bottomRight">
      <Button
        type="text"
        aria-label={t("serverhealthindicator.server_health_alerts")}
        onClick={() => navigate("/jabali-admin/server-status")}
        style={{ width: 40, height: 40, padding: 0, display: "inline-flex", alignItems: "center", justifyContent: "center" }}
      >
        <Badge
          count={count}
          size="small"
          overflowCount={99}
          style={{ backgroundColor: color }}
        >
          <Icon style={{ fontSize: 18, color }} />
        </Badge>
      </Button>
    </Tooltip>
  );
}
