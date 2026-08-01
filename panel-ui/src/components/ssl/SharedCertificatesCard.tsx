import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  Upload,
  message,
} from "antd";
import type { UploadFile } from "antd";
import { DeleteOutlined, PlusOutlined, SafetyCertificateOutlined, UploadOutlined } from "@icons";

import { apiClient } from "../../apiClient";

// SharedCertificatesCard — admin surface for JAB-170 shared wildcard/multi-SAN
// certificates: upload one cert, serve it from many domains. Sibling of the
// per-domain SSLManagerTable; lives on the SSL Manager page beneath the
// per-domain certificate list. The private key is write-only — it is sent on
// upload and never returned by the list endpoint.

interface SharedCert {
  id: string;
  name: string;
  sans: string[];
  expires_at?: string;
  attached_domains: number;
  source?: "uploaded" | "acme";
  pending?: boolean;
  staging?: boolean;
  last_error?: string;
}

interface DomainOption {
  id: string;
  name: string;
}

const daysUntil = (iso?: string): number | null => {
  if (!iso) return null;
  const diff = new Date(iso).getTime() - Date.now();
  return Math.round(diff / 86_400_000);
};

const formatExpiry = (iso?: string): JSX.Element => {
  if (!iso) return <span>—</span>;
  const date = new Date(iso);
  const dateStr = date.toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
  const days = daysUntil(iso);
  if (days === null) return <span>{dateStr}</span>;
  const label = days < 0 ? "expired" : days === 0 ? "today" : `in ${days} days`;
  return (
    <Typography.Text type={days < 14 ? "danger" : undefined}>
      {dateStr} ({label})
    </Typography.Text>
  );
};

// readFileText resolves a picked file's text so the operator can select a .pem
// off disk instead of pasting. Returning false from beforeUpload keeps antd
// from actually POSTing the file anywhere.
const readFileText = (file: File): Promise<string> =>
  new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });

