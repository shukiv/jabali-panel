// Admin Dashboard — top-level summary. Deep host metrics live on
// /jabali-admin/server-status (M31, ADR-0065). The Dashboard is now a
// short landing card: hostname, top-level health roll-up, three count
// stats (users / domains / mailboxes) and a prominent button into the
// Server Status page.
import { Alert, Button, Card, Col, Masonry, Row, Space, Table, Tag, Typography , Avatar} from "antd";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";

import { ServerOutlined, TeamOutlined, GlobalOutlined, MailOutlined } from "@icons";

import { apiClient } from "../../apiClient";
import { StatCard } from "../../components/StatCard";
import { useListQuery } from "../../hooks/useQueries";
import { useServerStatus } from "../../hooks/useServerStatus";
import { useServerCapabilities } from "../../hooks/useServerCapabilities";

interface UserRow {
  id: string;
  email: string;
  username?: string | null;
  name_first?: string;
  name_last?: string;
  is_admin?: boolean;
  created_at?: string;
}
interface DomainRow {
  id: string;
  name: string;
  created_at?: string;
}
interface ApplicationRow {
  id: string;
  name?: string;
  domain?: string;
  domain_name?: string;
  type?: string;
  app_type?: string;
}
interface PackageRow {
  id: string;
  name: string;
  ssh_enabled?: boolean;
  cgi_enabled?: boolean;
}

const RECENT_LIMIT = 5;

interface CountsResponse {
  users: number;
  domains: number;
  mailboxes: number;
}

const formatCount = (n: number | undefined) =>
  n == null ? "—" : n.toLocaleString();

