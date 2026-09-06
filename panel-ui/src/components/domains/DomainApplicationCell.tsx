// DomainApplicationCell — the tenant Web Domains "Application" column (GH
// #1543, D1). Renders this domain's primary (docroot) One-Click install as a
// badge + version (or live status while it settles), with a one-click Login for
// ready apps that support SSO. A domain can host several installs (/, /blog);
// the extras collapse to a "+N" link out to the Applications page. Install and
// Delete are intentionally NOT here — they land in D2, which owns the mutations
// and the transitional poll.
//
// It needs a per-row hook (useMagicLink), so it is a component, not a render fn.
// appDisplayName lives under shells/user/applications; importing it here mirrors
// DomainInventory already importing shells/DomainRedirectsButton — the domain
// inventory module and the applications module are peers, not layered.
import { Button, Space, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { Link } from "react-router";
import type { DomainApplicationSummary } from "./types";
import { ApplicationStatusTag } from "../applications/ApplicationStatusTag";
import {
  canApplicationLogin,
  openApplicationLogin,
} from "../applications/applicationInventory";
import { useMagicLink } from "../../hooks/useMagicLink";
import { appDisplayName } from "../../shells/user/applications/CmsIcon";

interface DomainApplicationCellProps {
  applications?: DomainApplicationSummary[];
  // GH #1543 D2: inline mutations. Supplied only on the tenant grid (undefined
  // ⇒ read-only: no Install button, no Delete). onInstall opens the install
  // modal pinned to this domain; onDelete removes the primary install (extras
  // are managed on the Applications page). deletingId spins the matching row.
  onInstall?: () => void;
  onDelete?: (app: DomainApplicationSummary) => void;
  deletingId?: string | null;
}

// The Login control is its own component so useMagicLink (one mint state per
// install) is scoped to the row that renders it, not the whole table.
const AppLoginButton = ({ app }: { app: DomainApplicationSummary }) => {
  const { mint, loading, error } = useMagicLink(app.id);
  return (
    <Button
      type="link"
      size="small"
      loading={loading}
      style={{ paddingInline: 0 }}
      onClick={() => openApplicationLogin(mint, error)}
    >
      Login
    </Button>
  );
};

export const DomainApplicationCell = ({
  applications,
  onInstall,
  onDelete,
  deletingId,
}: DomainApplicationCellProps) => {
  const apps = applications ?? [];
  if (apps.length === 0) {
    // Empty: offer Install when the grid wired it (tenant), else a plain dash.
    return onInstall ? (
      <Button size="small" icon={<PlusOutlined />} onClick={onInstall}>
        Install
      </Button>
    ) : (
      <span style={{ color: "#bbb" }}>—</span>
    );
  }

  const primary = apps[0];
  const extra = apps.length - 1;
  // A settling/deleting primary is already spinning via its status tag; also
  // spin the Delete button while its own request is in flight.
  const deleting = deletingId === primary.id;

  return (
    <Space size={6} wrap>
      <Tag style={{ marginInlineEnd: 0 }}>{appDisplayName(primary.app_type)}</Tag>
      {primary.status === "ready" ? (
        <Typography.Text>{primary.version || "-"}</Typography.Text>
      ) : (
        // Not ready: show the live status (installing / failed …) instead of a
        // version, so a settling or broken install is visible in the grid.
        <ApplicationStatusTag status={primary.status} lastError={primary.last_error} />
      )}
      {canApplicationLogin(primary) && <AppLoginButton app={primary} />}
      {onDelete && (
        <Tooltip title="Delete this app">
          <Button
            type="text"
            size="small"
            danger
            loading={deleting}
            icon={<DeleteOutlined />}
            aria-label="Delete app"
            onClick={() => onDelete(primary)}
          />
        </Tooltip>
      )}
      {extra > 0 && (
        <Link to="/jabali-panel/applications" style={{ fontSize: 12 }}>
          +{extra} more
        </Link>
      )}
    </Space>
  );
};
