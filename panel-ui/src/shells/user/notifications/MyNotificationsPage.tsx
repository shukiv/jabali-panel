// My Notifications — tenant self-service for per-user notification channels +
// event routing (JAB-171 phase 5). Ownership-scoped to the signed-in user via
// /api/v1/me/notifications/*. The whole surface is gated server-side behind an
// admin switch; when it's off every call returns 403 and we render a calm
// "not enabled" state rather than an error.
//
// Routing is a matrix: the server hands back a labelled catalog of the events
// that fire for this user (GET .../event-catalog), and for each the tenant picks
// which of their channels should receive it — no raw event-kind typing.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, Empty, Popconfirm, Select, Space, Switch, Table, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import type { AxiosError } from "axios";

import { DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from "@icons";
import { apiClient } from "../../../apiClient";
import { useDeleteMutation, useUpdateMutation } from "../../../hooks/useQueries";
import { kindColors, kindLabels, type ChannelKind } from "../../admin/notifications/channelKindConfig";
import { MyChannelDrawer, TENANT_KINDS, type MyChannel } from "./MyChannelDrawer";

const CH_RESOURCE = "me/notifications/channels";
const RT_RESOURCE = "me/notifications/routes";
const CATALOG_RESOURCE = "me/notifications/event-catalog";

type Route = {
  id: string;
  user_id: string;
  event_kind: string;
  channel_id: string;
};

type CatalogEntry = {
  event_kind: string;
  label: string;
  description: string;
  severity: "info" | "warning" | "error" | "critical";
};

const severityColor: Record<CatalogEntry["severity"], string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

function statusOf(err: unknown): number | undefined {
  return (err as AxiosError | undefined)?.response?.status;
}

export function MyNotificationsPage(): JSX.Element {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<MyChannel | undefined>();

  const channelsQ = useQuery<{ items: MyChannel[]; allowedKinds: ChannelKind[] }>({
    queryKey: ["list", CH_RESOURCE],
    queryFn: async () => {
      const { data } = await apiClient.get<{ items?: MyChannel[]; allowed_kinds?: ChannelKind[] }>(
        `/${CH_RESOURCE}`,
      );
      // JAB-326: the effective admin allowlist rides along with the list; fall
      // back to the safe defaults if an older API omits it.
      return { items: data.items ?? [], allowedKinds: data.allowed_kinds ?? TENANT_KINDS };
    },
    retry: false,
  });

  const disabled = statusOf(channelsQ.error) === 403;

  const routesQ = useQuery<Route[]>({
    queryKey: ["list", RT_RESOURCE],
    queryFn: async () => {
      const { data } = await apiClient.get<{ items: Route[] }>(`/${RT_RESOURCE}`);
      return data.items ?? [];
    },
    retry: false,
    enabled: !disabled,
  });

  const catalogQ = useQuery<CatalogEntry[]>({
    queryKey: ["list", CATALOG_RESOURCE],
    queryFn: async () => {
      const { data } = await apiClient.get<{ items: CatalogEntry[] }>(`/${CATALOG_RESOURCE}`);
      return data.items ?? [];
    },
    retry: false,
    enabled: !disabled,
  });

  const updateMutation = useUpdateMutation<MyChannel, { enabled: boolean }>({ resource: CH_RESOURCE });
  const deleteMutation = useDeleteMutation({ resource: CH_RESOURCE });

  const channels = channelsQ.data?.items ?? [];
  const allowedKinds = channelsQ.data?.allowedKinds ?? TENANT_KINDS;

  // event_kind -> [{channelId, routeId}] so the matrix can show current
  // selections and delete the right route row when a channel is unpicked.
  const routesByEvent = useMemo(() => {
    const m = new Map<string, { channelId: string; routeId: string }[]>();
    for (const r of routesQ.data ?? []) {
      const arr = m.get(r.event_kind) ?? [];
      arr.push({ channelId: r.channel_id, routeId: r.id });
      m.set(r.event_kind, arr);
    }
    return m;
  }, [routesQ.data]);

  const toggleEnabled = async (row: MyChannel, next: boolean) => {
    try {
      await updateMutation.mutateAsync({ id: row.id, input: { enabled: next } });
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Toggle failed");
    }
  };

  const removeChannel = async (row: MyChannel) => {
    try {
      await deleteMutation.mutateAsync({ id: row.id });
      feedback.message.success(`Deleted ${row.name}`);
      qc.invalidateQueries({ queryKey: ["list", RT_RESOURCE] });
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const testChannel = async (row: MyChannel) => {
    try {
      await apiClient.post(`/${CH_RESOURCE}/${row.id}/test`);
      feedback.message.success(`Test sent to ${row.name}`);
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Test failed");
    }
  };

  // Reconcile a row's channel multi-select against the stored routes: POST the
  // added channels, DELETE the removed ones, then refetch.
  const applyRoute = async (eventKind: string, nextIds: string[]) => {
    const current = routesByEvent.get(eventKind) ?? [];
    const currentIds = current.map((r) => r.channelId);
    const toAdd = nextIds.filter((id) => !currentIds.includes(id));
    const toRemove = current.filter((r) => !nextIds.includes(r.channelId));
    try {
      for (const id of toAdd) {
        await apiClient.post(`/${RT_RESOURCE}`, { event_kind: eventKind, channel_id: id });
      }
      for (const r of toRemove) {
        await apiClient.delete(`/${RT_RESOURCE}/${r.routeId}`);
      }
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Routing update failed");
    } finally {
      routesQ.refetch();
    }
  };

  if (disabled) {
    return (
      <Card>
        <Alert
          type="info"
          showIcon
          message="Notifications are not enabled"
          description="Per-user notification channels are turned off on this server. Ask your administrator to enable them."
        />
      </Card>
    );
  }

  const channelOptions = channels.map((c) => ({ value: c.id, label: c.name }));

  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Card
        title="My notification channels"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(undefined);
              setDrawerOpen(true);
            }}
          >
            Add channel
          </Button>
        }
      >
        <Table<MyChannel>
          rowKey="id"
          loading={channelsQ.isLoading}
          dataSource={channels}
          pagination={false}
          locale={{ emptyText: <Empty description="No channels yet — add one to get notified." /> }}
          scroll={{ x: "max-content" }}
        >
          <Table.Column
            dataIndex="name"
            title="Name"
            render={(name: string, row: MyChannel) => (
              <a
                onClick={() => {
                  setEditing(row);
                  setDrawerOpen(true);
                }}
              >
                {name}
              </a>
            )}
          />
          <Table.Column
            dataIndex="kind"
            title="Kind"
            render={(k: string) => (
              <Tag color={kindColors[k as keyof typeof kindColors]}>
                {kindLabels[k as keyof typeof kindLabels] ?? k}
              </Tag>
            )}
          />
          <Table.Column
            dataIndex="enabled"
            title="Enabled"
            render={(enabled: boolean, row: MyChannel) => (
              <Switch checked={enabled} onChange={(next) => toggleEnabled(row, next)} />
            )}
          />
          <Table.Column
            title="Actions"
            key="actions"
            render={(_: unknown, row: MyChannel) => (
              <Space>
                <Button size="small" icon={<SendOutlined />} onClick={() => testChannel(row)}>
                  Test
                </Button>
                <Button
                  size="small"
                  icon={<EditOutlined />}
                  onClick={() => {
                    setEditing(row);
                    setDrawerOpen(true);
                  }}
                />
                <Popconfirm title={`Delete ${row.name}?`} onConfirm={() => removeChannel(row)}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            )}
          />
        </Table>
      </Card>

      <Card title="Event routing">
        <Typography.Paragraph type="secondary">
          Pick which of your channels should be notified for each event. Events fire for your own
          account (your certs, backups, quota, and so on).
        </Typography.Paragraph>
        <Table<CatalogEntry>
          rowKey="event_kind"
          loading={catalogQ.isLoading}
          dataSource={catalogQ.data ?? []}
          pagination={false}
          locale={{ emptyText: <Empty description="No routable events." /> }}
          scroll={{ x: "max-content" }}
        >
          <Table.Column<CatalogEntry>
            title="Event"
            dataIndex="label"
            render={(label: string, row) => (
              <div>
                <Typography.Text strong>{label}</Typography.Text>
                <div>
                  <Tag color={severityColor[row.severity]} style={{ marginTop: 4 }}>
                    {row.severity}
                  </Tag>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    <code>{row.event_kind}</code>
                  </Typography.Text>
                </div>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
                  {row.description}
                </Typography.Paragraph>
              </div>
            )}
          />
          <Table.Column<CatalogEntry>
            title="Deliver to"
            key="deliver"
            width={340}
            render={(_: unknown, row) => (
              <Select
                mode="multiple"
                allowClear
                style={{ minWidth: 260, width: "100%" }}
                placeholder={channels.length ? "No channels — event ignored" : "Add a channel first"}
                disabled={channels.length === 0}
                value={(routesByEvent.get(row.event_kind) ?? []).map((r) => r.channelId)}
                onChange={(ids: string[]) => applyRoute(row.event_kind, ids)}
                options={channelOptions}
                loading={routesQ.isFetching}
              />
            )}
          />
        </Table>
      </Card>

      <MyChannelDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        existing={editing}
        allowedKinds={allowedKinds}
      />
      {/* t() kept referenced for future i18n of this page */}
      <span style={{ display: "none" }}>{t("nav.user.notifications", "Notifications")}</span>
    </Space>
  );
}
