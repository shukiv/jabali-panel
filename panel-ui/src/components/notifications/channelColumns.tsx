// channelColumns — the shared column set for a notification-channel inventory
// (JAB-336, ADR-0083). Both the admin tab and the tenant page render their own
// table shell (server-paginated SearchableTable vs a plain client Table inside a
// Card with event routing) but the name / kind / owner / enabled columns are
// identical, so they live here. The owner column is admin-only, gated by a single
// policy flag — that's AC4 in one place.
//
// The actions cell differs structurally between the two surfaces (an overflow
// RowActions menu vs inline buttons + Popconfirm), so each host supplies it via
// renderActions rather than the builder guessing.
import type { ReactNode } from "react";
import { Switch, Tag } from "antd";
import type { TableProps } from "antd";
import { kindColors, kindLabels } from "../../utils/channelKindConfig";
import type { NotificationChannel } from "./channelPolicy";

type ChannelColumns = NonNullable<TableProps<NotificationChannel>["columns"]>;

export type ChannelColumnLabels = {
  name: string;
  kind: string;
  owner: string;
  enabled: string;
  actions: string;
};

export function buildChannelColumns(opts: {
  labels: ChannelColumnLabels;
  showOwnerColumn: boolean;
  onOpenEdit: (row: NotificationChannel) => void;
  onToggleEnabled: (row: NotificationChannel, next: boolean) => void;
  renderActions: (row: NotificationChannel) => ReactNode;
}): ChannelColumns {
  const { labels, showOwnerColumn, onOpenEdit, onToggleEnabled, renderActions } = opts;

  const columns: ChannelColumns = [
    {
      dataIndex: "name",
      title: labels.name,
      render: (name: string, row: NotificationChannel) => (
        <a onClick={() => onOpenEdit(row)}>{name}</a>
      ),
    },
    {
      dataIndex: "kind",
      title: labels.kind,
      render: (k: string) => (
        <Tag color={kindColors[k as keyof typeof kindColors]}>
          {kindLabels[k as keyof typeof kindLabels] ?? k}
        </Tag>
      ),
    },
  ];

  // Owner column is admin-only: it distinguishes server-wide channels from
  // tenant-owned ones. A tenant only ever sees its own rows, so the column would
  // be noise. (AC4)
  if (showOwnerColumn) {
    columns.push({
      dataIndex: "user_id",
      title: labels.owner,
      render: (userID: string | null | undefined) =>
        userID ? (
          <Tag color="blue" title={userID}>
            Tenant
          </Tag>
        ) : (
          <Tag>Server-wide</Tag>
        ),
    });
  }

  columns.push(
    {
      dataIndex: "enabled",
      title: labels.enabled,
      render: (enabled: boolean, row: NotificationChannel) => (
        <Switch checked={enabled} onChange={(next) => onToggleEnabled(row, next)} />
      ),
    },
    {
      key: "actions",
      title: labels.actions,
      render: (_: unknown, row: NotificationChannel) => renderActions(row),
    },
  );

  return columns;
}
