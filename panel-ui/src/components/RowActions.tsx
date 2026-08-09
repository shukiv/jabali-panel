// RowActions — the canonical per-row actions cell. Renders the FIRST visible
// action as a full RowActionButton (icon + text) and collapses every remaining
// action into an overflow ("hamburger") menu whose items keep their full text +
// icon. This keeps dense tables narrow and uniform across the whole panel — the
// default shape for any row that has more than one action.
//
// Destructive actions pass `confirm` (a feedback.modal.confirm pops before onClick), so
// the menu can hold a Delete without nesting a Popconfirm inside the dropdown.
//
// Usage:
//   <RowActions actions={[
//     { key: "login",  label: "Log in to admin", icon: <LoginOutlined />, onClick: handleLogin, tooltip: "Open admin", loading },
//     { key: "purge",  label: "Purge cache",      icon: <ThunderboltOutlined />, onClick: handlePurge, hidden: !cacheEnabled },
//     { key: "clone",  label: "Clone",            icon: <CopyOutlined />, onClick: onClone, disabled: !canClone },
//     { key: "delete", label: "Delete",           icon: <DeleteOutlined />, onClick: onDelete, danger: true,
//       confirm: { title: "Delete this item?", description: "This cannot be undone.", okText: "Delete" } },
//   ]} />

import type { ReactNode } from "react";
import { Dropdown, Space, Tooltip } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts
import { MoreOutlined } from "@icons";
import { RowActionButton } from "./RowActionButton";

export interface RowAction {
  /** Stable key (menu item id). */
  key: string;
  /** Full text — shown on the first button AND as the menu item label. */
  label: string;
  /** Required icon (matches RowActionButton's contract). */
  icon: ReactNode;
  onClick?: () => void;
  danger?: boolean;
  disabled?: boolean;
  loading?: boolean;
  /** When true the action is omitted entirely (e.g. capability gate). */
  hidden?: boolean;
  /** Tooltip for the first button (e.g. why it's disabled). */
  tooltip?: string;
  /** When set, a feedback.modal.confirm pops before onClick runs. */
  confirm?: { title?: string; description?: ReactNode; okText?: string };
}

function run(a: RowAction) {
  if (!a.confirm) {
    a.onClick?.();
    return;
  }
  feedback.modal.confirm({
    title: a.confirm.title ?? "Are you sure?",
    content: a.confirm.description,
    okText: a.confirm.okText ?? "OK",
    okButtonProps: { danger: a.danger },
    onOk: a.onClick,
  });
}

export function RowActions({ actions }: { actions: RowAction[] }) {
  const visible = actions.filter((a) => !a.hidden);
  if (visible.length === 0) {
    return null;
  }
  const [first, ...rest] = visible;

  const firstButton = (
    <RowActionButton
      icon={first.icon}
      danger={first.danger}
      disabled={first.disabled}
      loading={first.loading}
      onClick={() => run(first)}
    >
      {first.label}
    </RowActionButton>
  );

  return (
    <Space size={4}>
      {first.tooltip ? <Tooltip title={first.tooltip}>{firstButton}</Tooltip> : firstButton}
      {rest.length > 0 && (
        <Dropdown
          trigger={["click"]}
          menu={{
            items: rest.map((a) => ({
              key: a.key,
              label: a.label,
              icon: a.icon,
              danger: a.danger,
              disabled: a.disabled,
              onClick: () => run(a),
            })),
          }}
        >
          <RowActionButton icon={<MoreOutlined />} color="default" aria-label="More actions" />
        </Dropdown>
      )}
    </Space>
  );
}
