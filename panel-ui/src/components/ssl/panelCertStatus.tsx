// panelCertStatus — status tag + expiry hint for panel_certificate rows,
// shared by the Settings PanelSSLCard and the SSL Manager's SYSTEM band
// so the two surfaces can never drift.
import { Tag } from "antd";
import type { PanelCertificate } from "../../hooks/usePanelCertificate";

export function panelCertStatusTag(c: PanelCertificate) {
  switch (c.status) {
    case "issued":
      return (
        <Tag color="success">
          Issued by Let&apos;s Encrypt{c.staging ? " (staging)" : ""}
        </Tag>
      );
    case "pending_acme":
      return <Tag color="processing">Issuing…</Tag>;
    case "pending_acme_retry":
      return <Tag color="warning">Pending retry</Tag>;
    case "failed":
      return <Tag color="error">Failed</Tag>;
    case "self_signed":
    default:
      return <Tag>Self-signed</Tag>;
  }
}

export function panelCertExpiryHint(c: PanelCertificate): string | null {
  if (c.status !== "issued" || !c.expires_at) return null;
  const ms = new Date(c.expires_at).getTime() - Date.now();
  if (Number.isNaN(ms)) return null;
  const days = Math.floor(ms / (24 * 3600 * 1000));
  if (days < 0) return "Expired";
  return `Expires in ${days} day${days === 1 ? "" : "s"}`;
}
