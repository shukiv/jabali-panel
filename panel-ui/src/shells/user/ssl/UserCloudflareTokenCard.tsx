// UserCloudflareTokenCard — the user's own Cloudflare API token for DNS-01
// (GH #2032). A domain proxied by Cloudflare (orange cloud) cannot pass the
// HTTP certificate check, so the panel writes the DNS check record in the
// user's Cloudflare zone instead. The user's token is tried first for their
// own domains; the server's token, if the administrator set one, is the
// fallback. Write-only surface: the field is masked, the server checks the
// token can add and delete a record in the zones holding the user's
// domains before storing it sealed, and the token is never shown back.
import { useCallback, useEffect, useState } from "react";
import { SafetyOutlined } from "@icons";
import { Button, Card, Input, Popconfirm, Space, Tag, Typography } from "antd";
import { apiClient } from "../../../apiClient";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { Timestamp } from "../../../components/Timestamp";

interface TokenStatus {
  configured: boolean;
  verified_at?: string;
  zones?: string[];
  zones_not_checked?: number;
}

// errorText is the reason the panel gave, if any.
const errorText = (err: unknown, fallback: string): string => {
  const data = (err as { response?: { data?: { detail?: string } } })?.response?.data;
  if (data?.detail) return data.detail;
  return err instanceof Error && err.message ? err.message : fallback;
};

export function UserCloudflareTokenCard() {
  const [status, setStatus] = useState<TokenStatus | null>(null);
  const [notChecked, setNotChecked] = useState(0);
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [removing, setRemoving] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const r = await apiClient.get<TokenStatus>("/me/cloudflare-token");
      setStatus(r.data);
    } catch {
      setStatus(null);
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
      const r = await apiClient.put<TokenStatus>("/me/cloudflare-token", { token: trimmed });
      setToken("");
      setStatus(r.data);
      setNotChecked(r.data.zones_not_checked ?? 0);
      const zones = r.data.zones ?? [];
      feedback.message.success(
        zones.length > 0 ? `Token verified in ${zones.join(", ")} and saved.` : "Token verified and saved.",
      );
    } catch (e) {
      feedback.message.error(errorText(e, "The token could not be verified."));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    setRemoving(true);
    try {
      await apiClient.delete("/me/cloudflare-token");
      setStatus({ configured: false });
      setNotChecked(0);
      feedback.message.success("Cloudflare token removed.");
    } catch (e) {
      feedback.message.error(errorText(e, "Could not remove the token."));
    } finally {
      setRemoving(false);
    }
  };

  const configured = status?.configured === true;

  return (
    <Card
      title={
        <Space>
          <SafetyOutlined />
          Cloudflare DNS check (SSL for proxied domains)
        </Space>
      }
      extra={status === null ? null : configured ? <Tag color="success">Token saved</Tag> : <Tag>Not set</Tag>}
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
        If a domain&apos;s DNS is at Cloudflare and proxied (orange cloud), Let&apos;s Encrypt cannot reach
        this server to check it. With your Cloudflare API token, the panel adds the check record in your
        Cloudflare zone instead, and issues the certificate. Your token is used only for your own domains.
        Without one, the server&apos;s token is used if the administrator has set one.
      </Typography.Paragraph>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        In Cloudflare, create an API token (My Profile → API Tokens) with the permissions{" "}
        <code>Zone · Zone · Read</code> and <code>Zone · DNS · Edit</code>, for the zones of your domains
        only. Before saving, the panel adds and deletes a test record, <code>_jabali-dns01-check</code>, in each
        of those zones. The token is stored encrypted and never shown again.
      </Typography.Paragraph>
      <Space.Compact style={{ width: "100%", maxWidth: 480 }}>
        <Input.Password
          aria-label="Cloudflare API token"
          placeholder={configured ? "Replace the saved token…" : "Cloudflare API token"}
          value={token}
          onChange={(e) => setToken(e.target.value)}
          onPressEnter={() => void save()}
          autoComplete="off"
        />
        <Button type="primary" loading={saving} disabled={!token.trim()} onClick={() => void save()}>
          Verify &amp; save
        </Button>
      </Space.Compact>
      {configured && status?.verified_at ? (
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          Verified <Timestamp value={status.verified_at} />.
          {notChecked > 0
            ? ` ${notChecked} more zone${notChecked === 1 ? "" : "s"} holding your domains ${notChecked === 1 ? "was" : "were"} not checked.`
            : ""}
        </Typography.Paragraph>
      ) : null}
      {configured ? (
        <div style={{ marginTop: 12 }}>
          <Popconfirm
            title="Remove your Cloudflare token?"
            description="Your domains then use the server's Cloudflare token, if the administrator has set one."
            okText="Remove"
            onConfirm={() => void remove()}
          >
            <Button danger loading={removing}>
              Remove token
            </Button>
          </Popconfirm>
        </div>
      ) : null}
    </Card>
  );
}
