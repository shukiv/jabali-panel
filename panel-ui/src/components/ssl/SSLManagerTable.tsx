import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Input,
  Table,
  Tag,
  Button,
  Popconfirm,
  message,
  Empty,
  Space,
  Tooltip,
  Typography,
  Modal,
  Descriptions,
} from "antd";
import {
  ReloadOutlined,
  DeleteOutlined,
  SyncOutlined,
  WarningOutlined,
  RedoOutlined,
  ExclamationCircleOutlined,
} from "@icons";
import { apiClient } from "../../apiClient";
import { columnSearchProps } from "../columnSearch";

interface SSLCertificate {
  id: string;
  domain_id: string;
  domain_name: string;
  user_id: string;
  user_username: string;
  status: "pending" | "issuing" | "issued" | "renewing" | "revoked" | "failed" | "self_signed" | "pending_acme_retry";
  issued_at: string | null;
  expires_at: string | null;
  renewal_count: number;
  last_renewed_at: string | null;
  last_error: string | null;
  last_attempt_at: string | null;
  staging: boolean;
  next_retry_at: string | null;
  retry_count: number;
  service: string;
  sans?: string[];
}

interface SSLManagerTableProps {
  endpoint: string;
  showOwner: boolean;
}

const STATUS_COLORS: Record<string, string> = {
  issued: "green",
  issuing: "processing",
  renewing: "processing",
  pending: "default",
  revoked: "default",
  failed: "red",
  self_signed: "orange",
  pending_acme_retry: "gold",
};

const STATUS_ICONS: Record<string, JSX.Element | null> = {
  issuing: <SyncOutlined spin />,
  renewing: <SyncOutlined spin />,
  pending: null,
  issued: null,
  revoked: null,
  failed: null,
  self_signed: <WarningOutlined />,
  pending_acme_retry: <SyncOutlined spin />,
};

