// AdminSecurityEgress — M34 admin tab. Lists every user with their
// egress policy state, drop count, and edit Drawer. Pending requests
// panel below the table; approve/deny inline.
//
// Backed by panel-api/internal/api/user_egress.go.
import { useTranslation } from "react-i18next";
import {
  App,
  Alert,
  Button,
  Card,
  Drawer,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { CheckOutlined, CloseOutlined } from "@icons";
import { shortDateTime } from "../../../utils/datetime";
import { RowActions } from "../../../components/RowActions";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { EgressMaturePromotion } from "./EgressMaturePromotion";
import { Sparkline } from "../../../components/Sparkline";
import { useListQuery } from "../../../hooks/useQueries";
import {
  type EgressDestination,
  type EgressRequest,
  type EgressState,
  useDecideEgressRequest,
  useEgressSummary,
  usePendingEgressRequests,
  useUpdateUserEgress,
  useUserEgressPolicy,
} from "../../../hooks/useUserEgress";

type UserRow = {
  id: string;
  username?: string | null;
  email: string;
  is_admin: boolean;
};

const STATE_TAG: Record<EgressState, { color: string; label: string }> = {
  off: { color: "default", label: "OFF" },
  learning: { color: "gold", label: "LEARNING" },
  enforced: { color: "green", label: "ENFORCED" },
};

const STATE_OPTIONS: { value: EgressState; label: string }[] = [
  { value: "off", label: "Off (no filter)" },
  { value: "learning", label: "Learning (log only)" },
  { value: "enforced", label: "Enforced (drop)" },
];

const PROTOCOL_OPTIONS = [
  { value: "tcp", label: "TCP" },
  { value: "udp", label: "UDP" },
];

const renderStateTag = (state: EgressState) => {
  const t = STATE_TAG[state] ?? STATE_TAG.enforced;
  return <Tag color={t.color}>{t.label}</Tag>;
};

export const AdminSecurityEgress = () => {
  const { t } = useTranslation();
  const summary = useEgressSummary();
  const pending = usePendingEgressRequests();
  const usersQuery = useListQuery<UserRow>({
    resource: "users",
    params: { page: 1, pageSize: 200 },
  });

  const [editingUserID, setEditingUserID] = useState<string | undefined>(undefined);

  const nonAdminUsers = (usersQuery.items ?? []).filter((u: UserRow) => !u.is_admin);

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message={t("adminsecurityegress.per_user_php_fpm_egress_firewall")}
        description={
          <Typography.Paragraph style={{ marginBottom: 0 }}>
            Kernel-level packet filter via nftables + cgroup v2 socket match. ENFORCED
            users have outbound traffic dropped if it doesn't hit the default allowlist
            (loopback / DNS / HTTP-S / SMTP submission) or a per-user override.
            LEARNING users have drops logged + counted but allowed (7-day soak before
            auto-flip). OFF disables the filter entirely (break-glass).
          </Typography.Paragraph>
        }
      />

      <Card size="small">
        <Space size="large" wrap>
          <Statistic
            title={t("adminsecurityegress.enforced_users")}
            value={summary.data?.state_counts.enforced ?? 0}
          />
          <Statistic
            title={t("adminsecurityegress.learning_users")}
            value={summary.data?.state_counts.learning ?? 0}
          />
          <Statistic
            title={t("adminsecurityegress.off_users")}
            value={summary.data?.state_counts.off ?? 0}
          />
          <Statistic
            title={t("adminsecurityegress.total_drops_last_tick")}
            value={summary.data?.total_drops ?? 0}
          />
        </Space>
      </Card>

      <EgressMaturePromotion />

      <Card size="small" title={t("adminsecurityegress.per_user_policy")}>
        <Table<UserRow>
          dataSource={nonAdminUsers}
          rowKey="id"
          pagination={{ pageSize: 50, hideOnSinglePage: true }}
          loading={usersQuery.isLoading}
          scroll={{ x: "max-content" }}
        >
          <Table.Column<UserRow>
            title={t("adminsecurityegress.user")}
            dataIndex="username"
            render={(_, r) => r.username ?? r.email}
          />
          <Table.Column<UserRow>
            title={t("adminsecurityegress.egress_state")}
            render={(_, r) => <UserStateCell userID={r.id} />}
          />
          <Table.Column<UserRow>
            title={t("adminsecurityegress.drops_last_tick")}
            render={(_, r) => <UserDropsCell userID={r.id} />}
          />
          <Table.Column<UserRow>
            title={t("adminsecurityegress.drops_24h")}
            render={(_, r) => <UserDrops24hSparkline userID={r.id} />}
          />
          <Table.Column<UserRow>
            title={t("adminsecurityegress.actions")}
            render={(_, r) => (
              <Button size="small" onClick={() => setEditingUserID(r.id)}>
                Edit policy
              </Button>
            )}
          />
        </Table>
      </Card>

      <Card size="small" title={`Pending requests (${pending.data?.total ?? 0})`}>
        <PendingRequestsTable rows={pending.data?.data ?? []} />
      </Card>

      <UserEgressDrawer
        open={!!editingUserID}
        userID={editingUserID}
        onClose={() => setEditingUserID(undefined)}
      />
    </Space>
  );
};

