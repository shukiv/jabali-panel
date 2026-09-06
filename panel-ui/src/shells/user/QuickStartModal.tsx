// QuickStartModal — tenant-shell adapter for the shared QuickStartGuide.
// Supplies the tenant audience: preference key, the six tenant destinations,
// and the tenant support callout (docs-only — no Support link, no emoji). All
// loading, dismissal, and layout live in the Module.
import { Typography } from "antd";
import { QuestionCircleOutlined } from "@icons";

import {
  QuickStartGuide,
  type QuickStartAudience,
  type QuickStartStep,
} from "../../components/quickstart/QuickStartGuide";

const PREF_KEY = "quickstart_user";

const STEPS: QuickStartStep[] = [
  {
    number: 1,
    title: "Domains",
    desc: "Add your first domain — Jabali provisions the vhost, DNS zone, and SSL automatically.",
    href: "/jabali-panel/domains",
    color: "#f59e0b", // amber
  },
  {
    number: 2,
    title: "Mail",
    desc: "Create mailboxes + forwarders on your mail-enabled domains.",
    href: "/jabali-panel/mail/mailboxes",
    color: "#10b981", // green
  },
  {
    number: 3,
    title: "Applications",
    desc: "1-click install WordPress, Joomla, Drupal, and more onto any domain.",
    href: "/jabali-panel/applications",
    color: "#3b82f6", // bright blue
  },
  {
    number: 4,
    title: "Databases",
    desc: "Create MariaDB / Postgres databases + per-tenant users.",
    href: "/jabali-panel/databases",
    color: "#2563eb", // blue
  },
  {
    number: 5,
    title: "Backups",
    desc: "Schedule snapshots or trigger an on-demand backup; restore in one click.",
    href: "/jabali-panel/backups",
    color: "#a855f7", // purple
  },
  {
    number: 6,
    title: "Personal API Tokens",
    desc: "Mint a token for scripting / DDNS — same key works in routers, ddclient, CI.",
    href: "/jabali-panel/api-tokens",
    color: "#22c55e", // emerald
  },
];

const AUDIENCE: QuickStartAudience = {
  prefKey: PREF_KEY,
  steps: STEPS,
  renderSupport: () => (
    <>
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
    </>
  ),
};

export function QuickStartModal() {
  return <QuickStartGuide audience={AUDIENCE} />;
}
