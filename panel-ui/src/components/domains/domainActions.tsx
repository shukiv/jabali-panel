// JAB-300: the row-action menu for the domain inventory grid. Admin and
// tenant had two hand-kept item lists that drifted (tenant Delete lacked the
// System-domain guard the admin list has; the toggle/preview/bot mutations
// invalidated different query keys). This is the single builder; the audience
// discriminant selects the item set. Every mutation and modal-open is a
// callback DomainInventory supplies, so the builder stays a pure list producer
// that unit tests can exercise per audience.
//
// GH #1543: the tenant per-domain actions (DNS and every editor) moved onto the
// dedicated Web Domain page (row-click → tabs). The tenant menu is now just
// Enable/Disable + Delete. The admin list is unchanged; it keeps its full
// modal-driven menu, including DNS (admin has no per-domain page).
import {
  DeleteOutlined,
  EditOutlined,
  FileTextOutlined,
  GlobalOutlined,
  InfoCircleOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  SettingOutlined,
  SwapOutlined,
  TeamOutlined,
  ThunderboltOutlined,
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
  | "chown";

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
      // GH #1238: reassign the domain to a new tenant. Admin-only; the modal's
      // POST is behind the JAB-380 step-up.
      {
        key: "chown",
        icon: <TeamOutlined />,
        label: "Change owner",
        onClick: () => onOpenModal(r.id, "chown"),
      },
      toggleItem,
      ...deleteItems,
    ];
  }

  // Tenant. Every per-domain surface — the editors (Redirects, Index, Caching,
  // Directory Privacy, Domain options, Rewrite rules, Document root), the
  // preview-URL / bot-challenge toggles, and now DNS — lives on the dedicated
  // Web Domain page, reached by clicking the domain name. The row menu keeps
  // only the Enable/Disable + Delete lifecycle. GH #1543.
  return [toggleItem, ...deleteItems];
}