export const SharedCertificatesCard = () => {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const [uploadOpen, setUploadOpen] = useState(false);
  const [name, setName] = useState("");
  const [certPem, setCertPem] = useState("");
  const [keyPem, setKeyPem] = useState("");
  const [acmeOpen, setAcmeOpen] = useState(false);
  const [acmeDomainId, setAcmeDomainId] = useState<string>();

  const { data, isLoading, error } = useQuery({
    queryKey: ["shared-certificates"],
    queryFn: async () => {
      const res = await apiClient.get("/admin/certificates/shared");
      return (res.data?.data ?? []) as SharedCert[];
    },
    // Poll while an ACME row is still issuing (the reconciler drives the
    // certbot run); stop once nothing is pending.
    refetchInterval: (q) => {
      const rows = q.state.data as SharedCert[] | undefined;
      return rows?.some((r) => r.pending) ? 10_000 : false;
    },
  });

  const domainsQuery = useQuery({
    queryKey: ["shared-cert-acme-domains"],
    enabled: acmeOpen,
    queryFn: async () => {
      const res = await apiClient.get("/domains?page_size=500");
      return (res.data?.data ?? []) as DomainOption[];
    },
  });

  const acmeMutation = useMutation({
    mutationFn: async () => {
      await apiClient.post("/admin/certificates/shared/acme", {
        domain_id: acmeDomainId,
      });
    },
    onSuccess: () => {
      message.success("Wildcard certificate requested — issuance runs within a minute");
      setAcmeOpen(false);
      setAcmeDomainId(undefined);
      queryClient.invalidateQueries({ queryKey: ["shared-certificates"] });
    },
    onError: (err: unknown) => {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      message.error(resp?.detail ?? resp?.error ?? "Failed to request certificate");
    },
  });

  const resetForm = () => {
    setName("");
    setCertPem("");
    setKeyPem("");
  };

  const uploadMutation = useMutation({
    mutationFn: async () => {
      await apiClient.post("/admin/certificates/shared", {
        name: name.trim(),
        cert_pem: certPem,
        key_pem: keyPem,
      });
    },
    onSuccess: () => {
      message.success("Shared certificate uploaded");
      setUploadOpen(false);
      resetForm();
      queryClient.invalidateQueries({ queryKey: ["shared-certificates"] });
    },
    onError: (err: unknown) => {
      const resp = (err as { response?: { data?: { error?: string; detail?: string } } })?.response?.data;
      message.error(resp?.detail ?? resp?.error ?? "Failed to upload certificate");
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      await apiClient.delete(`/admin/certificates/shared/${id}`);
    },
    onSuccess: () => {
      message.success("Shared certificate deleted");
      queryClient.invalidateQueries({ queryKey: ["shared-certificates"] });
    },
    onError: (err: unknown) => {
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 409) {
        message.info("Detach every domain from this certificate first");
      } else {
        message.error("Failed to delete certificate");
      }
    },
  });

  const submitUpload = () => {
    if (!name.trim()) {
      message.error("Give the certificate a name");
      return;
    }
    if (!certPem.trim() || !keyPem.trim()) {
      message.error("Paste both the certificate and the private key");
      return;
    }
    uploadMutation.mutate();
  };

  const columns = [
    {
      title: "Name",
      dataIndex: "name",
      key: "name",
      sorter: (a: SharedCert, b: SharedCert) => a.name.localeCompare(b.name),
      defaultSortOrder: "ascend" as const,
      render: (name: string) => <span style={{ fontWeight: 500 }}>{name}</span>,
    },
    {
      title: "SANs",
      dataIndex: "sans",
      key: "sans",
      render: (sans: string[]) => {
        const list = sans ?? [];
        if (list.length === 0) return <span>—</span>;
        return (
          <Space size={4} wrap>
            {list.slice(0, 3).map((s) => (
              <Tag key={s} style={{ fontFamily: "monospace" }}>
                {s}
              </Tag>
            ))}
            {list.length > 3 && (
              <Tooltip title={list.join(", ")}>
                <Tag>+{list.length - 3} more</Tag>
              </Tooltip>
            )}
          </Space>
        );
      },
    },
    {
      title: "Status",
      key: "status",
      render: (_: unknown, r: SharedCert) => {
        if (r.source !== "acme") return <Tag>Uploaded</Tag>;
        if (r.last_error) {
          return (
            <Tooltip title={r.last_error}>
              <Tag color="red">Issue failed</Tag>
            </Tooltip>
          );
        }
        if (r.pending) return <Tag color="processing">Issuing…</Tag>;
        return <Tag color="green">{r.staging ? "Let's Encrypt (staging)" : "Let's Encrypt"}</Tag>;
      },
    },
    {
      title: "Expires",
      dataIndex: "expires_at",
      key: "expires_at",
      sorter: (a: SharedCert, b: SharedCert) =>
        (a.expires_at ? +new Date(a.expires_at) : 0) - (b.expires_at ? +new Date(b.expires_at) : 0),
      render: (iso?: string) => formatExpiry(iso),
    },
    {
      title: "Attached domains",
      dataIndex: "attached_domains",
      key: "attached_domains",
      sorter: (a: SharedCert, b: SharedCert) => a.attached_domains - b.attached_domains,
      render: (n: number) =>
        n > 0 ? <Tag color="blue">{n}</Tag> : <Typography.Text type="secondary">0</Typography.Text>,
    },
    {
      title: "Actions",
      key: "actions",
      render: (_: unknown, record: SharedCert) => (
        <Popconfirm
          title="Delete shared certificate?"
          description={
            record.attached_domains > 0
              ? `${record.attached_domains} domain(s) still use it — detach them first.`
              : "This removes the certificate from disk."
          }
          okText="Delete"
          okButtonProps={{ danger: true }}
          onConfirm={() => deleteMutation.mutate(record.id)}
        >
          <Button danger size="small" icon={<DeleteOutlined />} loading={deleteMutation.isPending}>
            Delete
          </Button>
        </Popconfirm>
      ),
    },
  ];

  return (
    <Card
      title={t("sharedcertificates.title")}
      extra={
        <Space>
          <Button icon={<SafetyCertificateOutlined />} onClick={() => setAcmeOpen(true)}>
            Request wildcard (Let&apos;s Encrypt)
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setUploadOpen(true)}>
            Upload shared certificate
          </Button>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Upload a wildcard or multi-SAN certificate once, then attach it to any
        covering domain from that domain&apos;s SSL tab. The reconciler renders
        the shared pair into each attached domain&apos;s vhost.
      </Typography.Paragraph>

      {error ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="Failed to load shared certificates" />
      ) : !data || data.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description="No shared certificates yet"
        />
      ) : (
        <Table
          dataSource={data}
          columns={columns}
          rowKey="id"
          loading={isLoading}
          pagination={{ pageSize: 10, showSizeChanger: true }}
          scroll={{ x: "max-content" }}
        />
      )}

      <Modal
        open={acmeOpen}
        title="Request wildcard certificate"
        okText="Request"
        okButtonProps={{ disabled: !acmeDomainId }}
        confirmLoading={acmeMutation.isPending}
        onOk={() => acmeMutation.mutate()}
        onCancel={() => {
          setAcmeOpen(false);
          setAcmeDomainId(undefined);
        }}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Alert
            type="info"
            showIcon
            message="Issues a Let's Encrypt certificate covering the domain and *.domain via DNS-01. The domain's DNS zone must be hosted on this server — the challenge record is placed in the local PowerDNS."
          />
          <Select
            style={{ width: "100%" }}
            placeholder="Select a domain"
            showSearch
            optionFilterProp="label"
            loading={domainsQuery.isLoading}
            value={acmeDomainId}
            onChange={setAcmeDomainId}
            options={(domainsQuery.data ?? []).map((d) => ({ value: d.id, label: d.name }))}
          />
        </Space>
      </Modal>

      <Modal
        open={uploadOpen}
        title="Upload shared certificate"
        okText="Upload"
        confirmLoading={uploadMutation.isPending}
        onOk={submitUpload}
        onCancel={() => {
          setUploadOpen(false);
          resetForm();
        }}
        width={680}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Alert
            type="info"
            showIcon
            message="Paste the full-chain certificate (leaf + intermediates) and its private key, or pick the .pem files. The key is stored 0600, root-owned, and never shown again."
          />
          <div>
            <div style={{ marginBottom: 4 }}>Name</div>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. *.example.com wildcard"
              maxLength={128}
            />
          </div>
          <div>
            <Space style={{ marginBottom: 4 }} align="center">
              <span>Certificate (PEM)</span>
              <Upload
                accept=".pem,.crt,.cer,.txt"
                maxCount={1}
                showUploadList={false}
                beforeUpload={(file: UploadFile & File) => {
                  readFileText(file)
                    .then((text) => setCertPem(text))
                    .catch(() => message.error("Could not read the certificate file"));
                  return false;
                }}
              >
                <Button size="small" icon={<UploadOutlined />}>
                  Pick file
                </Button>
              </Upload>
            </Space>
            <Input.TextArea
              rows={6}
              value={certPem}
              onChange={(e) => setCertPem(e.target.value)}
              placeholder="-----BEGIN CERTIFICATE-----"
              style={{ fontFamily: "monospace" }}
            />
          </div>
          <div>
            <Space style={{ marginBottom: 4 }} align="center">
              <span>Private key (PEM)</span>
              <Upload
                accept=".pem,.key,.txt"
                maxCount={1}
                showUploadList={false}
                beforeUpload={(file: UploadFile & File) => {
                  readFileText(file)
                    .then((text) => setKeyPem(text))
                    .catch(() => message.error("Could not read the key file"));
                  return false;
                }}
              >
                <Button size="small" icon={<UploadOutlined />}>
                  Pick file
                </Button>
              </Upload>
            </Space>
            <Input.TextArea
              rows={6}
              value={keyPem}
              onChange={(e) => setKeyPem(e.target.value)}
              placeholder="-----BEGIN PRIVATE KEY-----"
              style={{ fontFamily: "monospace" }}
            />
          </div>
        </Space>
      </Modal>
    </Card>
  );
};
