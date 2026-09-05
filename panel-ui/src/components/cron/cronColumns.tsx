// cronColumns — the shared Enabled and Actions columns for the Cron Job
// Workspace (JAB-298). Kept in their own module so both the admin and tenant
// Adapters splice them into their own column ORDER while the render + busy
// behavior stay in one place. The Adapter owns order and descriptive columns;
// these two columns own the shared behavior.
import type { ReactNode } from "react";
import { Switch, type TableColumnsType } from "antd";
import { CheckOutlined, CloseOutlined, DeleteOutlined, EditOutlined, EyeOutlined, PlayCircleOutlined } from "@icons";

import { RowActions } from "../RowActions";
import type { CronJob } from "../../apiClient";

// The row shape shared by both screens: the tenant list returns CronJob; the
// admin list returns CronJob plus a denormalised owner username. username is
// optional here so one type covers both.
export type CronWorkspaceRow = CronJob & { username?: string };

type CronColumn = TableColumnsType<CronWorkspaceRow>[number];

// Handlers + busy state the Module hands to the shared column builders. The
// Adapter never implements these; it only decides column order and presentation.
export interface CronColumnContext {
  busy: { deletingId: string | null; runningId: string | null; togglingId: string | null };
  onToggle: (row: CronWorkspaceRow) => void;
  onRun: (row: CronWorkspaceRow) => void;
  onLog: (row: CronWorkspaceRow) => void;
  onEdit: (row: CronWorkspaceRow) => void;
  onDelete: (row: CronWorkspaceRow) => void;
}

// Per-audience presentation for the Actions column: the run label ("Run" vs
// "Run now"), whether an Edit action is offered (tenant only), and the delete
// confirmation copy (each screen keeps its own wording).
export interface CronActionsOptions {
  runLabel: string;
  canEdit: boolean;
  deleteConfirm: (row: CronWorkspaceRow) => { title: string; description?: ReactNode; okText: string };
}

// The shared Enabled toggle column. One implementation of the busy behavior:
// the row in flight shows a spinner; every other row's toggle is disabled until
// it settles.
export function cronEnabledColumn(ctx: CronColumnContext): CronColumn {
  return {
    title: "Enabled",
    dataIndex: "enabled",
    width: 90,
    sorter: (a, b) => Number(a.enabled) - Number(b.enabled),
    render: (_: boolean, row) => (
      <Switch
        size="small"
        checked={row.enabled}
        loading={ctx.busy.togglingId === row.id}
        disabled={ctx.busy.togglingId !== null && ctx.busy.togglingId !== row.id}
        onChange={() => ctx.onToggle(row)}
        checkedChildren={<CheckOutlined />}
        unCheckedChildren={<CloseOutlined />}
      />
    ),
  };
}

// The shared Actions column: Run, Log, (Edit for tenant), Delete. Run and
// Delete share the same busy gating as the toggle; Delete carries the
// audience's own confirmation copy.
export function cronActionsColumn(ctx: CronColumnContext, opts: CronActionsOptions): CronColumn {
  return {
    title: "Actions",
    dataIndex: "actions",
    width: 240,
    render: (_, row) => (
      <RowActions
        actions={[
          {
            key: "run",
            label: opts.runLabel,
            icon: <PlayCircleOutlined />,
            loading: ctx.busy.runningId === row.id,
            disabled: ctx.busy.runningId !== null && ctx.busy.runningId !== row.id,
            onClick: () => ctx.onRun(row),
          },
          { key: "log", label: "Log", icon: <EyeOutlined />, onClick: () => ctx.onLog(row) },
          ...(opts.canEdit
            ? [{ key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => ctx.onEdit(row) }]
            : []),
          {
            key: "delete",
            label: "Delete",
            icon: <DeleteOutlined />,
            danger: true,
            loading: ctx.busy.deletingId === row.id,
            onClick: () => ctx.onDelete(row),
            confirm: opts.deleteConfirm(row),
          },
        ]}
      />
    ),
  };
}