const formatDate = (dateStr: string | null): string => {
  if (!dateStr) return "—";
  try {
    const date = new Date(dateStr);
    return date.toLocaleDateString("en-US", {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return "—";
  }
};

const daysUntilExpiry = (expiresAt: string | null): number | null => {
  if (!expiresAt) return null;
  try {
    const expiryDate = new Date(expiresAt);
    const now = new Date();
    const diffMs = expiryDate.getTime() - now.getTime();
    return Math.ceil(diffMs / (1000 * 60 * 60 * 24));
  } catch {
    return null;
  }
};

const formatExpiry = (expiresAt: string | null): JSX.Element => {
  const dateStr = formatDate(expiresAt);
  if (dateStr === "—") return <span>{dateStr}</span>;

  const days = daysUntilExpiry(expiresAt);
  if (days === null) return <span>{dateStr}</span>;

  const isExpiringSoon = days < 14;
  const label =
    days < 0
      ? "expired"
      : days === 0
        ? "today"
        : days === 1
          ? "tomorrow"
          : `in ${days} days`;

  return (
    <Typography.Text type={isExpiringSoon ? "danger" : undefined}>
      {dateStr} ({label})
    </Typography.Text>
  );
};

export const SSLManagerTable = ({
  endpoint,
  showOwner,
}: SSLManagerTableProps) => {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  // Client-side search over the fetched rows — SSL list is small
  // enough that we don't need server-side ?q filtering.
  const [search, setSearch] = useState("");

  // When non-null, opens a Modal showing the full last_error text + retry
  // metadata for that row. The status column renders a small alert button
  // beside the tag whenever last_error is non-empty so operators can
  // diagnose pending_acme_retry / failed states without shelling into the
  // VPS to grep journalctl.
  const [errorRow, setErrorRow] = useState<SSLCertificate | null>(null);

  // Fetch SSL certificates
  const { data, isLoading, error } = useQuery({
    queryKey: ["ssl-manager", endpoint],
    queryFn: async () => {
      const response = await apiClient.get(endpoint);
      return response.data.items as SSLCertificate[];
    },
  });

  const filteredData = useMemo(() => {
    if (!data || !search) return data;
    const needle = search.toLowerCase();
    return data.filter(
      (row) =>
        row.domain_name.toLowerCase().includes(needle) ||
        (row.user_username ?? "").toLowerCase().includes(needle),
    );
  }, [data, search]);

  // Renew certificate mutation
  const renewMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/ssl/renew`);
    },
    onSuccess: () => {
      message.success("Certificate renewal initiated");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      message.error("Failed to renew certificate");
    },
  });

  // Revoke certificate mutation
  const revokeMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.delete(`/domains/${domainId}/ssl`);
    },
    onSuccess: () => {
      message.success("Certificate revoked");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      message.error("Failed to revoke certificate");
    },
  });

  // Retry certificate mutation
  const retryMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/ssl/retry`);
    },
    onSuccess: () => {
      message.success("Retry queued");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: (error: unknown) => {
      const status = (error as { response?: { status?: number; data?: { error?: string } } })?.response;
      if (status?.status === 409) {
        message.info("Already retryable — will attempt on next tick");
      } else {
        message.error("Failed to queue retry");
      }
    },
  });

  // Reissue mutation for per-domain mail certs (mail-cert:<domainID> rows).
  // Hits the mail-certificate endpoint, NOT the website-cert retry/renew.
  const reissueMailMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/mail-certificate/reissue`);
    },
    onSuccess: () => {
      message.success("Mail certificate reissue queued");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      message.error("Failed to reissue mail certificate");
    },
  });

  if (error) {
    return (
      <Empty
        image={Empty.PRESENTED_IMAGE_SIMPLE}
        description={t("sslmanagertable.failed_to_load_ssl_certificates")}
      />
    );
  }

  const columns = [
    {
      title: "Domain",
      dataIndex: "domain_name",
      sorter: (a: SSLCertificate, b: SSLCertificate) => a.domain_name.localeCompare(b.domain_name),
      defaultSortOrder: "ascend" as const,
      key: "domain_name",
      ...columnSearchProps<SSLCertificate>({
        placeholder: "Search by domain or owner",
        currentQ: search,
        onSearch: (v) => setSearch(v),
      }),
      render: (text: string, record: SSLCertificate) => {
        const isPanelCert = record.id.startsWith("panel-cert:");
        // Aliases = every SAN other than the primary record name, shown
        // as a muted second line beneath it (GH #195).
        const aliases = (record.sans ?? []).filter((s) => s !== text);
        return (
          <Space direction="vertical" size={0}>
            <Space size={4}>
              <span style={{ fontFamily: "monospace" }}>{text}</span>
              {isPanelCert && (
                <Tooltip title={t("sslmanagertable.panel_cert_managed_via_server_settings_panel")}>
                  <Tag color="purple">panel</Tag>
                </Tooltip>
              )}
            </Space>
            {aliases.length > 0 && (
              // Codeberg #7: cap the alias line to 2 SANs + a "+N more" tooltip
              // so long lists (www/mail/autoconfig/autodiscover…) don't clip
              // horizontally on mobile. Full list on hover.
              <Typography.Text
                type="secondary"
                italic
                style={{ fontFamily: "monospace", fontSize: 12 }}
              >
                {aliases.slice(0, 2).join(", ")}
                {aliases.length > 2 && (
                  <Tooltip title={aliases.join(", ")}>
                    <span style={{ cursor: "pointer", fontStyle: "normal" }}>
                      {` +${aliases.length - 2} more`}
                    </span>
                  </Tooltip>
                )}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    ...(showOwner
      ? [
          {
            title: "Owner",
            dataIndex: "user_username",
            sorter: (a: SSLCertificate, b: SSLCertificate) => (a.user_username ?? "").localeCompare(b.user_username ?? ""),
            key: "user_username",
            ...columnSearchProps<SSLCertificate>({
              placeholder: "Search by domain or owner",
              currentQ: search,
              onSearch: (v) => setSearch(v),
            }),
          },
        ]
      : []),
    {
      title: "Service",
      dataIndex: "service",
      sorter: (a: SSLCertificate, b: SSLCertificate) => a.service.localeCompare(b.service),
      key: "service",
      render: (service: string) =>
        service ? <Tag color="geekblue">{service}</Tag> : <span>—</span>,
    },
    {
      title: "Status",
      dataIndex: "status",
      sorter: (a: SSLCertificate, b: SSLCertificate) => a.status.localeCompare(b.status),
      key: "status",
      render: (status: string, record: SSLCertificate) => {
        let tooltip = "";
        if (status === "self_signed") {
          tooltip = "Self-signed certificate (Self SSL mode) — browsers show a trust warning; not issued by a CA.";
        } else if (status === "pending_acme_retry") {
          tooltip = `ACME failed — retrying at ${formatDate(record.next_retry_at)}`;
        }
        const hasError = !!record.last_error;
        return (
          <Space size={4}>
            <Tooltip title={tooltip}>
              <Tag
                color={STATUS_COLORS[status] || "default"}
                icon={STATUS_ICONS[status]}
              >
                {status === "pending_acme_retry" ? "Pending" : status.charAt(0).toUpperCase() + status.slice(1).replace(/_/g, " ")}
              </Tag>
            </Tooltip>
            {hasError && (
              <Tooltip title={t("sslmanagertable.show_last_error")}>
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<ExclamationCircleOutlined />}
                  onClick={() => setErrorRow(record)}
                  aria-label={`Show last error for ${record.domain_name}`}
                />
              </Tooltip>
            )}
          </Space>
        );
      },
    },
    {
      title: "Last check",
      dataIndex: "last_attempt_at",
      sorter: (a: SSLCertificate, b: SSLCertificate) => (a.last_attempt_at ? +new Date(a.last_attempt_at) : 0) - (b.last_attempt_at ? +new Date(b.last_attempt_at) : 0),
      key: "last_attempt_at",
      render: (dateStr: string | null) => {
        if (!dateStr) return "—";
        try {
          const date = new Date(dateStr);
          return date.toLocaleString();
        } catch {
          return "—";
        }
      },
    },
    {
      title: "Issued",
      dataIndex: "issued_at",
      sorter: (a: SSLCertificate, b: SSLCertificate) => (a.issued_at ? +new Date(a.issued_at) : 0) - (b.issued_at ? +new Date(b.issued_at) : 0),
      key: "issued_at",
      render: (dateStr: string | null) => formatDate(dateStr),
    },
    {
      title: "Expires",
      dataIndex: "expires_at",
      sorter: (a: SSLCertificate, b: SSLCertificate) => (a.expires_at ? +new Date(a.expires_at) : 0) - (b.expires_at ? +new Date(b.expires_at) : 0),
      key: "expires_at",
      render: (dateStr: string | null, record: SSLCertificate) => {
        if (record.status === "self_signed") {
          return (
            <Typography.Text type="secondary">
              {formatDate(dateStr)} <em>(self-signed)</em>
            </Typography.Text>
          );
        }
        return formatExpiry(dateStr);
      },
    },
    {
      title: "Staging",
      dataIndex: "staging",
      sorter: (a: SSLCertificate, b: SSLCertificate) => Number(a.staging) - Number(b.staging),
      key: "staging",
      render: (isStaging: boolean) =>
        isStaging ? <Tag color="blue">staging</Tag> : null,
    },
    {
      title: "Actions",
      key: "actions",
      render: (_: unknown, record: SSLCertificate) => {
        // Panel-hostname + panel-mail certs (synthetic rows from
        // panel_certificate) renew via their own scheduler — the
        // per-domain renew/retry/revoke endpoints don't apply. Show a
        // pointer instead of broken buttons.
        if (record.id.startsWith("panel-cert:")) {
          return (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Manage in Server Settings &rarr; Panel SSL
            </Typography.Text>
          );
        }
        // Per-domain mail cert (mail_certificate table) — only a Reissue
        // action applies (the website-cert retry/renew/revoke endpoints don't
        // exist for it). GH#132.
        if (record.id.startsWith("mail-cert:")) {
          return (
            <Tooltip title={t("sslmanagertable.clears_backoff_and_reissues_the_let_s_encryp")}>
              <Button
                icon={<RedoOutlined />}
                loading={reissueMailMutation.isPending}
                onClick={() => reissueMailMutation.mutate(record.domain_id)}
              >
                Reissue
              </Button>
            </Tooltip>
          );
        }
        const isRetryable = record.status === "failed" ||
          (record.status === "pending_acme_retry" && record.next_retry_at && new Date(record.next_retry_at) < new Date());
        return (
          <Space>
            {isRetryable && (
              <Tooltip title={t("sslmanagertable.force_acme_retry_now")}>
                <Button
                  icon={<RedoOutlined />}
                  loading={retryMutation.isPending}
                  onClick={() => retryMutation.mutate(record.domain_id)}
                />
              </Tooltip>
            )}
            {record.status === "issued" && (
              <Tooltip title={t("sslmanagertable.renew_certificate")}>
                <Button
                  type="primary"
                  icon={<ReloadOutlined />}
                  loading={renewMutation.isPending}
                  onClick={() => renewMutation.mutate(record.domain_id)}
                />
              </Tooltip>
            )}
            {record.status === "issued" && (
              <Popconfirm
                title={t("sslmanagertable.revoke_certificate")}
                description={t("sslmanagertable.are_you_sure_you_want_to_revoke_this_certifi")}
                onConfirm={() => revokeMutation.mutate(record.domain_id)}
                okText={t("sslmanagertable.yes")}
                cancelText="No"
              >
                <Button
                  danger
                  icon={<DeleteOutlined />}
                  loading={revokeMutation.isPending}
                />
              </Popconfirm>
            )}
          </Space>
        );
      },
    },
  ];

  return (
    <>
      {!data || data.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={t("sslmanagertable.no_ssl_certificates_yet")}
        />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Input.Search
            placeholder={showOwner ? "Search by domain or owner" : "Search by domain"}
            allowClear
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onSearch={(value) => setSearch(value.trim())}
            style={{ maxWidth: 360 }}
          />
          <Table
            dataSource={filteredData}
            columns={columns}
            rowKey="id"
            loading={isLoading}
            pagination={{ pageSize: 25, showSizeChanger: true }}
            scroll={{ x: "max-content" }}
          />
        </Space>
      )}
      <Modal
        open={!!errorRow}
        title={errorRow ? `Last error — ${errorRow.domain_name}` : "Last error"}
        onCancel={() => setErrorRow(null)}
        footer={[
          <Button key="close" onClick={() => setErrorRow(null)}>
            Close
          </Button>,
        ]}
        width={720}
      >
        {errorRow && (
          <Space direction="vertical" size="middle" style={{ width: "100%" }}>
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label={t("sslmanagertable.status")}>
                {errorRow.status.charAt(0).toUpperCase() +
                  errorRow.status.slice(1).replace(/_/g, " ")}
              </Descriptions.Item>
              <Descriptions.Item label={t("sslmanagertable.last_attempt")}>
                {errorRow.last_attempt_at
                  ? new Date(errorRow.last_attempt_at).toLocaleString()
                  : "—"}
              </Descriptions.Item>
              <Descriptions.Item label={t("sslmanagertable.retry_count")}>
                {errorRow.retry_count}
              </Descriptions.Item>
              <Descriptions.Item label={t("sslmanagertable.next_retry")}>
                {errorRow.next_retry_at
                  ? new Date(errorRow.next_retry_at).toLocaleString()
                  : "—"}
              </Descriptions.Item>
            </Descriptions>
            <div>
              <Typography.Text strong>Error</Typography.Text>
              <pre
                style={{
                  marginTop: 8,
                  marginBottom: 0,
                  maxHeight: 360,
                  overflow: "auto",
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                  fontFamily: "monospace",
                  fontSize: 12,
                  background: "rgba(0,0,0,0.04)",
                  padding: 12,
                  borderRadius: 4,
                }}
              >
                {errorRow.last_error || "(no error recorded)"}
              </pre>
            </div>
          </Space>
        )}
      </Modal>
    </>
  );
};
