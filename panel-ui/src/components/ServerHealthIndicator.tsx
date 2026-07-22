// ServerHealthIndicator — admin-only header badge that surfaces the
// worst-level alert from /admin/server-status. Click navigates to the
// full Server Status page. Hidden when status is "ok" (no exclamation
// in the chrome for the steady state).
//
// Polls every 30s on a setInterval — same cadence as NotificationBell.
// Endpoint is admin-gated; on the user shell this component MUST NOT
// mount (caller in JabaliHeader checks isAdminShell).

import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Badge, Button, Tooltip } from "antd";
import { ExclamationCircleOutlined, WarningOutlined } from "@icons";
import { useNavigate } from "react-router";
import { apiClient } from "../apiClient";

interface Alert {
  level: "warning" | "critical";
  kind: string;
  message?: string;
}

interface ServerStatusPayload {
  alerts?: Alert[];
}

// poll cadence — matches NotificationBell. Keep <60s so an operator
// who just fixed a disk doesn't stare at a stale red badge.
const POLL_MS = 30_000;

export function ServerHealthIndicator(): JSX.Element | null {
  const { t } = useTranslation();
  const [worst, setWorst] = useState<"ok" | "warning" | "critical">("ok");
  const [count, setCount] = useState(0);
  const [tooltip, setTooltip] = useState("");
  const navigate = useNavigate();

  const refresh = async () => {
    try {
      const resp = await apiClient.get<ServerStatusPayload>("/admin/server-status");
      const alerts = resp.data.alerts ?? [];
      if (alerts.length === 0) {
        setWorst("ok");
        setCount(0);
        setTooltip("");
        return;
      }
      const hasCritical = alerts.some((a) => a.level === "critical");
      setWorst(hasCritical ? "critical" : "warning");
      setCount(alerts.length);
      // Show up to 3 alert messages in the tooltip; collapse the rest.
      const head = alerts.slice(0, 3).map((a) => `[${a.level}] ${a.kind}${a.message ? ": " + a.message : ""}`);
      const tail = alerts.length > 3 ? `\n+${alerts.length - 3} more` : "";
      setTooltip(head.join("\n") + tail);
    } catch {
      // Network blip — leave previous state intact rather than blink.
    }
  };

  useEffect(() => {
    void refresh();
    const id = window.setInterval(refresh, POLL_MS);
    return () => window.clearInterval(id);
  }, []);

  if (worst === "ok") return null;

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
