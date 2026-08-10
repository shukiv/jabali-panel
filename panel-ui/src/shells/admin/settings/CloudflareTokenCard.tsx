// CloudflareTokenCard — the Cloudflare API token behind the ACME DNS-01
// fallback (JAB-235). A CDN-fronted customer domain cannot pass HTTP-01
// (the edge answers the challenge path), so the panel writes the
// _acme-challenge TXT straight into the customer's Cloudflare zone through
// this token. Write-only surface: the field is masked, the server verifies
// the token against Cloudflare before storing it sealed, and status only
// ever reports configured yes/no — the token is never displayed back.
import { useCallback, useEffect, useState } from "react";
import { SafetyOutlined } from "@icons";
import { Alert, Button, Card, Input, Popconfirm, Space, Tag, Typography } from "antd";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

interface TokenStatus {
  configured: boolean;
}

interface TokenSetResult {
  configured: boolean;
  zones: number;
}

export function CloudflareTokenCard() {
  const [configured, setConfigured] = useState<boolean | null>(null);
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [clearing, setClearing] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const r = await apiClient.get<TokenStatus>("/admin/settings/cloudflare-token");
      setConfigured(r.data.configured);
    } catch {
      setConfigured(null);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const save = async () => {
    const trimmed = token.trim();
    if (!trimmed) return;
    setSaving(true);
    try {
      const r = await apiClient.put<TokenSetResult>("/admin/settings/cloudflare-token", {
        token: trimmed,
      });
      setToken("");
      setConfigured(true);
      feedback.message.success(
        `Token verified and stored — it can see ${r.data.zones} Cloudflare zone${r.data.zones === 1 ? "" : "s"}.`,
      );
    } catch (e) {
      feedback.message.error(
        e instanceof Error && e.message
          ? e.message
          : "Cloudflare rejected the token — check its scopes (Zone:DNS:Edit + Zone:Read).",
      );
    } finally {
      setSaving(false);
    }
  };

  const clear = async () => {
    setClearing(true);
    try {
      await apiClient.delete("/admin/settings/cloudflare-token");
      setConfigured(false);
      feedback.message.success("Cloudflare API token cleared.");
    } catch {
      feedback.message.error("Could not clear the token.");
    } finally {
      setClearing(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <SafetyOutlined />
          Cloudflare DNS-01 (SSL for proxied domains)
        </Space>
      }
      extra={
        configured === null ? null : configured ? (
          <Tag color="success">Token configured</Tag>
        ) : (
          <Tag>Not configured</Tag>
        )
      }
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        Domains fronted by Cloudflare (orange-cloud) cannot pass the normal HTTP
        certificate check, so they are stranded on a self-signed certificate.
        With an API token stored here, the panel completes the DNS challenge
        directly in the customer&apos;s Cloudflare zone and issues a real
        Let&apos;s Encrypt certificate for the origin. Use a dedicated token
        scoped to <code>Zone:DNS:Edit</code> + <code>Zone:Read</code> on the
        zones it should cover — it is a powerful credential, stored encrypted,
        and never shown again after saving.
      </Typography.Paragraph>
      <Space.Compact style={{ width: "100%", maxWidth: 480 }}>
        <Input.Password
          placeholder={configured ? "Replace the stored token…" : "Cloudflare API token"}
          value={token}
          onChange={(e) => setToken(e.target.value)}
          onPressEnter={() => void save()}
          autoComplete="off"
        />
        <Button type="primary" loading={saving} disabled={!token.trim()} onClick={() => void save()}>
          Verify &amp; save
        </Button>
      </Space.Compact>
      {configured ? (
        <div style={{ marginTop: 12 }}>
          <Popconfirm
            title="Clear the stored token?"
            description="Cloudflare-hosted zones lose the DNS-01 fallback until a new token is saved."
            onConfirm={() => void clear()}
          >
            <Button danger loading={clearing}>
              Clear token
            </Button>
          </Popconfirm>
        </div>
      ) : (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message="Without a token, Cloudflare-fronted domains are parked with a clear reason instead of retrying Let's Encrypt."
        />
      )}
    </Card>
  );
}
