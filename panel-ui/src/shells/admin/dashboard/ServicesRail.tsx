// ServicesRail — the admin Dashboard's always-visible services list (GH #1249:
// lxsdevcode asked for the server's service state to stay in view whatever
// tab is open). Read-only: start / stop / restart stay on Server Status, where
// the confirm dialogs and the self-destruct guards live.
import { Card, Tag, Typography } from "antd";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";

import { useServerCapabilities } from "../../../hooks/useServerCapabilities";
import type { ServiceDetail } from "../../../hooks/useServerStatus";
import {
  isRunConfigured,
  prettyName,
  statusTag,
  visibleServicesFor,
} from "../server-status/serviceHelpers";

interface Props {
  /** undefined while the first server-status poll is in flight. */
  services: ServiceDetail[] | undefined;
}

export function ServicesRail({ services }: Props) {
  const { t } = useTranslation();
  const { data: caps } = useServerCapabilities();
  const visible = services ? visibleServicesFor(services, caps) : [];

  // "N of M running" counts the units that are meant to run. An idle unit
  // that is not run-configured (jabali-webmail before any domain enables
  // mail) is idle by design, so it is neither running nor missing.
  const expected = visible.filter((s) => s.active === "active" || isRunConfigured(s.unit_file_state));
  const running = expected.filter((s) => s.active === "active").length;
  const failed = expected.some((s) => s.active === "failed");
  const summaryColor = running === expected.length ? "green" : failed ? "red" : "orange";

  return (
    <Card
      size="small"
      title={t("dashboard.services.title")}
      loading={services === undefined}
      extra={
        services !== undefined && (
          <Tag color={summaryColor} style={{ marginInlineEnd: 0 }}>
            {t("dashboard.services.running", { running, total: expected.length })}
          </Tag>
        )
      }
      actions={[
        <Link key="manage" to="/jabali-admin/server-status">
          {t("dashboard.services.manage")}
        </Link>,
      ]}
    >
      {visible.map((s) => (
        <div
          key={s.unit}
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            gap: 8,
            padding: "4px 0",
          }}
        >
          <Typography.Text ellipsis style={{ minWidth: 0 }}>
            {prettyName(s.unit)}
          </Typography.Text>
          {statusTag(s)}
        </div>
      ))}
      {services !== undefined && visible.length === 0 && (
        <Typography.Text type="secondary">{t("dashboard.services.empty")}</Typography.Text>
      )}
    </Card>
  );
}
