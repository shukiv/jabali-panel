// panelCertStatus — status tag + expiry hint for panel_certificate rows,
// shared by SSL Manager → Panel SSL and the Server Settings summary card
// so the two surfaces can never drift.
import { Tag } from "antd";
import {
  isPanelSelfSignResult,
  type PanelCertIssueResult,
  type PanelCertificate,
  type PanelServedCert,
} from "../../hooks/usePanelCertificate";
import { shortDate } from "../../utils/datetime";

// PANEL_SSL_TAB is where the panel's own certs are managed (GH #2166).
export const PANEL_SSL_TAB = "/jabali-admin/ssl?tab=panel";

// leOn is the hostname row's use_le, which governs both certs. With Let's
// Encrypt off (GH #2166) the panel serves its own certificates, so the tag
// names the certificate the file holds: the row's Let's Encrypt state (a
// retry, a failure) no longer applies, and the mail row can keep one from
// before the toggle, which writes only the hostname row.
export function panelCertStatusTag(c: PanelCertificate, leOn: boolean) {
  const s = c.served;
  if (!leOn) {
    if (!s) return <Tag>Self-signed</Tag>;
    switch (s.type) {
      case "self_signed":
        return <Tag color="success">Issued (self-signed)</Tag>;
      case "ca":
        return <Tag color="success">Issued by {s.issuer}</Tag>;
      default:
        return (
          <Tag color="success">
            Issued by Let&apos;s Encrypt{s.type === "lets_encrypt_staging" ? " (staging)" : ""}
          </Tag>
        );
    }
  }
  switch (c.status) {
    case "issued":
      // A kept CA cert (Let's Encrypt off, GH #2166) is "issued" too: name
      // its own issuer, not Let's Encrypt.
      if (c.served?.type === "ca") {
        return <Tag color="success">Issued by {c.served.issuer}</Tag>;
      }
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

// panelCertTypeLabel names the kind of certificate a cert file holds.
export function panelCertTypeLabel(s: PanelServedCert): string {
  switch (s.type) {
    case "self_signed":
      return "Self-signed";
    case "lets_encrypt":
      return "Let's Encrypt";
    case "lets_encrypt_staging":
      return "Let's Encrypt (staging)";
    default:
      return "Certificate authority";
  }
}

// panelCertValidity: the served cert's expiry in the admin's date format,
// with a warning from 30 days out and an error once it has expired.
export function panelCertValidity(s: PanelServedCert, now: number = Date.now()) {
  const ms = new Date(s.not_after).getTime() - now;
  const days = Math.floor(ms / (24 * 3600 * 1000));
  const until = shortDate(s.not_after);
  if (Number.isNaN(ms)) return { until, tag: null };
  if (ms <= 0) return { until, tag: <Tag color="error">Expired</Tag> };
  const left = `Expires in ${days} day${days === 1 ? "" : "s"}`;
  return { until, tag: <Tag color={days <= 30 ? "warning" : undefined}>{left}</Tag> };
}

export function panelCertExpiryHint(c: PanelCertificate): string | null {
  if (c.status !== "issued" || !c.expires_at) return null;
  const ms = new Date(c.expires_at).getTime() - Date.now();
  if (Number.isNaN(ms)) return null;
  const days = Math.floor(ms / (24 * 3600 * 1000));
  if (days < 0) return "Expired";
  return `Expires in ${days} day${days === 1 ? "" : "s"}`;
}

// GH #2166: with Let's Encrypt off the panel serves self-signed certs, so
// each cert's action regenerates its own instead of asking Let's Encrypt.
// leOn is the hostname row's use_le, which governs both certs: the toggle
// writes only that row, so the mail row's own flag can be stale.
export function panelCertRegeneratesSelfSigned(leOn: boolean): boolean {
  return !leOn;
}

export function panelCertActionLabel(leOn: boolean): string {
  return panelCertRegeneratesSelfSigned(leOn) ? "Regenerate self-signed" : "Issue / retry";
}

export function panelCertActionConfirm(
  c: PanelCertificate,
  label: string,
  leOn: boolean,
): { title: string; description?: string } {
  if (!panelCertRegeneratesSelfSigned(leOn)) {
    return { title: `Issue the ${label.toLowerCase()} cert now?` };
  }
  if (c.kind === "mail") {
    return {
      title: "Regenerate the self-signed mail certificate?",
      description:
        "The panel regenerates it only when it no longer fits the mail names or has expired. Webmail and the mail server then serve it, and mail apps and browsers ask you to accept the new certificate.",
    };
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
