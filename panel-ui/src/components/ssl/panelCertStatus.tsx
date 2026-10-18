// panelCertStatus — status tag + expiry hint for panel_certificate rows,
// shared by the Settings PanelSSLCard and the SSL Manager's SYSTEM band
// so the two surfaces can never drift.
import { Tag } from "antd";
import {
  isPanelSelfSignResult,
  type PanelCertIssueResult,
  type PanelCertificate,
} from "../../hooks/usePanelCertificate";
import { shortDate } from "../../utils/datetime";

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

// GH #2166: with Let's Encrypt off the panel serves a self-signed hostname
// cert, so the hostname row's action regenerates it instead of asking Let's
// Encrypt. The mail row stays a Let's Encrypt action.
export function panelCertRegeneratesSelfSigned(c: PanelCertificate): boolean {
  return c.kind === "hostname" && !c.use_le;
}

export function panelCertActionLabel(c: PanelCertificate): string {
  return panelCertRegeneratesSelfSigned(c) ? "Regenerate self-signed" : "Issue / retry";
}

export function panelCertActionConfirm(c: PanelCertificate, label: string): { title: string; description?: string } {
  if (!panelCertRegeneratesSelfSigned(c)) {
    return { title: `Issue the ${label.toLowerCase()} cert now?` };
  }
  return {
    title: "Regenerate the self-signed certificate?",
    description:
      "The panel regenerates it only when it no longer fits the hostname or has expired. It then restarts to serve it, and your browser asks you to accept the new certificate.",
  };
}

// panelCertIssueMessage: the toast after an Issue. A self-signed answer is
// the server's own words, plus the current cert's expiry in the admin's
// date format.
export function panelCertIssueMessage(kind: string, r: PanelCertIssueResult | undefined): string {
  if (!isPanelSelfSignResult(r)) return `${kind} cert: issued`;
  return r.expires_at ? `${r.detail} Valid until ${shortDate(r.expires_at)}.` : r.detail;
}
