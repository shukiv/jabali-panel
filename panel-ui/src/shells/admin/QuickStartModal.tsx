// QuickStartModal — admin-shell adapter for the shared QuickStartGuide.
// Supplies the admin audience: preference key, the six admin destinations,
// and the admin support callout (Support link + docs + 📖). All loading,
// dismissal, and layout live in the Module.
import { Typography } from "antd";
import { QuestionCircleOutlined } from "@icons";
import { Link } from "react-router";

import {
  QuickStartGuide,
  type QuickStartAudience,
  type QuickStartStep,
} from "../../components/quickstart/QuickStartGuide";

const PREF_KEY = "quickstart_admin";

const STEPS: QuickStartStep[] = [
  {
    number: 1,
    title: "Server Settings",
    desc: "Set your hostname, primary IPs, and nameservers (ns1/ns2).",
    href: "/jabali-admin/settings",
    color: "#2563eb", // blue
  },
  {
    number: 2,
    title: "Hosting Packages",
    desc: "Define disk/bandwidth quota, PHP version, and resource limits for tenants.",
    href: "/jabali-admin/packages",
    color: "#10b981", // green
  },
  {
    number: 3,
    title: "Users",
    desc: "Create your first tenant account.",
    href: "/jabali-admin/users",
    color: "#a855f7", // purple
  },
  {
    number: 4,
    title: "Domains",
    desc: "Add a domain — vhost, DNS zone, and SSL provision automatically.",
    href: "/jabali-admin/domains",
    color: "#f59e0b", // amber
  },
  {
    number: 5,
    title: "SSL Manager",
    desc: "Let's Encrypt issues automatically. Track + retry from here.",
    href: "/jabali-admin/ssl",
    color: "#22c55e", // emerald
  },
  {
    number: 6,
    title: "Applications",
    desc: "1-click install WordPress and other apps for any user.",
    href: "/jabali-admin/applications",
    color: "#3b82f6", // bright blue
  },
];

const AUDIENCE: QuickStartAudience = {
  prefKey: PREF_KEY,
  steps: STEPS,
  renderSupport: (close) => (
    <>
      <QuestionCircleOutlined
        style={{ fontSize: 24, color: "#3b82f6", flexShrink: 0 }}
      />
      <Typography.Text style={{ flex: 1 }}>
        Stuck? Use{" "}
        <Link to="/jabali-admin/support" onClick={close}>
          Support
        </Link>{" "}
        or read the docs at{" "}
        <a
          href="https://jabali-panel.com"
          target="_blank"
          rel="noopener noreferrer"
        >
          jabali-panel.com
        </a>
        .
      </Typography.Text>
      <span
        role="img"
        aria-label="docs"
        style={{ fontSize: 28, flexShrink: 0 }}
      >
        📖
      </span>
    </>
  ),
};

export function QuickStartModal() {
  return <QuickStartGuide audience={AUDIENCE} />;
}