const UserStateCell = ({ userID }: { userID: string }) => {
  const q = useUserEgressPolicy(userID);
  if (q.isLoading) return <span>—</span>;
  if (!q.data) return <Tag>unknown</Tag>;
  return renderStateTag(q.data.state);
};

interface EgressDropEvent {
  dest: string;
  port: number;
  proto: string;
  count: number;
  last_seen: string;
}

// DropEventsDrawer (GH #713) — drill-down into WHAT a user was blocked from,
// parsed from the nft drop log (the count/sparkline alone can't show this).
const DropEventsDrawer = ({
  userID,
  open,
  onClose,
}: {
  userID: string;
  open: boolean;
  onClose: () => void;
}) => {
  const q = useQuery({
    queryKey: ["admin/users", userID, "egress/drop-events"],
    queryFn: async () =>
      (await apiClient.get<{ source: string; events: EgressDropEvent[] }>(
        `/admin/users/${userID}/egress/drop-events`,
      )).data,
    enabled: open,
  });
  const qc = useQueryClient();
  const { message } = App.useApp();
  // GH #713 Phase 2: one-click "allow this destination" — fetch the policy,
  // append to allowed_extra (preserving state), PUT it back.
  const allow = useMutation({
    mutationFn: async (ev: EgressDropEvent) => {
      const cur = (
        await apiClient.get<{ state: string; allowed_extra: unknown[] }>(`/admin/users/${userID}/egress`)
      ).data;
      const cidr = ev.dest.includes(":") ? `${ev.dest}/128` : `${ev.dest}/32`;
      const extra = {
        cidr,
        port: ev.port || undefined,
        protocol: (ev.proto || "tcp").toLowerCase(),
        comment: "allowed from drop drill-down",
      };
      await apiClient.put(`/admin/users/${userID}/egress`, {
        state: cur.state,
        allowed_extra: [...(cur.allowed_extra ?? []), extra],
      });
    },
    onSuccess: () => {
      message.success("Destination allowed — the reconciler applies it shortly");
      void qc.invalidateQueries({ queryKey: ["admin/users", userID, "egress/drop-events"] });
    },
    onError: (e) => message.error(String((e as Error).message)),
  });
  // GH #713 Phase 2: client-side filters (dest substring + protocol).
  const [destFilter, setDestFilter] = useState("");
  const [protoFilter, setProtoFilter] = useState<string | undefined>(undefined);
  const events = (q.data?.events ?? []).filter(
    (e) =>
      (!destFilter || e.dest.includes(destFilter) || String(e.port).includes(destFilter)) &&
      (!protoFilter || (e.proto || "").toUpperCase() === protoFilter),
  );
  const protos = Array.from(new Set((q.data?.events ?? []).map((e) => (e.proto || "?").toUpperCase())));
  return (
    <Drawer title="Blocked egress — recent drops" width={620} open={open} onClose={onClose}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="Data source"
        description={q.data?.source ?? "nftables drop log (kernel ring buffer)."}
      />
      <Space wrap style={{ marginBottom: 12 }}>
        <Input
          allowClear
          placeholder="Filter destination or port"
          style={{ width: 240 }}
          value={destFilter}
          onChange={(e) => setDestFilter(e.target.value)}
        />
        <Select
          allowClear
          placeholder="Protocol"
          style={{ width: 130 }}
          value={protoFilter}
          onChange={(v) => setProtoFilter(v)}
          options={protos.map((p) => ({ label: p, value: p }))}
        />
      </Space>
      <Table<EgressDropEvent>
        rowKey={(r) => `${r.dest}:${r.port}:${r.proto}`}
        loading={q.isLoading}
        dataSource={events}
        size="small"
        pagination={false}
        scroll={{ x: "max-content" }}
        locale={{ emptyText: "No drops logged (rate-limited 5/min; may have rotated)" }}
        columns={[
          { title: "Destination", dataIndex: "dest" },
          { title: "Port", dataIndex: "port", width: 80, render: (v: number) => v || "—" },
          { title: "Proto", dataIndex: "proto", width: 80, render: (v: string) => <Tag>{v || "?"}</Tag> },
          { title: "Count", dataIndex: "count", width: 80 },
          { title: "Last seen", dataIndex: "last_seen", width: 180, render: (v: string) => v || "—" },
          {
            title: "",
            key: "allow",
            width: 90,
            render: (_: unknown, r: EgressDropEvent) => (
              <Button size="small" loading={allow.isPending} onClick={() => allow.mutate(r)}>
                Allow
              </Button>
            ),
          },
        ]}
      />
    </Drawer>
  );
};