export const Dashboard = () => {
  const status = useServerStatus();
  const env = status.data;
  const { data: caps } = useServerCapabilities();

  const counts = useQuery({
    queryKey: ["dashboard", "admin-counts"],
    queryFn: async () => {
      try {
        const r = await apiClient.get<CountsResponse>("/admin/counts");
        return r.data;
      } catch {
        return { users: 0, domains: 0, mailboxes: 0 } as CountsResponse;
      }
    },
  });
  const users = { data: counts.data?.users };
  const domains = { data: counts.data?.domains };
  const mailboxes = { data: counts.data?.mailboxes };

  const recentUsers = useListQuery<UserRow>({
    resource: "users",
    params: { pageSize: RECENT_LIMIT, sort: "created_at", order: "desc" },
  });
  const recentDomains = useListQuery<DomainRow>({
    resource: "domains",
    params: { pageSize: RECENT_LIMIT, sort: "created_at", order: "desc" },
  });
  const recentApps = useListQuery<ApplicationRow>({
    resource: "applications",
    params: { pageSize: RECENT_LIMIT, sort: "created_at", order: "desc" },
  });
  const allPackages = useListQuery<PackageRow>({
    resource: "packages",
    params: { pageSize: RECENT_LIMIT, sort: "name", order: "asc" },
  });

  const alerts = env?.alerts ?? [];
  const critical = alerts.filter((a) => a.level === "critical").length;
  const warnings = alerts.filter((a) => a.level === "warning").length;

  let healthTag = <Tag color="green">Healthy</Tag>;
  if (critical > 0) healthTag = <Tag color="red">{critical} critical</Tag>;
  else if (warnings > 0) healthTag = <Tag color="orange">{warnings} warning{warnings === 1 ? "" : "s"}</Tag>;

  return (
    <div>
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} sm={8}>
          <StatCard
            label="Total Users"
            value={formatCount(users.data)}
            icon={<TeamOutlined />}
            iconBg="rgba(22, 119, 255, 0.12)"
            iconColor="#1677ff"
            to="/jabali-admin/users"
          />
        </Col>
        <Col xs={24} sm={8}>
          <StatCard
            label="Active Domains"
            value={formatCount(domains.data)}
            icon={<GlobalOutlined />}
            iconBg="rgba(146, 84, 222, 0.14)"
            iconColor="#9254de"
            to="/jabali-admin/domains"
          />
        </Col>
        <Col xs={24} sm={8}>
          <StatCard
            label="Mailboxes"
            value={formatCount(mailboxes.data)}
            icon={<MailOutlined />}
            iconBg="rgba(250, 140, 22, 0.14)"
            iconColor="#fa8c16"
            to="/jabali-admin/mail"
          />
        </Col>
      </Row>

      <Masonry
        columns={{ xs: 1, sm: 1, md: 1, lg: 2 }}
        gutter={16}
        items={[
          {
            key: "health",
            data: null,
            children: (
              <Card>
                <Space direction="vertical" size={12} style={{ width: "100%" }}>
                  <Space size={12} wrap>
                    <Typography.Title level={4} style={{ margin: 0 }}>
                      {env?.host?.hostname ?? "—"}
                    </Typography.Title>
                    {healthTag}
                  </Space>
                  {caps?.public_ipv4 || caps?.public_ipv6 ? (
                    <Typography.Text type="secondary" style={{ fontSize: 13 }}>
                      Server IP:{" "}
                      {caps?.public_ipv4 ? (
                        <Typography.Text copyable code style={{ fontSize: 13 }}>
                          {caps.public_ipv4}
                        </Typography.Text>
                      ) : null}
                      {caps?.public_ipv6 ? (
                        <Typography.Text
                          copyable
                          code
                          style={{ fontSize: 13, marginLeft: caps?.public_ipv4 ? 8 : 0 }}
                        >
                          {caps.public_ipv6}
                        </Typography.Text>
                      ) : null}
                    </Typography.Text>
                  ) : null}
                  <Typography.Text type="secondary">
                    Top-level summary. For live metrics, services, network, and processes,
                    see Server Status.
                  </Typography.Text>
                  <Link to="/jabali-admin/server-status">
                    <Button type="primary" icon={<ServerOutlined />}>
                      View server status →
                    </Button>
                  </Link>
                </Space>
              </Card>
            ),
          },
          {
            key: "users",
            data: null,
            children: (
              <Card
                title="Recent Users"
                size="small"
                extra={<Link to="/jabali-admin/users"><Button type="primary" size="small">View all</Button></Link>}
              >
                <Table<UserRow>
                  size="small"
                  rowKey="id"
                  pagination={false}
                  loading={recentUsers.isLoading}
                  dataSource={recentUsers.items}
                  locale={{ emptyText: "No users yet" }}
                  columns={[
                    {
                      title: "Username",
                      key: "username",
                      ellipsis: true,
                      render: (_: unknown, r: UserRow) => {
                        const name = [r.name_first, r.name_last]
                          .filter(Boolean)
                          .join(" ")
                          .trim();
                        const label = r.username || r.email || "?";
                        return (
                          <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
                            <Avatar size="small" style={{ flexShrink: 0, backgroundColor: "#1677ff" }}>
                              {label.charAt(0).toUpperCase()}
                            </Avatar>
                            <div style={{ minWidth: 0 }}>
                            <div
                              style={{
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                            >
                              {r.username || r.email}
                            </div>
                            {name && (
                              <Typography.Text
                                type="secondary"
                                style={{ fontSize: 12 }}
                                ellipsis
                              >
                                {name}
                              </Typography.Text>
                            )}
                            </div>
                          </div>
                        );
                      },
                    },
                    {
                      title: "Role",
                      dataIndex: "is_admin",
                      width: 90,
                      render: (v: boolean) =>
                        v ? <Tag color="blue">Admin</Tag> : <Tag>User</Tag>,
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "domains",
            data: null,
            children: (
              <Card
                title="Recent Domains"
                size="small"
                extra={<Link to="/jabali-admin/domains"><Button type="primary" size="small">View all</Button></Link>}
              >
                <Table<DomainRow>
                  size="small"
                  rowKey="id"
                  pagination={false}
                  loading={recentDomains.isLoading}
                  dataSource={recentDomains.items}
                  locale={{ emptyText: "No domains yet" }}
                  columns={[
                    {
                      title: "Domain",
                      dataIndex: "name",
                      ellipsis: true,
                      render: (v: string) => (
                        <span>
                          <GlobalOutlined style={{ marginRight: 8, color: "#1677ff" }} />
                          {v}
                        </span>
                      ),
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "apps",
            data: null,
            children: (
              <Card
                title="Recent Applications"
                size="small"
                extra={<Link to="/jabali-admin/applications"><Button type="primary" size="small">View all</Button></Link>}
              >
                <Table<ApplicationRow>
                  size="small"
                  rowKey="id"
                  pagination={false}
                  loading={recentApps.isLoading}
                  dataSource={recentApps.items}
                  locale={{ emptyText: "No applications yet" }}
                  columns={[
                    {
                      title: "Name",
                      dataIndex: "name",
                      ellipsis: true,
                      render: (v: string | undefined, r) =>
                        v ?? r.domain_name ?? r.domain ?? "—",
                    },
                    {
                      title: "Type",
                      dataIndex: "type",
                      width: 110,
                      render: (v: string | undefined, r) => v ?? r.app_type ?? "—",
                    },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: "packages",
            data: null,
            children: (
              <Card
                title="Hosting Packages"
                size="small"
                extra={<Link to="/jabali-admin/packages"><Button type="primary" size="small">View all</Button></Link>}
              >
                <Table<PackageRow>
                  size="small"
                  rowKey="id"
                  pagination={false}
                  loading={allPackages.isLoading}
                  dataSource={allPackages.items}
                  locale={{ emptyText: "No packages yet" }}
                  columns={[
                    { title: "Name", dataIndex: "name", ellipsis: true },
                    {
                      title: "SSH",
                      dataIndex: "ssh_enabled",
                      width: 80,
                      render: (v: boolean) =>
                        v ? <Tag color="green">on</Tag> : <Tag>off</Tag>,
                    },
                  ]}
                />
              </Card>
            ),
          },
          ...(critical > 0
            ? [
                {
                  key: "alert",
                  data: null,
                  children: (
                    <Alert
                      type="error"
                      showIcon
                      message={`${critical} critical issue${critical === 1 ? "" : "s"} on host`}
                      description={
                        <Link to="/jabali-admin/server-status">
                          Open Server Status to investigate →
                        </Link>
                      }
                    />
                  ),
                },
              ]
            : []),
        ]}
      />
    </div>
  );
};
