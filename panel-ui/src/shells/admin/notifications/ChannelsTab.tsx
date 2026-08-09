// ChannelsTab — admin list of notification channels (M14 Step 6/9).
//
// Rendered inside NotificationsTabsPage Card.tabList. Strips its own
// page-level header; the "Add channel" button stays here because it's
// tab-specific (the History tab has a different action).
import { useTranslation } from "react-i18next";
import { Button, Space, Switch, Table, Tag } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { useState } from "react";

import { DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from "@icons";

import { RowActions } from "../../../components/RowActions";
import { apiClient } from "../../../apiClient";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  useDeleteMutation,
  useUpdateMutation,
} from "../../../hooks/useQueries";
import { useTableURL } from "../../../hooks/useTableURL";
import { AdminChannelDrawer, type NotificationChannel } from "./AdminChannelDrawer";
import { kindColors, kindLabels } from "./channelKindConfig";

const RESOURCE = "admin/notifications/channels";

export const ChannelsTab = () => {
  const { t } = useTranslation();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel | undefined>();

  const query = useTableURL<NotificationChannel>({
    resource: RESOURCE,
    defaultSort: "created_at",
    defaultOrder: "desc",
  });
  const updateMutation = useUpdateMutation<NotificationChannel, { enabled: boolean }>({ resource: RESOURCE });
  const deleteMutation = useDeleteMutation({ resource: RESOURCE });

  const handleToggleEnabled = async (row: NotificationChannel, next: boolean) => {
    try {
      await updateMutation.mutateAsync({ id: row.id, input: { enabled: next } });
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Toggle failed");
    }
  };

  const handleDelete = async (row: NotificationChannel) => {
    try {
      await deleteMutation.mutateAsync({ id: row.id });
      feedback.message.success(`Deleted ${row.name}`);
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const handleTest = async (row: NotificationChannel) => {
    try {
      const res = await apiClient.post<{ delivered?: boolean }>(`/${RESOURCE}/${row.id}/test`);
      if (res.data?.delivered) {
        feedback.message.success(`Test delivered to ${row.name}`);
      } else {
        feedback.message.success(`Test queued for ${row.name} — see the History tab for the result`);
      }
    } catch (err) {
      // Synchronous send surfaces the real delivery error (e.g. SMTP auth).
      feedback.message.error(err instanceof Error ? err.message : "Test failed");
    }
  };

  const openCreate = () => {
    setEditing(undefined);
    setDrawerOpen(true);
  };

  const openEdit = (row: NotificationChannel) => {
    setEditing(row);
    setDrawerOpen(true);
  };

  return (
    <div>
      <Space style={{ marginBottom: 16, width: "100%", justifyContent: "flex-end" }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          Add channel
        </Button>
      </Space>

      <SearchableTableStringQ<NotificationChannel>
        rowKey="id"
        loading={query.isLoading}
        dataSource={query.items}
        initialSearch={query.params.q}
        searchPlaceholder="Search by name"
        onSearchChange={(q) => query.setParams({ q, page: 1 })}
        pagination={{
          current: query.params.page,
          pageSize: query.params.pageSize,
          total: query.total,
          showSizeChanger: true,
          // GH #232: controlled pagination needs onChange or page/size
          // clicks are inert (current/pageSize are pinned to query state).
          onChange: (page, pageSize) => query.setParams({ page, pageSize }),
        }}
        scroll={{ x: "max-content" }}
      >
        <Table.Column
          dataIndex="name"
          title={t("channelstab.name")}
          render={(name: string, row: NotificationChannel) => (
            <a onClick={() => openEdit(row)}>{name}</a>
          )}
        />
        <Table.Column
          dataIndex="kind"
          title={t("channelstab.kind")}
          render={(k: string) => (
            <Tag color={kindColors[k as keyof typeof kindColors]}>
              {kindLabels[k as keyof typeof kindLabels] ?? k}
            </Tag>
          )}
        />
        <Table.Column
          dataIndex="user_id"
          title="Owner"
          render={(userID: string | null | undefined) =>
            userID ? (
              <Tag color="blue" title={userID}>
                Tenant
              </Tag>
            ) : (
              <Tag>Server-wide</Tag>
            )
          }
        />
        <Table.Column
          dataIndex="enabled"
          title={t("channelstab.enabled")}
          render={(enabled: boolean, row: NotificationChannel) => (
            <Switch checked={enabled} onChange={(next) => handleToggleEnabled(row, next)} />
          )}
        />
        <Table.Column
          title={t("channelstab.actions")}
          key="actions"
          render={(_: unknown, row: NotificationChannel) => (
            <RowActions
              actions={[
                { key: "test", label: "Test", icon: <SendOutlined />, onClick: () => handleTest(row) },
                { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => openEdit(row) },
                { key: "delete", label: "Delete", icon: <DeleteOutlined />, danger: true, onClick: () => handleDelete(row), confirm: { title: `Delete ${row.name}?` } },
              ]}
            />
          )}
        />
      </SearchableTableStringQ>

      <AdminChannelDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} existing={editing} />
    </div>
  );
};
