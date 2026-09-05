// JAB-300: the row-action menu for the domain inventory grid. Admin and
// tenant had two hand-kept item lists that drifted (tenant Delete lacked the
// System-domain guard the admin list has; the toggle/preview/bot mutations
// invalidated different query keys). This is the single builder; the audience
// discriminant selects the item set. Every mutation and modal-open is a
// callback DomainInventory supplies, so the builder stays a pure list producer
// that unit tests can exercise per audience.
import {
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  FileTextOutlined,
  FolderOutlined,
  GlobalOutlined,
  InfoCircleOutlined,
  LockOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  SafetyOutlined,
  SettingOutlined,
  SwapOutlined,
  ThunderboltOutlined,
  ToolOutlined,
} from "@icons";
import type { MenuProps } from "antd";
import type { DomainInventoryAudience } from "./domainColumns";
import type { Domain } from "./types";

export type DomainModalType =
  | "redirects"
  | "index"
  | "settings"
  | "info"
  | "caching"
  | "directory-privacy"
  | "nginx-options"
  | "rewrite-rules"
  | "document-root";

export type DomainMenuCaps =
  | {
      dns_enabled?: boolean;
      tenant_domain_options_enabled?: boolean;
      tenant_docroot_editable?: boolean;
    }
  | undefined;

export type DomainMenuCtx = {
  audience: DomainInventoryAudience;
  caps: DomainMenuCaps;
  togglingId: string | null;
  navigate: (path: string) => void;
  onOpenModal: (domainId: string, type: DomainModalType) => void;
  onToggle: (r: Domain) => void;
  onTogglePreview: (r: Domain) => void;
  onToggleBot: (r: Domain) => void;
  onDelete: (r: Domain) => void;
};

export function buildDomainMenuItems(r: Domain, ctx: DomainMenuCtx): MenuProps["items"] {
  const { audience, caps, togglingId, navigate, onOpenModal } = ctx;
  // GH #1419: hide DNS when the DNS module is off (the page only 403s).
  // Default-on while caps load, matching the sidebar gate.
  const dnsEnabled = caps?.dns_enabled !== false;
  // The DNS route prefix is the one navigation target that differs by audience.
  const dnsPrefix = audience.kind === "admin" ? "/jabali-admin" : "/jabali-panel";

  const dnsItem = dnsEnabled
    ? [
        {
          key: "dns",
          icon: <GlobalOutlined />,
          label: "DNS",
          onClick: () => navigate(`${dnsPrefix}/domains/${r.id}/dns`),
        },
      ]
    : [];

  const toggleItem = {
    key: "toggle",
    icon: r.is_enabled ? <PauseCircleOutlined /> : <PlayCircleOutlined />,
    label: r.is_enabled ? "Disable" : "Enable",
    danger: r.is_enabled,
    disabled: togglingId === r.id,
    onClick: () => ctx.onToggle(r),
  };

  // GH #1382 + JAB-300: the System-domain (is_panel_primary) delete guard is
  // applied universally — tenant rows never carry the field, so this is a
  // no-op there and closes the admin-only gap in one place.
  const deleteItems = r.is_panel_primary
    ? []
    : [
        { type: "divider" as const },
        {
          key: "delete",
          icon: <DeleteOutlined />,
          label: "Delete",
          danger: true,
          onClick: () => ctx.onDelete(r),
        },
      ];

  if (audience.kind === "admin") {
    return [
      {
        key: "edit",
        icon: <EditOutlined />,
        label: "Edit",
        onClick: () => navigate(`/jabali-admin/domains/edit/${r.id}`),
      },
      ...dnsItem,
      {
        key: "info",
        icon: <InfoCircleOutlined />,
        label: "Information",
        onClick: () => onOpenModal(r.id, "info"),
      },
      {
        key: "redirects",
        icon: <SwapOutlined />,
        label: "Redirects",
        onClick: () => onOpenModal(r.id, "redirects"),
      },
      {
        key: "index",
        icon: <FileTextOutlined />,
        label: "Index Files",
        onClick: () => onOpenModal(r.id, "index"),
      },
      {
        key: "settings",
        icon: <SettingOutlined />,
        label: "Nginx Settings",
        onClick: () => onOpenModal(r.id, "settings"),
      },
      {
        key: "caching",
        icon: <ThunderboltOutlined />,
        label: "Caching",
        onClick: () => onOpenModal(r.id, "caching"),
      },
      toggleItem,
      ...deleteItems,
    ];
  }

  // Tenant.
  const optionItems = caps?.tenant_domain_options_enabled
    ? [
        {
          key: "nginx-options",
          icon: <ThunderboltOutlined />,
          label: "Domain options",
          onClick: () => onOpenModal(r.id, "nginx-options"),
        },
        {
          key: "rewrite-rules",
          icon: <ToolOutlined />,
          label: "Rewrite rules",
          onClick: () => onOpenModal(r.id, "rewrite-rules"),
        },
      ]
    : [];
  const docRootItem = caps?.tenant_docroot_editable
    ? [
        {
          key: "document-root",
          icon: <FolderOutlined />,
          label: "Document root",
          onClick: () => onOpenModal(r.id, "document-root"),
        },
      ]
    : [];

  return [
    ...dnsItem,
    {
      key: "redirects",
      icon: <SwapOutlined />,
      label: "Redirects",
      onClick: () => onOpenModal(r.id, "redirects"),
    },
    {
      key: "index",
      icon: <FileTextOutlined />,
      label: "Index",
      onClick: () => onOpenModal(r.id, "index"),
    },
    {
      key: "directory-privacy",
      icon: <LockOutlined />,
      label: "Directory Privacy",
      onClick: () => onOpenModal(r.id, "directory-privacy"),
    },
    {
      key: "caching",
      icon: <ThunderboltOutlined />,
      label: "Caching",
      onClick: () => onOpenModal(r.id, "caching"),
    },
    ...optionItems,
    ...docRootItem,
    {
      key: "preview-url",
      icon: <EyeOutlined />,
      label: r.temp_url_enabled ? "Disable preview URL" : "Enable preview URL",
      onClick: () => ctx.onTogglePreview(r),
    },
    {
      key: "bot-challenge",
      icon: <SafetyOutlined />,
      label: r.bot_challenge_include
        ? "Disable bot-detection challenge"
        : "Enable bot-detection challenge",
      onClick: () => ctx.onToggleBot(r),
    },
    toggleItem,
    ...deleteItems,
  ];
}
