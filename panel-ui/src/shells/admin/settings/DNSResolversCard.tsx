import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  Row,
  Space,
  Tag,
  Typography,
  notification,
} from "antd";
import { SaveOutlined } from "@icons";

import { apiClient } from "../../../apiClient";

// ResolverGet matches the GET /system/resolvers response shape. source tells
// us whether we're reading the panel-owned drop-in ("drop-in") or there's
// nothing there yet ("none").
type ResolverGet = {
  active: boolean;
  resolvers: string[];
  search_domain: string;
  source: "drop-in" | "system" | "none";
};

// Provider templates. IPv4 primary/secondary + IPv6 primary/secondary so the
// form maps cleanly. When the admin clicks a preset, we fill all four slots
// (empty strings for providers without a second IPv6). Operator can prune
// before saving — we only send non-empty entries.
type Provider = {
  key: string;
  label: string;
  description: string;
  ipv4: [string, string];
  ipv6: [string, string];
};

const providers: Provider[] = [
  {
    key: "cloudflare",
    label: "Cloudflare",
    description: "Fast, privacy-focused public resolver.",
    ipv4: ["1.1.1.1", "1.0.0.1"],
    ipv6: ["2606:4700:4700::1111", "2606:4700:4700::1001"],
  },
  {
    key: "cloudflare-family",
    label: "Cloudflare Family",
    description: "Blocks malware and adult content.",
    ipv4: ["1.1.1.3", "1.0.0.3"],
    ipv6: ["2606:4700:4700::1113", "2606:4700:4700::1003"],
  },
  {
    key: "google",
    label: "Google Public DNS",
    description: "Global anycast resolver operated by Google.",
    ipv4: ["8.8.8.8", "8.8.4.4"],
    ipv6: ["2001:4860:4860::8888", "2001:4860:4860::8844"],
  },
  {
    key: "quad9",
    label: "Quad9",
    description: "Security-first resolver, blocks known malicious hosts.",
    ipv4: ["9.9.9.9", "149.112.112.112"],
    ipv6: ["2620:fe::fe", "2620:fe::9"],
  },
  {
    key: "opendns",
    label: "OpenDNS",
    description: "Cisco-operated resolver with optional filtering.",
    ipv4: ["208.67.222.222", "208.67.220.220"],
    ipv6: ["2620:119:35::35", "2620:119:53::53"],
  },
  {
    key: "adguard",
    label: "AdGuard DNS",
    description: "Blocks ads and trackers at the resolver.",
    ipv4: ["94.140.14.14", "94.140.15.15"],
    ipv6: ["2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"],
  },
  {
    key: "mullvad",
    label: "Mullvad",
    description: "Privacy-oriented resolver run by Mullvad VPN.",
    ipv4: ["194.242.2.2", ""],
    ipv6: ["2a07:e340::2", ""],
  },
  {
    key: "controld",
    label: "Control D",
    description: "Unfiltered resolver by Control D.",
    ipv4: ["76.76.2.0", "76.76.10.0"],
    ipv6: ["2606:1a40::", "2606:1a40:1::"],
  },
];

type FormShape = {
  ipv4_primary: string;
  ipv4_secondary: string;
  ipv6_primary: string;
  ipv6_secondary: string;
  search_domain: string;
};

// populateFromGet splits the flat resolvers[] into v4/v6 slots for the form.
// systemd-resolved doesn't care about order, but the UI wants stable slots
// so toggling between presets feels predictable.
function populateFromGet(get: ResolverGet): FormShape {
  const v4: string[] = [];
  const v6: string[] = [];
  for (const r of get.resolvers ?? []) {
    (r.includes(":") ? v6 : v4).push(r);
  }
  return {
    ipv4_primary: v4[0] ?? "",
    ipv4_secondary: v4[1] ?? "",
    ipv6_primary: v6[0] ?? "",
    ipv6_secondary: v6[1] ?? "",
    search_domain: get.search_domain ?? "",
  };
}