const UserDropsCell = ({ userID }: { userID: string }) => {
  const q = useUserEgressPolicy(userID);
  const [open, setOpen] = useState(false);
  if (q.isLoading) return <span>—</span>;
  const count = q.data?.drop_count_24h ?? 0;
  return (
    <>
      <Button type="link" size="small" style={{ padding: 0 }} disabled={count === 0} onClick={() => setOpen(true)}>
        {count}
      </Button>
      <DropEventsDrawer userID={userID} open={open} onClose={() => setOpen(false)} />
    </>
  );
};

// UserDrops24hSparkline — M34 deep stats. 24 hourly buckets fetched
// from /admin/users/:id/egress/drops-24h, rendered via the shared
// inline-SVG Sparkline.
const UserDrops24hSparkline = ({ userID }: { userID: string }) => {
  const q = useQuery<{ buckets: { at: string; drops: number }[] }>({
    queryKey: ["admin/users", userID, "egress/drops-24h"],
    queryFn: async () => {
      const { data } = await apiClient.get(`/admin/users/${userID}/egress/drops-24h`);
      return data;
    },
    refetchInterval: 5 * 60 * 1000,
  });
  if (q.isLoading || !q.data) return <span>—</span>;
  const series = q.data.buckets.map((b) => ({ x: b.at, y: b.drops }));
  return <Sparkline data={series} width={140} height={28} />;
};

type UserEgressDrawerProps = {
  open: boolean;
  userID: string | undefined;
  onClose: () => void;
};

type FormValues = {
  state: EgressState;
  allowed_extra: EgressDestination[];
};

