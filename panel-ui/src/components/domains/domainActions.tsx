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
  | "chown"
  | "rename";

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
  const { audience, togglingId, navigate, onOpenModal } = ctx;
  // GH #1920 (johnnyq): no DNS entry here — a domain's records are managed
  // only under DNS > Zones.

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
      // GH #1579: in-place rename, the same modal the tenant Web Domain page
      // uses. Admins may rename any domain (the API is owner-scoped: admin any).
      // Hidden for the panel's own primary domain, which the backend refuses.
      ...(r.is_panel_primary
        ? []
        : [
            {
              key: "rename",
              icon: <EditOutlined />,
              label: "Rename domain",
              onClick: () => onOpenModal(r.id, "rename"),
            },
          ]),
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
