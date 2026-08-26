// AdminUserOverview — per-user hub for admin cross-entity navigation (#483,
// ADR-0152, Wave C; extended for #545). The one new admin detail route. It is
// the breadcrumb root for drilling into a user and the target of every
// "→ owner" link.
//
// Two card groups:
//   1. Admin-managed — owner-scoped admin lists (?user_id=) with live counts
//      read from each list's `total` (page_size=1); no dedicated counts endpoint.
//   2. User panel — features that live only in the user shell (databases, DNS,
//      SSL, files, cron, SSH keys, API tokens, …). These have no admin page, so
//      the card starts impersonation (#545) and opens the matching
//      /jabali-panel/... page AS that user (same flow as the Users-table
//      "Log in as user" action, ADR-0128).
import { useTranslation } from "react-i18next";
import {
  ApiOutlined,
  AppstoreOutlined,
  ClockCircleOutlined,
  CloudServerOutlined,
  CodeOutlined,
  ContainerOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  FolderOutlined,
  GlobalOutlined,
  HddOutlined,
  KeyOutlined,
  LockOutlined,
  LoginOutlined,
  MailOutlined,
  SettingOutlined,
  ThunderboltOutlined,
  UserOutlined,
} from "@icons";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Card, Col, Row, Skeleton, Space, Statistic, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useState } from "react";
import { Link, useParams } from "react-router";

import { apiClient } from "../../../apiClient";
import { useSetBreadcrumbs } from "../../../components/admin/BreadcrumbContext";
import { adminLinks, ownerCrumbs, ownerLabel } from "../../../components/admin/entityLinks";
import { startImpersonation } from "../../../impersonation";
import { UserLimitsCard } from "./UserLimitsCard";
import { AdminSSHForwardingCard } from "./AdminSSHForwardingCard";

type AdminUser = {
  id: string;
  email: string;
  username?: string | null;
  name_first: string;
  name_last: string;
  is_admin: boolean;
  package_id?: string | null;
};

// Each count comes from the owner-scoped list endpoint. Domains/backups
// paginate and expose `total`; the admin mailbox + docker endpoints return the
// whole owner set, so fall back to the array length.
async function countDomains(id: string): Promise<number> {
  const { data } = await apiClient.get("/domains", { params: { user_id: id, page_size: 1 } });
  return data.total ?? 0;
}
async function countMailboxes(id: string): Promise<number> {
  const { data } = await apiClient.get("/admin/mailboxes", { params: { user_id: id } });
  return data.total ?? data.data?.length ?? 0;
}
async function countDockerApps(id: string): Promise<number> {
  const { data } = await apiClient.get("/admin/docker-apps", { params: { user_id: id } });
  return data.items?.length ?? 0;
}
async function countBackups(id: string): Promise<number> {
  const { data } = await apiClient.get("/admin/backups", { params: { user_id: id, page_size: 1 } });
  return data.total ?? 0;
}

type ResourceCard = {
  key: string;
  label: string;
  icon: React.ReactNode;
  href: string;
  count?: number;
};

// User-shell-only features (#545). No admin page exists for these, so each
// opens the matching /jabali-panel/... page via impersonation.
const USER_PANEL_CARDS: { key: string; label: string; icon: React.ReactNode; path: string }[] = [
  { key: "applications", label: "Applications", icon: <CodeOutlined />, path: "/jabali-panel/applications" },
  { key: "python-apps", label: "Python Apps", icon: <ThunderboltOutlined />, path: "/jabali-panel/python-apps" },
  { key: "databases", label: "Databases", icon: <DatabaseOutlined />, path: "/jabali-panel/databases" },
  { key: "dns", label: "DNS", icon: <GlobalOutlined />, path: "/jabali-panel/dns" },
  { key: "ssl", label: "SSL", icon: <LockOutlined />, path: "/jabali-panel/ssl" },
  { key: "php-settings", label: "PHP Settings", icon: <SettingOutlined />, path: "/jabali-panel/php-settings" },
  { key: "files", label: "Files", icon: <FolderOutlined />, path: "/jabali-panel/files" },
  { key: "disk-usage", label: "Disk Usage", icon: <HddOutlined />, path: "/jabali-panel/disk-usage" },
  { key: "logs", label: "Logs", icon: <FileTextOutlined />, path: "/jabali-panel/logs" },
  { key: "ssh-keys", label: "SSH Keys", icon: <KeyOutlined />, path: "/jabali-panel/ssh-keys" },
  { key: "api-tokens", label: "Personal API Tokens", icon: <ApiOutlined />, path: "/jabali-panel/api-tokens" },
  { key: "cron", label: "Cron Jobs", icon: <ClockCircleOutlined />, path: "/jabali-panel/cron" },
];

