import { useTranslation } from "react-i18next";
import { useEffect, useRef, useState } from "react";
import { Button, Card, Space, Spin, Switch, Tag, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { ReloadOutlined } from "@ant-design/icons";

import { apiClient } from "../../../apiClient";

// ModulesCard — Server Settings → Modules (M353, GH #353). Enable or disable
// optional panel modules server-wide. Turning a module ON sets the flag AND, for
// modules with a runtime install path (dns/mail/security/quota), triggers a
// background install: the agent runs install.sh --install-module <key>. This card
// polls the install status and shows installing / active / failed states. On
// failure the flag is deliberately NOT rolled back (the install is retryable via
// the Retry button + the reconciler's convergence pass), so the operator can
// recover without toggling off/on. Flags default ON so existing installs keep
// every feature.
type ModuleKey = "dns_enabled" | "mail_enabled" | "security_enabled" | "quota_enabled" | "api_enabled";

// The status/install keys drop the _enabled suffix (dns_enabled -> dns). api has
// no install path, so it has no status key.
const STATUS_KEY: Partial<Record<ModuleKey, string>> = {
  dns_enabled: "dns",
  mail_enabled: "mail",
  security_enabled: "security",
  quota_enabled: "quota",
};

const MODULES: { key: ModuleKey; label: string; desc: string }[] = [
  { key: "dns_enabled", label: "DNS server (PowerDNS)", desc: "Authoritative DNS + the domain records / DNSSEC pages." },
  { key: "mail_enabled", label: "Mail server (Stalwart + Bulwark)", desc: "Mailboxes, forwarders, webmail, and the Mail pages." },
  { key: "security_enabled", label: "Security (CrowdSec, malware/ClamAV, AppArmor)", desc: "Intrusion detection, malware scanning, and the Security page." },
  { key: "quota_enabled", label: "Filesystem quota", desc: "Per-user disk quota enforcement + the quota fields." },
  { key: "api_enabled", label: "REST API (API keys)", desc: "Remote-management API keys + the Personal API Tokens page." },
];

type ModuleStatus = { installed: boolean; active: boolean };

const POLL_INTERVAL_MS = 5000;
const POLL_MAX_ATTEMPTS = 60; // ~5 minutes; apt + downloads can be slow

export const ModulesCard = () => {
  const { t } = useTranslation();
  const [state, setState] = useState<Record<ModuleKey, boolean>>({
    dns_enabled: true,
    mail_enabled: true,
    security_enabled: true,
    quota_enabled: true,
    api_enabled: true,
  });
  const [status, setStatus] = useState<Record<string, ModuleStatus>>({});
  const [installing, setInstalling] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState<ModuleKey | null>(null);

  const mounted = useRef(true);
  const polling = useRef<Set<string>>(new Set());

  const fetchStatus = async (): Promise<Record<string, ModuleStatus>> => {
    try {
      const resp = await apiClient.get<{ modules: Record<string, ModuleStatus> }>("/admin/settings/modules/status");
      const modules = resp.data?.modules ?? {};
      if (mounted.current) setStatus(modules);
      return modules;
    } catch {
      return {};
    }
  };

  useEffect(() => {
    mounted.current = true;
    (async () => {
      try {
        const [settingsResp] = await Promise.all([
          apiClient.get<Partial<Record<ModuleKey, boolean>>>("/admin/settings"),
          fetchStatus(),
        ]);
        if (mounted.current) {
          setState((prev) => {
            const next = { ...prev };
            for (const m of MODULES) next[m.key] = settingsResp.data[m.key] !== false;
            return next;
          });
        }
      } catch {
        if (mounted.current) feedback.notification.error({ message: "Failed to load module settings" });
      } finally {
        if (mounted.current) setLoading(false);
      }
    })();
    return () => {
      mounted.current = false;
    };
  }, []);

  // Poll a module's status until it is installed+active, or until the attempt
  // budget is exhausted (leaving the "failed — retry" state visible).
  const pollUntilUp = async (statusKey: string) => {
    if (polling.current.has(statusKey)) return;
    polling.current.add(statusKey);
    setInstalling((p) => ({ ...p, [statusKey]: true }));
    try {
      for (let attempt = 0; attempt < POLL_MAX_ATTEMPTS; attempt += 1) {
        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS));
        if (!mounted.current) return;
        const modules = await fetchStatus();
        const s = modules[statusKey];
        if (s?.installed && s?.active) return; // converged
      }
    } finally {
      polling.current.delete(statusKey);
      if (mounted.current) setInstalling((p) => ({ ...p, [statusKey]: false }));
    }
  };

  const onToggle = async (key: ModuleKey, label: string, next: boolean) => {
    setSaving(key);
    try {
      await apiClient.patch("/admin/settings", { [key]: next });
      setState((prev) => ({ ...prev, [key]: next }));
      const statusKey = STATUS_KEY[key];
      feedback.notification.success({
        message: `${label} ${next ? "enabled" : "disabled"}`,
        description:
          next && statusKey
            ? "Installing the module in the background — this can take a few minutes."
            : next
              ? "The module's pages are now available."
              : "The module's pages are hidden; its endpoints return 409.",
      });
      if (next && statusKey) void pollUntilUp(statusKey);
    } catch {
      feedback.notification.error({ message: `Failed to update ${label}` });
    } finally {
      setSaving(null);
    }
  };

  const onRetry = async (statusKey: string, label: string) => {
    try {
      await apiClient.post("/admin/settings/modules/install", { key: statusKey });
      feedback.notification.info({ message: `Reinstalling ${label}…` });
      void pollUntilUp(statusKey);
    } catch {
      feedback.notification.error({ message: `Failed to start install for ${label}` });
    }
  };

  const renderStatus = (m: { key: ModuleKey; label: string }) => {
    const statusKey = STATUS_KEY[m.key];
    if (!statusKey || !state[m.key]) return null; // api-only, or module off
    if (installing[statusKey]) {
      return (
        <Tag color="processing" style={{ marginInlineEnd: 0 }}>
          installing… <Spin size="small" style={{ marginInlineStart: 6 }} />
        </Tag>
      );
    }
    const s = status[statusKey];
    if (s?.installed && s?.active) {
      return <Tag color="success" style={{ marginInlineEnd: 0 }}>active</Tag>;
    }
    // Enabled but not installed+active — the "flag on, service down" state. Offer
    // a retry so the operator isn't stuck.
    return (
      <Space size="small">
        <Tag color="error" style={{ marginInlineEnd: 0 }}>not installed</Tag>
        <Button size="small" icon={<ReloadOutlined />} onClick={() => onRetry(statusKey, m.label)}>
          Retry
        </Button>
      </Space>
    );
  };

  return (
    <Card title={t("modulescard.modules")} style={{ marginBottom: 16 }} loading={loading}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Turn optional modules on or off. Enabling a module installs its software in
        the background (the panel hides a module's pages when it's off; existing
        data is not removed). Core services (web, database, panel) are always on.
      </Typography.Paragraph>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        {MODULES.map((m) => (
          <Space key={m.key} align="start" style={{ width: "100%", justifyContent: "space-between" }}>
            <div style={{ maxWidth: 520 }}>
              <Typography.Text strong>{m.label}</Typography.Text>
              <br />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {m.desc}
              </Typography.Text>
            </div>
            <Space align="center" size="middle">
              {renderStatus(m)}
              <Switch
                checked={state[m.key]}
                loading={saving === m.key}
                onChange={(next) => onToggle(m.key, m.label, next)}
                checkedChildren="On"
                unCheckedChildren="Off"
              />
            </Space>
          </Space>
        ))}
      </Space>
    </Card>
  );
};
