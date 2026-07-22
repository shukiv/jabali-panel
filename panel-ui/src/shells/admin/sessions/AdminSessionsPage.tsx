// AdminSessionsPage — GH #338. Live panel (Kratos) login sessions: user, source
// IP, channel, auth level, when authenticated / expires, with a revoke action.
import { useTranslation } from "react-i18next";
import { App, Button, Card, Popconfirm, Space, Table, Tag, Tooltip, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { extractApiError } from "../../../apiErrors";

interface SessionRow {
  id: string;
  email: string;
  username: string;
  is_admin: boolean;
  ip: string;
  user_agent: string;
  channel: string;
  aal: string;
  authenticated_at: string;
  expires_at: string;
}

const fmt = (s: string): string => {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
};

export function AdminSessionsPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ["admin-sessions"],
    queryFn: async () => (await apiClient.get<{ sessions: SessionRow[] }>("/admin/sessions")).data.sessions ?? [],
    refetchInterval: 15000,
  });

  const revoke = useMutation({
    mutationFn: async (id: string) => apiClient.delete(`/admin/sessions/${id}`),
    onSuccess: () => {
      message.success("Session revoked");
      void qc.invalidateQueries({ queryKey: ["admin-sessions"] });
    },
    onError: (e) => message.error(extractApiError(e) ?? "Revoke failed"),
  });

  return (
    <Card
      title={t("adminsessionspage.active_sessions")}
      extra={<Typography.Text type="secondary">Live panel logins (auto-refreshes)</Typography.Text>}
    >
      <Table<SessionRow>
        rowKey="id"
        loading={isLoading}
        dataSource={data ?? []}
        size="small"
        scroll={{ x: "max-content" }}
        pagination={false}
        columns={[
          {
            title: "User",
            key: "user",
            render: (_, r) => (
              <Space direction="vertical" size={0}>
                <span>
                  {r.username || r.email || "—"}
                  {r.is_admin && (
                    <Tag color="gold" style={{ marginLeft: 8 }}>
                      admin
                    </Tag>
                  )}
                </span>
                {r.email && r.username && (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {r.email}
                  </Typography.Text>
                )}
              </Space>
            ),
          },
          {
            title: "Channel",
            dataIndex: "channel",
            width: 110,
            render: (v: string) => (
              <Tag color={{ panel: "blue", ssh: "green", sftp: "gold" }[v] ?? "default"}>{v}</Tag>
            ),
          },
          { title: "Source IP", dataIndex: "ip", width: 150, render: (v: string) => v || "—" },
          {
            title: "Client",
            dataIndex: "user_agent",
            ellipsis: true,
            render: (v: string) => (
              <Tooltip title={v}>
                <Typography.Text style={{ maxWidth: 240 }} ellipsis>
                  {v || "—"}
                </Typography.Text>
              </Tooltip>
            ),
          },
          {
            title: "2FA",
            dataIndex: "aal",
            width: 80,
            render: (v: string) => (v === "aal2" ? <Tag color="green">2FA</Tag> : <Tag>1FA</Tag>),
          },
          { title: "Authenticated", dataIndex: "authenticated_at", width: 180, render: fmt },
          { title: "Expires", dataIndex: "expires_at", width: 180, render: fmt },
          {
            title: "Action",
            key: "action",
            width: 110,
            render: (_, r) => (
              <Popconfirm
                title={t("adminsessionspage.revoke_this_session")}
                description={t("adminsessionspage.the_user_will_be_signed_out_of_the_panel")}
                okText={t("adminsessionspage.revoke")}
                okButtonProps={{ danger: true }}
                onConfirm={() => revoke.mutate(r.id)}
              >
                <Button danger size="small" loading={revoke.isPending}>
                  Revoke
                </Button>
              </Popconfirm>
            ),
          },
        ]}
      />
    </Card>
  );
}
