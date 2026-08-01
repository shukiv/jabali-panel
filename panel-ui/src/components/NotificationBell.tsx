// NotificationBell — topbar unread-count bell + dropdown for M14
// Step 7. Uses TanStack Query polling (30s) so the bell updates even
// when Web Push isn't subscribed (belt + braces per the plan).
//
// Dropdown chrome mirrors the AntD `custom-dropdown` demo: pass
// `menu={{ items }}` so each notification row renders as a real
// Menu.Item (AntD-native hover + padding + focus ring), then wrap
// the menu in `dropdownRender` with a header Space of buttons and
// a footer Space for the push toggle. Container uses theme tokens
// (colorBgElevated / borderRadiusLG / boxShadowSecondary) so the
// popup is visually identical to every other AntD dropdown.
import { useTranslation } from "react-i18next";
import { Badge, Button, Divider, Dropdown, Empty, Grid, Popconfirm, Space, Tag, Tooltip, Typography, message, theme } from "antd";
import type { MenuProps } from "antd";
import type { CSSProperties, ReactElement } from "react";
import { cloneElement, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";

import { notificationTarget } from "../utils/notificationTarget";

import { useAuth } from "../auth/AuthContext";

import { BellOutlined, CheckOutlined, DeleteOutlined } from "@icons";

import { apiClient } from "../apiClient";
import { useWebPushSubscription } from "../hooks/useWebPushSubscription";

const { useToken } = theme;

type NotificationRow = {
  id: string;
  event_kind: string;
  severity: "info" | "warning" | "error" | "critical";
  title: string;
  body: string;
  deeplink?: string;
  created_at: string;
  read_at?: string | null;
};

type InboxResponse = {
  data: NotificationRow[];
  total: number;
  page: number;
  page_size: number;
  unread: number;
  unread_only: boolean;
};

const INBOX_KEY = ["notifications", "inbox"] as const;

const severityColor: Record<NotificationRow["severity"], string> = {
  info: "blue",
  warning: "gold",
  error: "red",
  critical: "magenta",
};

// relativeTime — small no-dep helper. Keeps the bundle thin compared to
// pulling dayjs/relativeTime plugin for a single consumer.
function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diff = Date.now() - then;
  const mins = Math.floor(diff / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} min ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  return `${days}d ago`;
}