// collectResolvers strips empties and preserves admin intent (order: v4
// primary, v4 secondary, v6 primary, v6 secondary).
function collectResolvers(values: FormShape): string[] {
  return [values.ipv4_primary, values.ipv4_secondary, values.ipv6_primary, values.ipv6_secondary]
    .map((s) => (s ?? "").trim())
    .filter((s) => s.length > 0);
}

// matchingProviderKey returns the preset whose full address set equals the
// resolvers currently in use, so the UI can show WHICH provider is active
// (GH #912 — without this the admin only sees bare IPs and can't tell). A
// set-equality match (order-independent) against each provider's non-empty
// entries; null when the current list matches no preset (custom/mixed).
function matchingProviderKey(current: string[]): string | null {
  const cur = new Set(current.map((s) => s.toLowerCase()));
  for (const p of providers) {
    const want = [...p.ipv4, ...p.ipv6].filter((s) => s.length > 0).map((s) => s.toLowerCase());
    if (want.length === cur.size && want.every((s) => cur.has(s))) {
      return p.key;
    }
  }
  return null;
}

// Post-M21: useNotification shim. See ServerSettingsPage.tsx for why.
type NotifyInput = {
  type?: "success" | "error" | "warning" | "info";
  message: string;
  description?: React.ReactNode;
};
type NotifyFn = (input: NotifyInput) => void;
function notifyError(notify: NotifyFn, title: string, err: unknown) {
  const e = err as { response?: { data?: { detail?: string } }; message?: string };
  notify({
    type: "error",
    message: title,
    description: e.response?.data?.detail ?? e.message ?? "Unknown error",
  });
}

