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

// installing / last_error* / install_log (GH #2056): the agent reports an install
// queued or running, and why the last install failed while the module is down.
type ModuleStatus = {
  installed: boolean;
  active: boolean;
  installing?: boolean;
  last_error?: string;
  last_error_at?: string;
  install_log?: string;
};

// errorDetail is the reason the panel gave for refusing a request, if any.
const errorDetail = (err: unknown): string | undefined => {
  const data = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data;
  return data?.detail ?? data?.error;
};

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
  // The latest status, for code that runs outside a render (the poll loop).
  const statusRef = useRef<Record<string, ModuleStatus>>({});

  const fetchStatus = async (): Promise<Record<string, ModuleStatus>> => {
    try {
      const resp = await apiClient.get<{ modules: Record<string, ModuleStatus> }>("/admin/settings/modules/status");
      const modules = resp.data?.modules ?? {};
      statusRef.current = modules;
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
        if (mounted.current) feedback.message.error("Failed to load module settings");
      } finally {
        if (mounted.current) setLoading(false);
      }
    })();
    return () => {
      mounted.current = false;
    };
  }, []);

  // Poll a module's status until it is installed+active, until the agent
  // reports a new failed install (GH #2056: its reason then shows on the card),
  // or until the attempt budget is exhausted. errorAtBefore is the last failure's
  // time from before this install started; a different value is a new failure.
  const pollUntilUp = async (statusKey: string, errorAtBefore: string | undefined) => {
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
        if (s?.last_error_at && s.last_error_at !== errorAtBefore && !s.installing) return; // failed
      }
    } finally {
      polling.current.delete(statusKey);
      if (mounted.current) setInstalling((p) => ({ ...p, [statusKey]: false }));
    }
  };

  const onToggle = async (key: ModuleKey, label: string, next: boolean) => {
    setSaving(key);
    const statusKey = STATUS_KEY[key];
    const errorAtBefore = statusKey ? statusRef.current[statusKey]?.last_error_at : undefined;
    try {
      await apiClient.patch("/admin/settings", { [key]: next });
      setState((prev) => ({ ...prev, [key]: next }));
      feedback.message.success(`${`${label} ${next ? "enabled" : "disabled"}`}: ${next && statusKey
            ? "Installing the module in the background — this can take a few minutes."
            : next
              ? "The module's pages are now available."
              : "The module's pages are hidden; its endpoints return 409."}`);
      if (next && statusKey) void pollUntilUp(statusKey, errorAtBefore);
    } catch (err) {
      const detail = errorDetail(err);
      feedback.message.error(detail ? `${label}: ${detail}` : `Failed to update ${label}`);
    } finally {
      setSaving(null);
    }
  };

  const onRetry = async (statusKey: string, label: string) => {
    const errorAtBefore = statusRef.current[statusKey]?.last_error_at;
    try {
      await apiClient.post("/admin/settings/modules/install", { key: statusKey });
      feedback.message.info(`Reinstalling ${label}…`);
      void pollUntilUp(statusKey, errorAtBefore);
    } catch (err) {
      const detail = errorDetail(err);
      feedback.message.error(detail ? `${label}: ${detail}` : `Failed to start install for ${label}`);
    }
  };

  // GH #2056: mail's install needs DNS installed and running (install.sh stops
  // without DNS's own zone). mailWaitsForDNS is the reason mail can't be turned
  // on or retried yet, or null. A DNS status the agent didn't report doesn't
  // block: the panel checks again when mail is turned on.
  const dnsStatus = status.dns;
  const mailWaitsForDNS = !state.dns_enabled
    ? "Turn on DNS first; mail needs it."
    : dnsStatus && !(dnsStatus.installed && dnsStatus.active)
      ? "DNS isn't running yet. Mail can be turned on once DNS shows active."
      : null;

  const renderStatus = (m: { key: ModuleKey; label: string }) => {
    const statusKey = STATUS_KEY[m.key];
    if (!statusKey || !state[m.key]) return null; // api-only, or module off
    if (installing[statusKey] || status[statusKey]?.installing) {
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
    // a retry so the operator isn't stuck (mail's waits for DNS).
    return (
      <Space size="small">
        <Tag color="error" style={{ marginInlineEnd: 0 }}>not installed</Tag>
        <Button
          size="small"
          icon={<ReloadOutlined />}
          disabled={m.key === "mail_enabled" && !!mailWaitsForDNS}
          onClick={() => onRetry(statusKey, m.label)}
        >
          Retry
        </Button>
      </Space>
    );
  };

  // renderNotes shows, under a module's description, why its last install
  // failed and where the full log is, and why mail can't be turned on yet.
  const renderNotes = (m: { key: ModuleKey }) => {
    const statusKey = STATUS_KEY[m.key];
    const s = statusKey ? status[statusKey] : undefined;
    const busy = statusKey ? installing[statusKey] || s?.installing : false;
    const failed = state[m.key] && s?.last_error && !busy && !(s.installed && s.active);
    const waits = m.key === "mail_enabled" && mailWaitsForDNS && !(state.mail_enabled && s?.installed && s?.active);
    if (!failed && !waits) return null;
    return (
      <>
        {failed && (
          <div style={{ marginTop: 4 }}>
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              Last install failed: {s?.last_error}
            </Typography.Text>
            {s?.install_log && (
              <>
                <br />
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  Full log: <Typography.Text code style={{ fontSize: 12 }}>{s.install_log}</Typography.Text>
                </Typography.Text>
              </>
            )}
          </div>
        )}
        {waits && (
          <div style={{ marginTop: 4 }}>
            <Typography.Text type="warning" style={{ fontSize: 12 }}>
              {mailWaitsForDNS}
            </Typography.Text>
          </div>
        )}
      </>
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
          // Wraps on narrow screens: the controls drop below the text instead of
          // squeezing it (an install error can be a long line).
          <div
            key={m.key}
            style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-start", justifyContent: "space-between", gap: 8, width: "100%" }}
          >
            <div style={{ flex: "1 1 260px", maxWidth: 520, minWidth: 0, overflowWrap: "anywhere" }}>
              <Typography.Text strong>{m.label}</Typography.Text>
              <br />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {m.desc}
              </Typography.Text>
              {renderNotes(m)}
            </div>
            <Space align="center" size="middle">
              {renderStatus(m)}
              <Switch
                aria-label={m.label}
                checked={state[m.key]}
                loading={saving === m.key}
                // Mail can always be turned off; turning it on waits for DNS.
                disabled={m.key === "mail_enabled" && !state.mail_enabled && !!mailWaitsForDNS}
                onChange={(next) => onToggle(m.key, m.label, next)}
                checkedChildren="On"
                unCheckedChildren="Off"
              />
            </Space>
          </div>
        ))}
      </Space>
    </Card>
  );
};
