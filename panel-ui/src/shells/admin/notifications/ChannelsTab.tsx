// ChannelsTab — admin list of notification channels (M14 Step 6/9).
//
// Rendered inside NotificationsTabsPage Card.tabList. Strips its own
// page-level header; the "Add channel" button stays here because it's
// tab-specific (the History tab has a different action).
//
// The channel rows, columns and the toggle/test/delete handlers are the shared
// notification-channel inventory (JAB-336, ADR-0083); this tab keeps its own
// server-paginated table shell and supplies the admin policy + an overflow
// RowActions menu.
import { useTranslation } from "react-i18next";
import { Button, Space } from "antd";
import { useState } from "react";

import { DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from "@icons";

import { RowActions } from "../../../components/RowActions";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import { useTableURL } from "../../../hooks/useTableURL";
import { AdminChannelDrawer } from "./AdminChannelDrawer";
import {
  ADMIN_CHANNEL_POLICY,
  type NotificationChannel,
} from "../../../components/notifications/channelPolicy";
import { buildChannelColumns } from "../../../components/notifications/channelColumns";
import { useChannelActions } from "../../../components/notifications/useChannelActions";

export const ChannelsTab = () => {
  const { t } = useTranslation();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel | undefined>();

  const query = useTableURL<NotificationChannel>({
    resource: ADMIN_CHANNEL_POLICY.resourcePath,
    defaultSort: "created_at",
    defaultOrder: "desc",
  });
  const { toggleEnabled, deleteChannel, testChannel } = useChannelActions(ADMIN_CHANNEL_POLICY);

  const openCreate = () => {
    setEditing(undefined);
    setDrawerOpen(true);
  };

  const openEdit = (row: NotificationChannel) => {
    setEditing(row);
    setDrawerOpen(true);
  };

  const columns = buildChannelColumns({
    labels: {
      name: t("channelstab.name"),
      kind: t("channelstab.kind"),
      owner: "Owner",
      enabled: t("channelstab.enabled"),
      actions: t("channelstab.actions"),
    },
    showOwnerColumn: ADMIN_CHANNEL_POLICY.showOwnerColumn,
    onOpenEdit: openEdit,
    onToggleEnabled: toggleEnabled,
    renderActions: (row) => (
      <RowActions
        actions={[
          { key: "test", label: "Test", icon: <SendOutlined />, onClick: () => testChannel(row) },
          { key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => openEdit(row) },
          {
            key: "delete",
            label: "Delete",
            icon: <DeleteOutlined />,
            danger: true,
            onClick: () => deleteChannel(row),
            confirm: { title: `Delete ${row.name}?` },
          },
        ]}
      />
    ),
  });

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
        columns={columns}
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
      />

      <AdminChannelDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} existing={editing} />
    </div>
  );
};
