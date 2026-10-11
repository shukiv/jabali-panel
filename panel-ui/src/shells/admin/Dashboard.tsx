// Admin Dashboard — the community-mix layout voted for in GH discussion #1249
// (Option 7): Option 1's host banner and always-visible services rail, a row
// of count cards, and Option 6's tabbed surface carrying Option 3's inventory
// (Activity / Domains-Users-Databases / Hosting Packages / Backups / Alerts).
// Deep host metrics stay on /jabali-admin/server-status (M31, ADR-0065); the
// banner shows only the three meters an operator checks first.
import { Col, Row } from "antd";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";

import { AppstoreOutlined, GlobalOutlined, MailOutlined, TeamOutlined } from "@icons";

import { apiClient } from "../../apiClient";
import { StatCard } from "../../components/StatCard";
import { useServerStatus } from "../../hooks/useServerStatus";
import { useServerCapabilities } from "../../hooks/useServerCapabilities";
import { useUpdateState } from "../../hooks/useSystemUpdates";
import { UpdatesPendingBanner, OsSecurityUpdatesBanner } from "./updates/UpdatesPendingBanner";
import { DashboardTabs } from "./dashboard/DashboardTabs";
import { HostCard } from "./dashboard/HostCard";
import { ServicesRail } from "./dashboard/ServicesRail";

interface CountsResponse {
  users: number;
  domains: number;
  mailboxes: number;
  applications: number;
}

const formatCount = (n: number | undefined) =>
  n == null ? "—" : n.toLocaleString();

export const Dashboard = () => {
  const { t } = useTranslation();
  const status = useServerStatus();
  const env = status.data;
  const { data: caps } = useServerCapabilities();
  // Same query the updates banner reads, so the build SHA costs no request.
  const updateState = useUpdateState();

  const counts = useQuery({
    queryKey: ["dashboard", "admin-counts"],
    queryFn: async () => (await apiClient.get<CountsResponse>("/admin/counts")).data,
  });

  // GH #1417: with the mail module off the Mailboxes card would link to a
  // page that redirects away, so it is left out like the user dashboard's.
  const showMail = caps?.mail_enabled !== false;
  const cards = [
    {
      key: "users",
      label: t("dashboard.stats.users"),
      value: counts.data?.users,
      icon: <TeamOutlined />,
      color: "#1677ff",
      to: "/jabali-admin/users",
    },
    {
      key: "domains",
      label: t("dashboard.stats.domains"),
      value: counts.data?.domains,
      icon: <GlobalOutlined />,
      color: "#9254de",
      to: "/jabali-admin/domains",
    },
    ...(showMail
      ? [
          {
            key: "mailboxes",
            label: t("dashboard.stats.mailboxes"),
            value: counts.data?.mailboxes,
            icon: <MailOutlined />,
            color: "#fa8c16",
            to: "/jabali-admin/mail",
          },
        ]
      : []),
    {
      key: "applications",
      label: t("dashboard.stats.applications"),
      value: counts.data?.applications,
      icon: <AppstoreOutlined />,
      color: "#13c2c2",
      to: "/jabali-admin/applications",
    },
  ];

  return (
    <div>
      {/* JAB-221: above everything else. The commits-behind count existed
          only inside the Updates Center, and two production incidents came
          from boxes silently running builds that predated an already-merged
          fix. */}
      <UpdatesPendingBanner />
      <OsSecurityUpdatesBanner />
      <HostCard env={env} caps={caps} currentSha={updateState.data?.jabali_current_sha} />
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {cards.map((c) => (
          <Col key={c.key} xs={12} lg={24 / cards.length}>
            <StatCard
              label={c.label}
              value={formatCount(c.value)}
              icon={c.icon}
              iconColor={c.color}
              to={c.to}
            />
          </Col>
        ))}
      </Row>
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={16} xxl={17}>
          <DashboardTabs alerts={env?.alerts ?? []} />
        </Col>
        <Col xs={24} lg={8} xxl={7}>
          <ServicesRail services={env ? env.services?.services ?? [] : undefined} />
        </Col>
      </Row>
    </div>
  );
};
