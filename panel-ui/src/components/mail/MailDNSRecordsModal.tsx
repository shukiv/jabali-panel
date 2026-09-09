// MailDNSRecordsModal — GH #1612 (johnnyq). When a mail domain's DNS is hosted
// externally (no panel-managed zone), the panel can't publish the mail records
// itself. This modal shows exactly what the operator must add at their own DNS
// provider — the mail host A/AAAA, MX, SPF, DKIM, DMARC, autodiscover/SRV,
// TLS-RPT and CAA — each with a copy button, plus a "Copy all" that emits
// zone-file lines for bulk import.
//
// The record set is the same one GET /domains/:id/email already returns (the
// panel's authoritative mail-record list), so this can never drift from what a
// panel-hosted zone would publish. The live "status" column is dropped here on
// purpose: with no panel zone there's nothing to compare against.
import { Alert, Button, Modal, Space, Table, Typography, type TableColumnsType } from "antd";
import { CopyOutlined } from "@icons";
import { feedback } from "../../lib/feedback";
import { useDomainEmail, type DomainEmailDNSHint } from "../../hooks/useMailboxes";

interface Props {
  domainId?: string;
  domainName?: string;
  open: boolean;
  onClose: () => void;
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    feedback.message.success("Copied to clipboard");
  } catch {
    feedback.message.error("Copy failed — select the value and copy manually");
  }
}

// zoneFileLine renders one hint as a bulk-import-friendly zone-file line. The
// value already carries record-data form (MX/SRV priorities inlined), so it
// drops in after "<name> IN <type> ". TXT rdata MUST be quoted for zone-file
// syntax — an unquoted "v=DMARC1; p=…" is truncated at the first ";" (a comment
// marker), silently corrupting DMARC/DKIM/TLS-RPT/SPF. Per-value copy stays bare
// (provider UIs want the raw string); only the zone-file form quotes.
function zoneFileLine(h: DomainEmailDNSHint): string {
  const rdata = h.type === "TXT" && !h.value.startsWith('"') ? `"${h.value}"` : h.value;
  return `${h.name}\tIN\t${h.type}\t${rdata}`;
}

export function MailDNSRecordsModal({ domainId, domainName, open, onClose }: Props) {
  // Only fetch while open — passing undefined disables the query.
  const { data, isLoading } = useDomainEmail(open ? domainId : undefined);
  const records = (data?.records ?? []).filter((r) => r.value !== "");

  const copyAll = () => {
    if (records.length === 0) return;
    void copyText(records.map(zoneFileLine).join("\n"));
  };

  const columns: TableColumnsType<DomainEmailDNSHint> = [
    { title: "Name", dataIndex: "name", width: 240, render: (v: string) => <Typography.Text code>{v}</Typography.Text> },
    { title: "Type", dataIndex: "type", width: 64 },
    {
      title: "Value",
      dataIndex: "value",
      render: (value: string) => (
        <Space size="small">
          <Typography.Text code style={{ wordBreak: "break-all", fontSize: 12 }}>
            {value}
          </Typography.Text>
          <Button
            size="small"
            icon={<CopyOutlined />}
            onClick={() => void copyText(value)}
            aria-label={`Copy ${value}`}
          />
        </Space>
      ),
    },
    {
      title: "Purpose",
      dataIndex: "purpose",
      width: 300,
      render: (v: string) => <Typography.Text type="secondary">{v}</Typography.Text>,
    },
  ];

  return (
    <Modal
      open={open}
      onCancel={onClose}
      title={domainName ? `DNS records for ${domainName}` : "DNS records"}
      width={860}
      footer={[
        <Button key="copy-all" icon={<CopyOutlined />} onClick={copyAll} disabled={records.length === 0}>
          Copy all (zone file)
        </Button>,
        <Button key="close" type="primary" onClick={onClose}>
          Close
        </Button>,
      ]}
      destroyOnClose
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="This domain's DNS is hosted elsewhere"
        description={
          <>
            The panel does not manage this domain's DNS, so it can't publish these
            records for you. Add them at your DNS provider so mail delivers and
            authenticates (SPF/DKIM/DMARC) and mail clients auto-configure. Until
            the <Typography.Text code>MX</Typography.Text> and{" "}
            <Typography.Text code>mail</Typography.Text> host records exist, incoming
            mail will not arrive.
          </>
        }
      />
      <Table<DomainEmailDNSHint>
        size="small"
        loading={isLoading}
        pagination={false}
        dataSource={records}
        scroll={{ x: "max-content" }}
        rowKey={(r) => `${r.type}:${r.name}`}
        columns={columns}
      />
    </Modal>
  );
}