export function AdminUserOverview() {
  const { t } = useTranslation();
  const { id = "" } = useParams();
  const [impersonating, setImpersonating] = useState(false);

  const userQ = useQuery({
    queryKey: ["admin-user", id],
    queryFn: async () => (await apiClient.get<AdminUser>(`/users/${id}`)).data,
    enabled: id !== "",
  });

  const countsQ = useQuery({
    queryKey: ["admin-user-resource-counts", id],
    enabled: id !== "",
    queryFn: async () => {
      const [domains, mailboxes, dockerApps, backups] = await Promise.all([
        countDomains(id).catch(() => undefined),
        countMailboxes(id).catch(() => undefined),
        countDockerApps(id).catch(() => undefined),
        countBackups(id).catch(() => undefined),
      ]);
      return { domains, mailboxes, dockerApps, backups };
    },
  });

  // Start act-as for this user, then full-reload into the target page so /me
  // (now carrying the grant header) returns the target and every admin-scoped
  // query cache is dropped cleanly (ADR-0128; same as the Users-table action).
  const openAsUser = async (path: string) => {
    setImpersonating(true);
    try {
      await startImpersonation(id);
      window.location.assign(path);
    } catch {
      feedback.message.error("Could not open the user panel as this user");
      setImpersonating(false);
    }
  };

  useSetBreadcrumbs(userQ.data ? ownerCrumbs(userQ.data) : null);

  if (userQ.isLoading) {
    return <Skeleton active paragraph={{ rows: 4 }} />;
  }
  if (userQ.isError || !userQ.data) {
    return <Alert type="error" showIcon message={t("adminuseroverview.user_not_found")} />;
  }

  const user = userQ.data;
  const counts = countsQ.data;

  const cards: ResourceCard[] = [
    { key: "domains", label: "Domains", icon: <GlobalOutlined />, href: adminLinks.domains(id), count: counts?.domains },
    { key: "mailboxes", label: "Mailboxes", icon: <MailOutlined />, href: adminLinks.mailboxes(id), count: counts?.mailboxes },
    { key: "docker-apps", label: "Docker Apps", icon: <AppstoreOutlined />, href: adminLinks.dockerApps(id), count: counts?.dockerApps },
    { key: "backups", label: "Backups", icon: <CloudServerOutlined />, href: adminLinks.backups(id), count: counts?.backups },
  ];

  const fullName = [user.name_first, user.name_last].filter(Boolean).join(" ");

  return (
    <div>
      <Space align="start" style={{ width: "100%", justifyContent: "space-between", flexWrap: "wrap" }}>
        <div>
          <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 4 }}>
            <UserOutlined /> {ownerLabel(user)}
            {user.is_admin && (
              <Tag color="gold" style={{ marginInlineStart: 8 }}>
                Admin
              </Tag>
            )}
          </Typography.Title>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 20 }}>
            {fullName ? `${fullName} · ` : ""}
            {user.email}
          </Typography.Paragraph>
        </div>
        {!user.is_admin && (
          <Button
            icon={<LoginOutlined />}
            loading={impersonating}
            onClick={() => openAsUser("/jabali-panel")}
          >
            Log in as user
          </Button>
        )}
      </Space>

      <Typography.Title level={5} style={{ marginTop: 8 }}>
        Admin-managed
      </Typography.Title>
      <Row gutter={[16, 16]}>
        {cards.map((c) => (
          <Col key={c.key} xs={24} sm={12} md={8} lg={6}>
            <Link to={c.href}>
              <Card hoverable size="small">
                <Statistic
                  title={
                    <span>
                      {c.icon} {c.label}
                    </span>
                  }
                  value={c.count ?? "—"}
                  loading={countsQ.isLoading}
                />
              </Card>
            </Link>
          </Col>
        ))}
        {user.package_id && (
          <Col xs={24} sm={12} md={8} lg={6}>
            <Link to={adminLinks.packageEdit(user.package_id)}>
              <Card hoverable size="small">
                <Statistic
                  title={
                    <span>
                      <ContainerOutlined /> Package
                    </span>
                  }
                  value="View"
                />
              </Card>
            </Link>
          </Col>
        )}
      </Row>

      {!user.is_admin && (
        <div style={{ marginTop: 24 }}>
          <UserLimitsCard userId={id} />
        </div>
      )}

      {!user.is_admin && (
        <div style={{ marginTop: 24 }}>
          <AdminSSHForwardingCard userId={id} />
        </div>
      )}

      {!user.is_admin && (
        <>
          <Typography.Title level={5} style={{ marginTop: 24 }}>
            User panel
          </Typography.Title>
          <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
            These features live in the user shell. Opening one logs you in as this
            user and takes you to that page.
          </Typography.Paragraph>
          <Row gutter={[16, 16]}>
            {USER_PANEL_CARDS.map((c) => (
              <Col key={c.key} xs={24} sm={12} md={8} lg={6}>
                <Card
                  hoverable
                  size="small"
                  onClick={() => void openAsUser(c.path)}
                  style={{ cursor: "pointer", opacity: impersonating ? 0.6 : 1 }}
                >
                  <Statistic
                    title={
                      <span>
                        {c.icon} {c.label}
                      </span>
                    }
                    value="Open"
                    valueStyle={{ fontSize: 16 }}
                  />
                </Card>
              </Col>
            ))}
          </Row>
        </>
      )}
    </div>
  );
}