export function NotificationBell() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { user } = useAuth();
  const [open, setOpen] = useState(false);
  const qc = useQueryClient();
  const webpush = useWebPushSubscription();
  const { token } = useToken();
  const screens = Grid.useBreakpoint();
  const isNarrow = !screens.sm;

  const inbox = useQuery<InboxResponse>({
    queryKey: INBOX_KEY,
    queryFn: async () => {
      const { data } = await apiClient.get<InboxResponse>(
        "/notifications/inbox?page_size=10",
      );
      return data;
    },
    // 30s poll + refetch-on-focus. Focus refetch covers the "came back
    // to the tab" case instantly, so the background cadence only serves
    // long-lived open tabs — 3x fewer requests than the old 10s poll
    // with no perceived staleness. Push (SSE) is tracked separately.
    refetchInterval: 30_000,
    staleTime: 5_000,
    refetchOnWindowFocus: true,
  });

  useEffect(() => {
    if (typeof navigator === "undefined" || !navigator.serviceWorker) return;
    const onMessage = (event: MessageEvent) => {
      if (event.data && event.data.type === "jabali/notification") {
        qc.invalidateQueries({ queryKey: ["notifications"] });
      }
    };
    navigator.serviceWorker.addEventListener("message", onMessage);
    return () => {
      navigator.serviceWorker.removeEventListener("message", onMessage);
    };
  }, [qc]);

  const unread = inbox.data?.unread ?? 0;
  const rows = inbox.data?.data ?? [];

  const markAllRead = async () => {
    try {
      await apiClient.post("/notifications/inbox/read-all");
      qc.invalidateQueries({ queryKey: ["notifications"] });
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Mark all failed");
    }
  };

  const clearAll = async () => {
    try {
      await apiClient.delete("/notifications/inbox");
      qc.invalidateQueries({ queryKey: ["notifications"] });
      message.success("All notifications cleared");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Clear failed");
    }
  };

  const handleItemClick = async (row: NotificationRow) => {
    try {
      if (!row.read_at) {
        await apiClient.post(`/notifications/inbox/${row.id}/read`);
        qc.invalidateQueries({ queryKey: ["notifications"] });
      }
    } catch {
      // Silent — clicking through shouldn't block navigation.
    }
    // GH #726: notifications without a dedicated deeplink (service.down and
    // other Error alerts) used to dead-end on click; notificationTarget falls
    // back to the History tab, focused on this row.
    navigate(notificationTarget(row.id, row.deeplink));
  };

  const pushToggle = (() => {
    if (!webpush.supported) {
      return <Typography.Text type="secondary">Browser push not supported</Typography.Text>;
    }
    if (webpush.permission === "denied") {
      return (
        <Typography.Text type="secondary">Push blocked — enable in your browser</Typography.Text>
      );
    }
    if (webpush.subscribed) {
      return (
        <Button type="text" size="small" onClick={() => void webpush.unsubscribe()} loading={webpush.loading}>
          Disable browser push
        </Button>
      );
    }
    const button = (
      <Button
        type="text"
        size="small"
        onClick={() => {
          if (webpush.error) {
            message.error(webpush.error);
            return;
          }
          void webpush.subscribe();
        }}
        loading={webpush.loading}
        danger={!!webpush.error}
      >
        Enable browser push
      </Button>
    );
    return webpush.error ? (
      <Tooltip title={webpush.error} placement="topLeft">
        {button}
      </Tooltip>
    ) : (
      button
    );
  })();

  // Menu items — one Menu.Item per notification. label is a compact
  // two-line block (title + body-preview + timestamp) so the row
  // keeps AntD's native hover tint without us re-rolling padding.
  // The severity Tag lives in `icon` (left gutter) so it aligns
  // with the Menu.Item.icon slot used by AntD's own menus.
  const items: MenuProps["items"] =
    rows.length === 0
      ? [
          {
            key: "empty",
            disabled: true,
            label: (
              <Empty
                description={inbox.isLoading ? "Loading…" : "No notifications"}
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                style={{ padding: token.paddingSM }}
              />
            ),
          },
        ]
      : rows.map((row) => ({
          key: row.id,
          onClick: () => void handleItemClick(row),
          style: {
            height: "auto",
            padding: `${token.paddingXS}px ${token.padding}px`,
            background: row.read_at ? undefined : token.colorPrimaryBg,
          },
          label: (
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
              <Space size={token.marginXS} align="center">
                <Tag color={severityColor[row.severity] ?? "default"} style={{ marginInlineEnd: 0 }}>
                  {row.severity}
                </Tag>
                <Typography.Text strong style={{ lineHeight: 1.3 }}>
                  {row.title}
                </Typography.Text>
              </Space>
              <Typography.Paragraph
                type="secondary"
                style={{ margin: 0, fontSize: token.fontSizeSM, whiteSpace: "pre-wrap" }}
                ellipsis={{ rows: 2 }}
              >
                {row.body}
              </Typography.Paragraph>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {relativeTime(row.created_at)}
              </Typography.Text>
            </div>
          ),
        }));

  // Width clamps so the popup never overflows the viewport on narrow
  // phones — full bleed minus the body padding on <sm, capped at 380
  // everywhere else. min() keeps the desktop ceiling so wide screens
  // don't stretch the dropdown.
  const contentStyle: CSSProperties = {
    width: isNarrow ? "calc(100vw - 16px)" : "min(380px, calc(100vw - 16px))",
    maxWidth: "100vw",
    backgroundColor: token.colorBgElevated,
    borderRadius: token.borderRadiusLG,
    boxShadow: token.boxShadowSecondary,
  };

  const rowStyle: CSSProperties = {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: `${token.paddingSM}px ${token.padding}px`,
  };

  return (
    <Dropdown
      menu={{ items }}
      trigger={["click"]}
      open={open}
      onOpenChange={setOpen}
      // Always anchor right edge of popup to right edge of bell. On narrow
      // viewports "bottom" (centered) overflows the right edge when the bell
      // is near the right of the header. "bottomRight" keeps the popup inside
      // the viewport as long as popup width ≤ bell's x offset from left, which
      // holds for width=calc(100vw-16px) and any standard phone layout.
      // adjustX/adjustY let AntD nudge the popup if it still clips.
      placement="bottomRight"
      // shiftX/shiftY slide a wide popup fully into the viewport (a
      // near-full-width popup right-anchored to the bell would otherwise
      // hang off the LEFT edge on phones — the bell sits ~100px from the
      // viewport's right edge). adjustX flip alone can't fix a wide popup;
      // shiftX is the slide-to-fit that keeps both edges on-screen.
      getPopupContainer={() => document.body}
      align={{ overflow: { adjustX: false, adjustY: true, shiftX: true, shiftY: true } }}
      dropdownRender={(menu) => (
        <div style={contentStyle}>
          <div style={rowStyle}>
            <Space size={token.marginXS}>
              <BellOutlined />
              <Typography.Text strong style={{ whiteSpace: "nowrap" }}>
                Notifications
              </Typography.Text>
              {unread > 0 && <Badge count={unread} size="small" />}
            </Space>
            <Space size={token.marginXS}>
              {isNarrow ? (
                <Tooltip title={t("notificationbell.mark_all_read")}>
                  <Button
                    type="text"
                    size="small"
                    icon={<CheckOutlined />}
                    onClick={markAllRead}
                    disabled={unread === 0}
                  />
                </Tooltip>
              ) : (
                <Button
                  type="text"
                  size="small"
                  onClick={markAllRead}
                  disabled={unread === 0}
                >
                  Mark all read
                </Button>
              )}
              <Popconfirm
                title={t("notificationbell.clear_all_notifications")}
                description={t("notificationbell.this_deletes_every_notification_in_your_inbo")}
                onConfirm={clearAll}
                okText={t("notificationbell.clear")}
                okButtonProps={{ danger: true }}
              >
                <Button
                  type="text"
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  disabled={rows.length === 0}
                >
                  {isNarrow ? null : "Clear"}
                </Button>
              </Popconfirm>
            </Space>
          </div>
          <Divider style={{ margin: 0 }} />
          <div style={{ maxHeight: 420, overflowY: "auto" }}>
            {cloneElement(
              menu as ReactElement<{ style: CSSProperties }>,
              { style: { boxShadow: "none", background: "transparent" } },
            )}
          </div>
          <Divider style={{ margin: 0 }} />
          <div style={rowStyle}>
            {user?.isAdmin ? (
              <Button
                type="link"
                size="small"
                style={{ paddingLeft: 0 }}
                onClick={() => {
                  setOpen(false);
                  navigate("/jabali-admin/notifications/history");
                }}
              >
                View history
              </Button>
            ) : (
              <span />
            )}
            {pushToggle}
          </div>
        </div>
      )}
    >
      <Button type="text" aria-label={t("notificationbell.notifications")} style={{ width: 40, height: 40, padding: 0, display: "inline-flex", alignItems: "center", justifyContent: "center" }}>
        <Badge count={unread} size="small" overflowCount={99}>
          <BellOutlined />
        </Badge>
      </Button>
    </Dropdown>
  );
}
