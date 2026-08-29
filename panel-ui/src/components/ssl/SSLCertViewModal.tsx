// SSLCertViewModal — GH #1355: view the actual issued certificate for a domain.
// Fetches the parsed X.509 leaf (public data only) + the PEM from
// GET /domains/:id/ssl/certificate and shows it read-only, PEM copyable.
import { Alert, Button, Descriptions, Modal, Spin, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../apiClient";

type CertDetails = {
  subject: string;
  issuer: string;
  sans: string[];
  not_before: string;
  not_after: string;
  serial_number: string;
  sha256_fingerprint: string;
  signature_algorithm: string;
  public_key_algorithm: string;
  is_ca: boolean;
  pem: string;
};

export function SSLCertViewModal({
  domainId,
  domainName,
  onClose,
}: {
  domainId: string | null;
  domainName?: string;
  onClose: () => void;
}) {
  const open = !!domainId;
  const { data, isLoading, error } = useQuery<CertDetails>({
    queryKey: ["ssl-cert-view", domainId],
    queryFn: async () =>
      (await apiClient.get<CertDetails>(`/domains/${domainId}/ssl/certificate`))
        .data,
    enabled: open,
  });

  const detail = (error as { response?: { data?: { detail?: string } } })
    ?.response?.data?.detail;

  return (
    <Modal
      open={open}
      title={`Certificate${domainName ? ` — ${domainName}` : ""}`}
      onCancel={onClose}
      footer={[
        <Button key="close" onClick={onClose}>
          Close
        </Button>,
      ]}
      width={760}
    >
      {isLoading ? (
        <div style={{ textAlign: "center", padding: 24 }}>
          <Spin />
        </div>
      ) : error ? (
        <Alert
          type="error"
          showIcon
          message="Couldn't load the certificate"
          description={detail ?? "The certificate may not be issued yet."}
        />
      ) : data ? (
        <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label="Subject">{data.subject || "—"}</Descriptions.Item>
            <Descriptions.Item label="Issuer">{data.issuer || "—"}</Descriptions.Item>
            <Descriptions.Item label="Subject Alternative Names">
              {data.sans?.length ? data.sans.join(", ") : "—"}
            </Descriptions.Item>
            <Descriptions.Item label="Valid from">
              {new Date(data.not_before).toLocaleString()}
            </Descriptions.Item>
            <Descriptions.Item label="Valid to">
              {new Date(data.not_after).toLocaleString()}
            </Descriptions.Item>
            <Descriptions.Item label="Serial">
              <Typography.Text style={{ fontFamily: "monospace", fontSize: 12, wordBreak: "break-all" }}>
                {data.serial_number}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="SHA-256 fingerprint">
              <Typography.Text style={{ fontFamily: "monospace", fontSize: 12, wordBreak: "break-all" }}>
                {data.sha256_fingerprint}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="Signature">{data.signature_algorithm}</Descriptions.Item>
            <Descriptions.Item label="Public key">{data.public_key_algorithm}</Descriptions.Item>
          </Descriptions>
          <div>
            <Typography.Text strong copyable={{ text: data.pem, tooltips: ["Copy PEM", "Copied"] }}>
              PEM
            </Typography.Text>
            <pre
              style={{
                maxHeight: 240,
                overflow: "auto",
                background: "var(--ant-color-fill-quaternary, rgba(0,0,0,0.04))",
                padding: 8,
                marginTop: 4,
                fontSize: 11,
                borderRadius: 4,
                whiteSpace: "pre",
              }}
            >
              {data.pem}
            </pre>
          </div>
        </div>
      ) : null}
    </Modal>
  );
}
