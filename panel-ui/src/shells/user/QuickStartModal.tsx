// QuickStartModal — first-login welcome + quick-start guide for the
// tenant (user) shell. Per-user localStorage dismiss; "I'll read later"
// only closes for the session, "Never show again" persists.
import { useEffect, useState, type ComponentType, type CSSProperties } from "react";
import { Button, Modal, theme, Typography } from "antd";
import {
  ApiOutlined,
  AppstoreAddOutlined,
  CheckOutlined,
  ClockCircleOutlined,
  DatabaseOutlined,
  GlobalOutlined,
  MailOutlined,
  QuestionCircleOutlined,
  SaveOutlined,
} from "@icons";
import { Link } from "react-router";

import { useAuth } from "../../auth/AuthContext";
import { apiClient } from "../../apiClient";

const PREF_KEY = "quickstart_user";

interface Step {
  number: number;
  title: string;
  desc: string;
  href: string;
  icon: ComponentType<{ style?: CSSProperties }>;
  color: string; // accent for icon tile + number badge
}

const STEPS: Step[] = [
  {
    number: 1,
    title: "Domains",
    desc: "Add your first domain — Jabali provisions the vhost, DNS zone, and SSL automatically.",
    href: "/jabali-panel/domains",
    icon: GlobalOutlined,
    color: "#f59e0b", // amber
  },
  {
    number: 2,
    title: "Mail",
    desc: "Create mailboxes + forwarders on your mail-enabled domains.",
    href: "/jabali-panel/mail/mailboxes",
    icon: MailOutlined,
    color: "#10b981", // green
  },
  {
    number: 3,
    title: "Applications",
    desc: "1-click install WordPress, Joomla, Drupal, and more onto any domain.",
    href: "/jabali-panel/applications",
    icon: AppstoreAddOutlined,
    color: "#3b82f6", // bright blue
  },
  {
    number: 4,
    title: "Databases",
    desc: "Create MariaDB / Postgres databases + per-tenant users.",
    href: "/jabali-panel/databases",
    icon: DatabaseOutlined,
    color: "#2563eb", // blue
  },
  {
    number: 5,
    title: "Backups",
    desc: "Schedule snapshots or trigger an on-demand backup; restore in one click.",
    href: "/jabali-panel/backups",
    icon: SaveOutlined,
    color: "#a855f7", // purple
  },
  {
    number: 6,
    title: "Personal API Tokens",
    desc: "Mint a token for scripting / DDNS — same key works in routers, ddclient, CI.",
    href: "/jabali-panel/api-tokens",
    icon: ApiOutlined,
    color: "#22c55e", // emerald
  },
];

const hexAlpha = (hex: string, alpha: number) => {
  const n = parseInt(hex.slice(1), 16);
  const r = (n >> 16) & 0xff;
  const g = (n >> 8) & 0xff;
  const b = n & 0xff;
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
};

export function QuickStartModal() {
  const { token } = theme.useToken();
  const borderBase = token.colorBorderSecondary;
  const borderHover = token.colorBorder;
  const bgSubtle = token.colorFillTertiary;
  const bgSubtleHover = token.colorFillSecondary;
  const { user } = useAuth();
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!user?.id) return;
    let cancelled = false;
    // Server-side dismissal (GH #218) so it survives browsers that clear
    // storage on close. On error, leave the modal closed — don't pester.
    apiClient
      .get<{ prefs: Record<string, string> }>("/me/ui-prefs")
      .then(({ data }) => {
        if (!cancelled && data.prefs?.[PREF_KEY] !== "1") setOpen(true);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [user?.id]);

  const close = () => setOpen(false);

  const dismissForever = () => {
    setOpen(false);
    // Fire-and-forget persist; failure just means it may reappear later.
    apiClient.put(`/me/ui-prefs/${PREF_KEY}`, { value: "1" }).catch(() => {});
  };

  return (
    <Modal
      title={
        <div style={{ display: "flex", alignItems: "center", gap: 12, flexWrap: "nowrap", minWidth: 0 }}>
          <div
            style={{
              width: 40,
              height: 40,
              borderRadius: 10,
              background: hexAlpha("#3b82f6", 0.18),
              color: "#3b82f6",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: 20,
              flexShrink: 0,
            }}
          >
            <span role="img" aria-label="jabali">
              🐃
            </span>
          </div>
          <Typography.Title level={4} style={{ margin: 0, minWidth: 0, fontSize: "clamp(16px, 4.5vw, 22px)" }}>
            Welcome to Jabali Panel!{" "}
            <span role="img" aria-label="wave">
              👋
            </span>
          </Typography.Title>
        </div>
      }
      open={open}
      onCancel={close}
      width={960}
      style={{ maxWidth: "calc(100vw - 32px)" }}
      styles={{ wrapper: { padding: "16px" } }}
      destroyOnClose
      centered
      footer={[
        <Button key="later" icon={<ClockCircleOutlined />} onClick={close}>
          I&apos;ll read later
        </Button>,
        <Button
          key="never"
          type="primary"
          icon={<CheckOutlined />}
          onClick={dismissForever}
        >
          Never show again
        </Button>,
      ]}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 20 }}>
        Glad to have you here. A short tour of what to do first — click any
        item to jump straight to it:
      </Typography.Paragraph>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))",
          gap: 12,
        }}
      >
        {STEPS.map((step) => {
          return (
            <Link
              key={step.number}
              to={step.href}
              onClick={close}
              style={{
                display: "block",
                padding: "14px 16px",
                overflow: "hidden",
                borderRadius: 12,
                border: `1px solid ${borderBase}`,
                background: bgSubtle,
                color: "inherit",
                textDecoration: "none",
                transition: "background 0.15s, border-color 0.15s",
              }}
              onMouseEnter={(e) => {
                (e.currentTarget as HTMLAnchorElement).style.background =
                  bgSubtleHover;
                (e.currentTarget as HTMLAnchorElement).style.borderColor =
                  borderHover;
              }}
              onMouseLeave={(e) => {
                (e.currentTarget as HTMLAnchorElement).style.background =
                  bgSubtle;
                (e.currentTarget as HTMLAnchorElement).style.borderColor =
                  borderBase;
              }}
            >
              <div
                style={{
                  width: 36,
                  height: 36,
                  borderRadius: "50%",
                  background: step.color,
                  color: "white",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: 15,
                  fontWeight: 700,
                  float: "left",
                  marginRight: 14,
                  marginBottom: 4,
                }}
              >
                {step.number}
              </div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <Typography.Text strong style={{ fontSize: 16, display: "block" }}>
                  {step.title}
                </Typography.Text>
                <Typography.Text type="secondary" style={{ fontSize: 13 }}>
                  {step.desc}
                </Typography.Text>
              </div>
              
            </Link>
          );
        })}
      </div>

      <div
        style={{
          marginTop: 20,
          padding: "16px 18px",
          borderRadius: 12,
          background: hexAlpha("#3b82f6", 0.08),
          border: "1px solid " + hexAlpha("#3b82f6", 0.25),
          display: "flex",
          alignItems: "center",
          gap: 14,
        }}
      >
        <QuestionCircleOutlined
          style={{ fontSize: 22, color: "#3b82f6", flexShrink: 0 }}
        />
        <Typography.Text style={{ flex: 1, minWidth: 0 }}>
          Stuck? Read the docs at{" "}
          <a
            href="https://jabali-panel.com"
            target="_blank"
            rel="noopener noreferrer"
          >
            jabali-panel.com
          </a>
          .
        </Typography.Text>
      </div>
    </Modal>
  );
}
