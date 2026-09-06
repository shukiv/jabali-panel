// buildApplicationActions — the per-row action set for the Application
// Inventory lists. The shared login + delete actions live here once; the
// tenant list adds its privileged cache/clone actions by passing a tenant
// context. Admin literally cannot supply purge/warmup/clone (its context has
// no `onClone`), so those actions are absent, not merely hidden — the JAB-335
// absence-over-flag shape. AC4: role-specific actions stay explicit and are
// unit-tested against these key sets.
import { createElement, type ReactNode } from "react";
import {
  LoginOutlined,
  DeleteOutlined,
  ThunderboltOutlined,
  CopyOutlined,
} from "@icons";
import type { RowAction } from "../RowActions";

// The login + delete actions every list has.
export interface SharedActionCtx {
  canLogin: boolean;
  onLogin: () => void;
  loginLoading: boolean;
  onDelete: () => void;
  deleting: boolean;
  // The confirm copy differs per audience (admin: "…and its data"; tenant:
  // "…database, files, and any associated cron jobs"), so it is a context
  // field, not a constant baked into the builder.
  deleteDescription: ReactNode;
}

// The tenant list additionally exposes cache + clone actions.
export interface TenantActionCtx extends SharedActionCtx {
  cacheEnabled: boolean;
  onPurge: () => void;
  purging: boolean;
  onWarmup: () => void;
  warming: boolean;
  canClone: boolean;
  onClone: () => void;
}

const isTenantCtx = (
  ctx: SharedActionCtx | TenantActionCtx,
): ctx is TenantActionCtx => "onClone" in ctx;

export function buildApplicationActions(
  ctx: SharedActionCtx | TenantActionCtx,
): RowAction[] {
  const login: RowAction = {
    key: "login",
    label: "Log in to admin",
    icon: createElement(LoginOutlined),
    onClick: ctx.onLogin,
    loading: ctx.loginLoading,
    tooltip: "Log in to the admin dashboard",
    hidden: !ctx.canLogin,
  };

  const del: RowAction = {
    key: "delete",
    label: "Delete",
    icon: createElement(DeleteOutlined),
    danger: true,
    loading: ctx.deleting,
    onClick: ctx.onDelete,
    confirm: {
      title: "Delete this application?",
      description: ctx.deleteDescription,
      okText: "Delete",
    },
  };

  if (!isTenantCtx(ctx)) {
    // Admin: read-only cross-user list — login + delete only.
    return [login, del];
  }

  return [
    login,
    {
      key: "purge",
      label: "Purge cache",
      icon: createElement(ThunderboltOutlined),
      onClick: ctx.onPurge,
      loading: ctx.purging,
      hidden: !ctx.cacheEnabled,
    },
    {
      key: "warmup",
      label: "Warm cache",
      icon: createElement(ThunderboltOutlined),
      onClick: ctx.onWarmup,
      loading: ctx.warming,
      hidden: !ctx.cacheEnabled,
    },
    {
      key: "clone",
      label: "Clone",
      icon: createElement(CopyOutlined),
      onClick: ctx.onClone,
      disabled: !ctx.canClone,
      tooltip: ctx.canClone
        ? undefined
        : "Clone is only available for healthy WordPress installs",
    },
    del,
  ];
}
