// MailSyncInfoModal — shows a mailbox's CalDAV (calendar) and CardDAV
// (contacts) URLs so the tenant can add them to Thunderbird / Apple Mail
// / any DAV client (GH #1039 follow-up). The DAV endpoints live on the
// domain's mail host and Stalwart keys collections by the full email
// address, so the URLs are derived client-side — no API call needed.
//
// Why this exists: Thunderbird auto-discovers CALENDARS during mail setup
// but not CONTACTS, and its manual CardDAV dialog needs the exact URL.
// Surfacing both ready-to-paste removes the guesswork.
import { Modal, Typography, Alert, Space } from "antd";

interface MailSyncInfoModalProps {
  open: boolean;
  email: string;
  domain: string;
  onClose: () => void;
}

interface DavRow {
  label: string;
  url: string;
}

export function MailSyncInfoModal({ open, email, domain, onClose }: MailSyncInfoModalProps) {
  const base = domain ? `https://mail.${domain}` : "";
  const rows: DavRow[] = email && domain
    ? [
        { label: "Calendar (CalDAV)", url: `${base}/dav/cal/${email}/` },
        { label: "Contacts (CardDAV)", url: `${base}/dav/card/${email}/` },
      ]
    : [];

  return (
    <Modal
      open={open}
      title={email ? `Calendar & contacts — ${email}` : "Calendar & contacts"}
      onCancel={onClose}
      onOk={onClose}
      okText="Done"
      cancelButtonProps={{ style: { display: "none" } }}
      destroyOnClose
    >
      <Typography.Paragraph type="secondary">
        Add your calendar and contacts to Thunderbird, Apple Mail, or any
        CalDAV/CardDAV client with these URLs. Sign in with your full email
        address and mailbox password.
      </Typography.Paragraph>

      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        {rows.map((r) => (
          <div key={r.label}>
            <Typography.Text strong>{r.label}</Typography.Text>
            <br />
            <Typography.Text copyable={{ text: r.url }} code style={{ wordBreak: "break-all" }}>
              {r.url}
            </Typography.Text>
          </div>
        ))}
      </Space>

      <Alert
        style={{ marginTop: 16 }}
        type="info"
        showIcon
        message="Thunderbird tip"
        description={
          <span>
            Thunderbird auto-detects your calendar during mail setup, but not
            your contacts. Add contacts with <b>Address Book → New → CardDAV
            Address Book</b> and paste the Contacts URL above.
          </span>
        }
      />
    </Modal>
  );
}