export const DNSResolversCard = () => {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormShape>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [source, setSource] = useState<ResolverGet["source"]>("none");
  const [active, setActive] = useState<boolean>(true);
  // GH #912: the resolvers actually in use, for the "Currently in use"
  // readout + highlighting the matching preset.
  const [current, setCurrent] = useState<string[]>([]);
  const activePreset = matchingProviderKey(current);
  const notify: NotifyFn = (input) =>
    notification.open({
      message: input.message,
      description: input.description,
      type: input.type,
    });

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiClient.get<ResolverGet>("/system/resolvers");
        if (cancelled) return;
        form.setFieldsValue(populateFromGet(resp.data));
        setSource(resp.data.source);
        setActive(resp.data.active);
        setCurrent(resp.data.resolvers ?? []);
      } catch (err) {
        notifyError(notify, "Failed to load DNS resolvers", err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const applyPreset = (p: Provider) => {
    form.setFieldsValue({
      ipv4_primary: p.ipv4[0],
      ipv4_secondary: p.ipv4[1],
      ipv6_primary: p.ipv6[0],
      ipv6_secondary: p.ipv6[1],
    });
  };

  const handleSubmit = async (values: FormShape) => {
    setSaving(true);
    try {
      const resp = await apiClient.put<ResolverGet>("/system/resolvers", {
        resolvers: collectResolvers(values),
        search_domain: (values.search_domain ?? "").trim(),
      });
      notify({ type: "success", message: "DNS resolvers saved" });
      form.setFieldsValue(populateFromGet(resp.data));
      setSource(resp.data.source);
      setActive(resp.data.active);
      setCurrent(resp.data.resolvers ?? []);
    } catch (err) {
      notifyError(notify, "Failed to save DNS resolvers", err);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title={t("dnsresolverscard.dns_resolvers")} style={{ marginBottom: 16 }}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Set the upstream DNS servers this server uses to resolve hostnames.
        Writes a drop-in at <code>/etc/systemd/resolved.conf.d/jabali.conf</code>
        {" "}and restarts <code>systemd-resolved</code>. Falls back gracefully
        if the restart fails.
      </Typography.Paragraph>

      <Space wrap style={{ marginBottom: 12 }}>
        <Typography.Text type="secondary">Status:</Typography.Text>
        {active ? (
          <Tag color="green">systemd-resolved active</Tag>
        ) : (
          <Tag color="orange">systemd-resolved inactive</Tag>
        )}
        <Tag>Source: {source}</Tag>
      </Space>

      {/* GH #912: an at-a-glance readout of the resolvers actually in use, so
          the admin doesn't have to recognise bare IPs to know their provider. */}
      <div style={{ marginBottom: 12 }}>
        <Typography.Text type="secondary">Currently in use: </Typography.Text>
        {current.length === 0 ? (
          <Typography.Text type="secondary">system default (no panel drop-in)</Typography.Text>
        ) : (
          <Space size={[4, 4]} wrap>
            {activePreset && (
              <Tag color="blue">
                {providers.find((p) => p.key === activePreset)?.label}
              </Tag>
            )}
            {current.map((r) => (
              <Tag key={r} style={{ fontFamily: "monospace" }}>
                {r}
              </Tag>
            ))}
          </Space>
        )}
      </div>

      {!active && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          title="systemd-resolved is not active"
          description={t("dnsresolverscard.the_drop_in_will_still_be_written_but_change")}
        />
      )}

      <Form
        form={form}
        layout="vertical"
        onFinish={handleSubmit}
        disabled={loading}
      >
        <Typography.Text strong>Presets</Typography.Text>
        <div style={{ marginTop: 8, marginBottom: 16 }}>
          <Space size={[8, 8]} wrap>
            {providers.map((p) => (
              <Button
                key={p.key}
                onClick={() => applyPreset(p)}
                title={p.description}
                // GH #912: highlight the preset matching the resolvers in use.
                type={p.key === activePreset ? "primary" : "default"}
              >
                {p.label}
                {p.key === activePreset ? " ✓" : ""}
              </Button>
            ))}
          </Space>
        </div>

        <Row gutter={16}>
          <Col span={12}>
            <Form.Item
              label={t("dnsresolverscard.ipv4_primary")}
              name="ipv4_primary"
              rules={[
                {
                  pattern: /^$|^[0-9]{1,3}(\.[0-9]{1,3}){3}$/,
                  message: "Invalid IPv4",
                },
              ]}
            >
              <Input placeholder="1.1.1.1" />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item
              label={t("dnsresolverscard.ipv4_secondary_optional")}
              name="ipv4_secondary"
              rules={[
                {
                  pattern: /^$|^[0-9]{1,3}(\.[0-9]{1,3}){3}$/,
                  message: "Invalid IPv4",
                },
              ]}
            >
              <Input placeholder="1.0.0.1" />
            </Form.Item>
          </Col>
        </Row>

        <Row gutter={16}>
          <Col span={12}>
            <Form.Item
              label={t("dnsresolverscard.ipv6_primary_optional")}
              name="ipv6_primary"
              rules={[
                {
                  pattern: /^$|^[0-9a-fA-F:]+$/,
                  message: "Invalid IPv6",
                },
              ]}
            >
              <Input placeholder="2606:4700:4700::1111" />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item
              label={t("dnsresolverscard.ipv6_secondary_optional")}
              name="ipv6_secondary"
              rules={[
                {
                  pattern: /^$|^[0-9a-fA-F:]+$/,
                  message: "Invalid IPv6",
                },
              ]}
            >
              <Input placeholder="2606:4700:4700::1001" />
            </Form.Item>
          </Col>
        </Row>

        <Form.Item
          label={t("dnsresolverscard.search_domain_optional")}
          name="search_domain"
          extra="Appended to unqualified hostnames (systemd-resolved Domains=)."
          rules={[
            {
              pattern:
                /^$|^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/,
              message: "Invalid domain",
            },
          ]}
        >
          <Input placeholder="example.com" style={{ maxWidth: 360 }} />
        </Form.Item>

        <Space>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saving}
            htmlType="submit"
          >
            Save Resolvers
          </Button>
        </Space>
      </Form>
    </Card>
  );
};
