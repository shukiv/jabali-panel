// My Notifications — tenant self-service for per-user notification channels +
// event routing (JAB-171 phase 5). Ownership-scoped to the signed-in user via
// /api/v1/me/notifications/*. The whole surface is gated server-side behind an
// admin switch; when it's off every call returns 403 and we render a calm
// "not enabled" state rather than an error.
//
// Wire paths:
//   GET/POST/PATCH/DELETE  /me/notifications/channels[/:id]
//   POST                   /me/notifications/channels/:id/test
//   GET/POST/DELETE        /me/notifications/routes[/:id]
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import type { AxiosError } from "axios";

import { DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from "@icons";
import { apiClient } from "../../../apiClient";
import { useDeleteMutation, useUpdateMutation } from "../../../hooks/useQueries";
import { kindColors, kindLabels } from "../../admin/notifications/channelKindConfig";
import { MyChannelDrawer, type MyChannel } from "./MyChannelDrawer";

const CH_RESOURCE = "me/notifications/channels";
const RT_RESOURCE = "me/notifications/routes";

type Route = {
  id: string;
  user_id: string;
  event_kind: string;
  channel_id: string;
};

function statusOf(err: unknown): number | undefined {
  return (err as AxiosError | undefined)?.response?.status;
}

export function MyNotificationsPage(): JSX.Element {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<MyChannel | undefined>();

  const channelsQ = useQuery<MyChannel[]>({
    queryKey: ["list", CH_RESOURCE],
    queryFn: async () => {
      const { data } = await apiClient.get<{ items: MyChannel[] }>(`/${CH_RESOURCE}`);
      return data.items ?? [];
    },
    retry: false,
  });

  const routesQ = useQuery<Route[]>({
    queryKey: ["list", RT_RESOURCE],
    queryFn: async () => {
      const { data } = await apiClient.get<{ items: Route[] }>(`/${RT_RESOURCE}`);
      return data.items ?? [];
    },
    retry: false,
    // The channels query already surfaces the disabled state; keep this quiet.
    enabled: statusOf(channelsQ.error) !== 403,
  });

  const disabled = statusOf(channelsQ.error) === 403;

  const updateMutation = useUpdateMutation<MyChannel, { enabled: boolean }>({ resource: CH_RESOURCE });
  const deleteMutation = useDeleteMutation({ resource: CH_RESOURCE });

  const channels = channelsQ.data ?? [];
  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? id;

  const toggleEnabled = async (row: MyChannel, next: boolean) => {
    try {
      await updateMutation.mutateAsync({ id: row.id, input: { enabled: next } });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Toggle failed");
    }
  };

  const removeChannel = async (row: MyChannel) => {
    try {
      await deleteMutation.mutateAsync({ id: row.id });
      message.success(`Deleted ${row.name}`);
      qc.invalidateQueries({ queryKey: ["list", RT_RESOURCE] });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const testChannel = async (row: MyChannel) => {
    try {
      await apiClient.post(`/${CH_RESOURCE}/${row.id}/test`);
      message.success(`Test sent to ${row.name}`);
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Test failed");
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
          Route a server event to one of your channels. When that event fires for you, the channel is notified.
        </Typography.Paragraph>
        <RouteAdder channels={channels} onAdded={() => routesQ.refetch()} />
        <Table<Route>
          rowKey="id"
          style={{ marginTop: 16 }}
          loading={routesQ.isLoading}
          dataSource={routesQ.data ?? []}
          pagination={false}
          locale={{ emptyText: <Empty description="No routes yet." /> }}
          scroll={{ x: "max-content" }}
        >
          <Table.Column dataIndex="event_kind" title="Event" render={(e: string) => <Tag>{e}</Tag>} />
          <Table.Column dataIndex="channel_id" title="Channel" render={(id: string) => channelName(id)} />
          <Table.Column
            title="Actions"
            key="actions"
            render={(_: unknown, row: Route) => (
              <Popconfirm
                title="Remove this route?"
                onConfirm={async () => {
                  try {
                    await apiClient.delete(`/${RT_RESOURCE}/${row.id}`);
                    routesQ.refetch();
                  } catch (err) {
                    message.error(err instanceof Error ? err.message : "Remove failed");
                  }
                }}
              >
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            )}
          />
        </Table>
      </Card>

      <MyChannelDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} existing={editing} />
      {/* t() kept referenced for future i18n of this page */}
      <span style={{ display: "none" }}>{t("nav.user.notifications", "Notifications")}</span>
    </Space>
  );
}

function RouteAdder({ channels, onAdded }: { channels: MyChannel[]; onAdded: () => void }): JSX.Element {
  const [form] = Form.useForm<{ event_kind: string; channel_id: string }>();
  const [saving, setSaving] = useState(false);

  const submit = async (v: { event_kind: string; channel_id: string }) => {
    setSaving(true);
    try {
      await apiClient.post(`/${RT_RESOURCE}`, {
        event_kind: v.event_kind.trim(),
        channel_id: v.channel_id,
      });
      message.success("Route added");
      form.resetFields();
      onAdded();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Add route failed");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form form={form} layout="inline" onFinish={submit}>
      <Form.Item
        name="event_kind"
        rules={[{ required: true, message: "Event kind required" }]}
        style={{ minWidth: 220 }}
      >
        <Input placeholder="event kind, e.g. backup.completed" />
      </Form.Item>
      <Form.Item name="channel_id" rules={[{ required: true, message: "Pick a channel" }]} style={{ minWidth: 200 }}>
        <Select
          placeholder="Channel"
          options={channels.map((c) => ({ value: c.id, label: c.name }))}
        />
      </Form.Item>
      <Form.Item>
        <Button type="primary" htmlType="submit" loading={saving} icon={<PlusOutlined />}>
          Add route
        </Button>
      </Form.Item>
    </Form>
  );
}
