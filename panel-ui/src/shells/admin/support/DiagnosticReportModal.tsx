// DiagnosticReportModal — runs the diagnostic flow.
//
// Step 1 (auto on open): POST /admin/support/diagnostic uploads the
// redacted + encrypted bundle to enclosed.jabali-panel.com. Modal shows
// the link + password with copy buttons.
//
// Step 2 (operator click): "Send via email" opens the user's mail
// client (mailto:) with a pre-filled subject + body containing the link
// + password — the team gets it via inbox.
import { useEffect, useState } from "react";
import {
  Alert,
  Button,
  Input,
  Modal,
  Space,
  Spin,
  Tag,
  Typography,
  message,
} from "antd";

import { CopyOutlined, ExportOutlined, MailOutlined } from "@icons";

import { DIAGNOSTIC_EMAIL_RECIPIENT } from "../../../config/support-links";
import { useDiagnosticReport } from "../../../hooks/useSupport";

interface Props {
  open: boolean;
  onClose: () => void;
}

export function DiagnosticReportModal({ open, onClose }: Props) {
  const report = useDiagnosticReport();
  const [note, setNote] = useState("");

  useEffect(() => {
    if (open && !report.isPending && !report.data && !report.error) {
      report.mutate();
    }
    if (!open) {
      report.reset();
      setNote("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const copy = async (label: string, value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      message.success(`${label} copied`);
    } catch (e: unknown) {
      message.error(e instanceof Error ? e.message : "copy failed");
    }
  };

  const buildMailto = (): string => {
    if (!report.data) return "";
    const host = window.location.hostname;
    const subject = `Diagnostic report from ${host}`;
    const lines = [
      `Host: ${host}`,
      "",
      `Link: ${report.data.url}`,
      `Password: ${report.data.password}`,
      "",
      `Generated: ${report.data.generated_at}`,
      `Files: ${report.data.file_count}`,
      `Redactions: ${report.data.redaction_count}`,
      `Size: ${report.data.byte_count.toLocaleString()} bytes`,
    ];
    if (note.trim()) {
      lines.push("", "Operator note:", note.trim());
    }
    const body = lines.join("\n");
    return `mailto:${DIAGNOSTIC_EMAIL_RECIPIENT}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
  };

  return (
    <Modal
      open={open}
      onCancel={onClose}
      title="Diagnostic Report"
      width={780}
      footer={[
        <Button key="close" onClick={onClose}>
          Close
        </Button>,
      ]}
    >
      {report.isPending ? (
        <div style={{ padding: 32, textAlign: "center" }}>
          <Spin tip="Collecting host state, redacting secrets, uploading…" />
        </div>
      ) : null}

      {report.error ? (
        <Alert
          type="error"
          showIcon
          message="Failed to generate report"
          description={(report.error as Error).message}
        />
      ) : null}

      {report.data ? (
        <Space direction="vertical" size={16} style={{ width: "100%" }}>
          <Alert
            type="success"
            showIcon
            message="Encrypted bundle uploaded"
            description={
              <Space size={4} wrap>
                <Tag>{report.data.file_count} files</Tag>
                <Tag color="blue">{report.data.redaction_count} redactions</Tag>
                <Tag>{report.data.byte_count.toLocaleString()} bytes</Tag>
                <Tag color="default">7-day TTL</Tag>
              </Space>
            }
          />

          {report.data.claim_code ? (
            <Alert
              type="success"
              showIcon
              message="Support claim code"
              description={
                <Space direction="vertical" size={8} style={{ width: "100%" }}>
                  <Typography.Paragraph style={{ margin: 0 }}>
                    Give this code to Jabali support — it&apos;s safe to share
                    anywhere, even a public issue. The link + password below are
                    only a fallback.
                  </Typography.Paragraph>
                  <Space.Compact style={{ display: "flex" }}>
                    <Input
                      value={report.data.claim_code}
                      readOnly
                      style={{ fontFamily: "monospace", fontWeight: 600 }}
                    />
                    <Button
                      icon={<CopyOutlined />}
                      onClick={() => copy("Claim code", report.data!.claim_code!)}
                    >
                      Copy
                    </Button>
                  </Space.Compact>
                </Space>
              }
            />
          ) : null}

          <div>
            <Typography.Text strong>Link</Typography.Text>
            <Space.Compact style={{ display: "flex", marginTop: 4 }}>
              <Input value={report.data.url} readOnly />
              <Button
                icon={<CopyOutlined />}
                onClick={() => copy("Link", report.data!.url)}
              >
                Copy
              </Button>
              <Button
                icon={<ExportOutlined />}
                href={report.data.url}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open
              </Button>
            </Space.Compact>
          </div>

          <div>
            <Typography.Text strong>Password</Typography.Text>
            <Space.Compact style={{ display: "flex", marginTop: 4 }}>
              <Input value={report.data.password} readOnly />
              <Button
                icon={<CopyOutlined />}
                onClick={() => copy("Password", report.data!.password)}
              >
                Copy
              </Button>
            </Space.Compact>
          </div>

          <Alert
            type="info"
            showIcon
            message={`Send the link + password to ${DIAGNOSTIC_EMAIL_RECIPIENT}`}
            description={
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                <Typography.Paragraph style={{ margin: 0 }}>
                  Click <b>Send via email</b> below — your mail client opens
                  with the link, password, and host details pre-filled. Add
                  context if you like, then send.
                </Typography.Paragraph>
                <Input.TextArea
                  rows={2}
                  placeholder="Optional context (what's broken, what you've tried)"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                />
                <Button
                  type="primary"
                  icon={<MailOutlined />}
                  href={buildMailto()}
                >
                  Send via email
                </Button>
              </Space>
            }
          />
        </Space>
      ) : null}
    </Modal>
  );
}