const UserEgressDrawer = ({ open, userID, onClose }: UserEgressDrawerProps) => {
  const { t } = useTranslation();
  const policy = useUserEgressPolicy(userID);
  const update = useUpdateUserEgress(userID ?? "");
  const [form] = Form.useForm<FormValues>();

  useEffect(() => {
    if (policy.data) {
      form.setFieldsValue({
        state: policy.data.state,
        allowed_extra: policy.data.allowed_extra ?? [],
      });
    }
  }, [policy.data, form]);

  const onFinish = async (values: FormValues) => {
    if (!userID) return;
    try {
      await update.mutateAsync({
        state: values.state,
        allowed_extra: (values.allowed_extra ?? []).map((e) => ({
          ...e,
          protocol: e.protocol ?? "tcp",
        })),
      });
      message.success("Egress policy updated");
      onClose();
    } catch (e) {
      message.error("Failed to update policy");
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={640}
      title={`Egress policy${policy.data?.user_id ? ` — ${policy.data.user_id}` : ""}`}
      destroyOnClose
    >
      {policy.isLoading ? (
        <Typography.Text>Loading...</Typography.Text>
      ) : (
        <Form<FormValues> form={form} layout="vertical" onFinish={onFinish}>
          <Form.Item label={t("adminsecurityegress.state")} name="state" rules={[{ required: true }]}>
            <Select options={STATE_OPTIONS} />
          </Form.Item>

          <Typography.Title level={5}>Allowed destinations (extras)</Typography.Title>
          <Typography.Paragraph type="secondary">
            Beyond the default allowlist (loopback / DNS / HTTP-S / SMTP submission).
            Maximum 50 entries.
          </Typography.Paragraph>

          <Form.List name="allowed_extra">
            {(fields, { add, remove }) => (
              <>
                {fields.map((field) => (
                  <Space key={field.key} align="baseline" wrap>
                    <Form.Item
                      label={t("adminsecurityegress.cidr")}
                      name={[field.name, "cidr"]}
                      rules={[{ required: true, message: "Required" }]}
                    >
                      <Input placeholder="203.0.113.0/24" style={{ width: 200 }} />
                    </Form.Item>
                    <Form.Item label={t("adminsecurityegress.port")} name={[field.name, "port"]}>
                      <InputNumber min={1} max={65535} style={{ width: 100 }} />
                    </Form.Item>
                    <Form.Item label={t("adminsecurityegress.protocol")} name={[field.name, "protocol"]}>
                      <Select
                        options={PROTOCOL_OPTIONS}
                        defaultValue="tcp"
                        style={{ width: 100 }}
                      />
                    </Form.Item>
                    <Form.Item label={t("adminsecurityegress.comment")} name={[field.name, "comment"]}>
                      <Input style={{ width: 200 }} />
                    </Form.Item>
                    <Button danger size="small" onClick={() => remove(field.name)}>
                      Remove
                    </Button>
                  </Space>
                ))}
                <Button onClick={() => add({ protocol: "tcp" })}>+ Add destination</Button>
              </>
            )}
          </Form.List>

          <Form.Item style={{ marginTop: 24 }}>
            <Space>
              <Button type="primary" htmlType="submit" loading={update.isPending}>
                Save
              </Button>
              <Button onClick={onClose}>Cancel</Button>
            </Space>
          </Form.Item>
        </Form>
      )}
    </Drawer>
  );
};

const PendingRequestsTable = ({ rows }: { rows: EgressRequest[] }) => {
  const { t } = useTranslation();
  const decide = useDecideEgressRequest();
  if (rows.length === 0) {
    return <Typography.Text type="secondary">No pending requests.</Typography.Text>;
  }
  return (
    <Table<EgressRequest> dataSource={rows} rowKey="id" pagination={false} size="small">
      <Table.Column<EgressRequest> title={t("adminsecurityegress.user")} dataIndex="user_id" />
      <Table.Column<EgressRequest> title={t("adminsecurityegress.cidr")} dataIndex="cidr" />
      <Table.Column<EgressRequest>
        title={t("adminsecurityegress.port")}
        render={(_, r) => r.port ?? "—"}
      />
      <Table.Column<EgressRequest> title={t("adminsecurityegress.proto")} dataIndex="protocol" />
      <Table.Column<EgressRequest>
        title={t("adminsecurityegress.reason")}
        dataIndex="reason"
        render={(s: string) => (
          <Typography.Text style={{ maxWidth: 280 }} ellipsis={{ tooltip: s }}>
            {s}
          </Typography.Text>
        )}
      />
      <Table.Column<EgressRequest>
        title={t("adminsecurityegress.submitted")}
        dataIndex="created_at"
        render={(s: string) => shortDateTime(s)}
      />
      <Table.Column<EgressRequest>
        title={t("adminsecurityegress.actions")}
        render={(_, r) => (
          <RowActions
            actions={[
              {
                key: "approve",
                label: "Approve",
                icon: <CheckOutlined />,
                onClick: () =>
                  decide.mutate(
                    { id: r.id, decision: "approve" },
                    { onSuccess: () => message.success("Request approved") },
                  ),
                confirm: { title: "Approve and add to user's allowlist?", okText: "Approve" },
              },
              {
                key: "deny",
                label: "Deny",
                icon: <CloseOutlined />,
                danger: true,
                onClick: () =>
                  decide.mutate(
                    { id: r.id, decision: "deny" },
                    { onSuccess: () => message.info("Request denied") },
                  ),
                confirm: { title: "Deny request?", okText: "Deny" },
              },
            ]}
          />
        )}
      />
    </Table>
  );
};
