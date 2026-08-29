import { useTranslation } from "react-i18next";
import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Dropdown, Input, Table, Tag, Button, Empty, Space, Tooltip, Typography, Modal, Descriptions } from "antd";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  ReloadOutlined,
  DeleteOutlined,
  SyncOutlined,
  WarningOutlined,
  RedoOutlined,
  ExclamationCircleOutlined,
  MoreOutlined,
  SafetyCertificateOutlined,
} from "@icons";
import { SSLCertViewModal } from "./SSLCertViewModal";
import { apiClient } from "../../apiClient";
import { columnSearchProps } from "../columnSearch";
import { RowActionButton } from "../RowActionButton";
import { expiryFraction, matchesFilter, modeTag, type SSLFilter } from "./sslHealth";

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
  // GH #1221: configured SANs the issued cert doesn't cover yet (no public DNS
  // at issuance); the drift pass adds them once they resolve.
  pending_sans?: string[];
  // Domain ssl_mode (le/self/custom/none/shared) — absent on the synthetic
  // panel-cert:*/mail-cert:* rows (Certificate console Mode column).
  ssl_mode?: string;
}

interface SSLManagerTableProps {
  endpoint: string;
  showOwner: boolean;
  /** Health-bucket filter driven by the admin summary tiles. Default "all". */
  statusFilter?: SSLFilter;
  /** Drop panel-cert:* synthetic rows — the admin SYSTEM band shows them instead. */
  hideSystemRows?: boolean;
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
  statusFilter = "all",
  hideSystemRows = false,
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
  const [viewRow, setViewRow] = useState<SSLCertificate | null>(null); // GH #1355

  // Fetch SSL certificates
  const { data, isLoading, error } = useQuery({
    queryKey: ["ssl-manager", endpoint],
    queryFn: async () => {
      const response = await apiClient.get(endpoint);
      return response.data.items as SSLCertificate[];
    },
  });

  const filteredData = useMemo(() => {
    if (!data) return data;
    const needle = search.toLowerCase();
    return data.filter((row) => {
      if (hideSystemRows && row.id.startsWith("panel-cert:")) return false;
      if (!matchesFilter(row, statusFilter)) return false;
      if (!needle) return true;
      return (
        row.domain_name.toLowerCase().includes(needle) ||
        (row.user_username ?? "").toLowerCase().includes(needle)
      );
    });
  }, [data, search, statusFilter, hideSystemRows]);

  // Renew certificate mutation
  const renewMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/ssl/renew`);
    },
    onSuccess: () => {
      feedback.message.success("Certificate renewal initiated");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      feedback.message.error("Failed to renew certificate");
    },
  });

  // Revoke certificate mutation
  const revokeMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.delete(`/domains/${domainId}/ssl`);
    },
    onSuccess: () => {
      feedback.message.success("Certificate revoked");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      feedback.message.error("Failed to revoke certificate");
    },
  });

  // Retry certificate mutation
  const retryMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/ssl/retry`);
    },
    onSuccess: () => {
      feedback.message.success("Retry queued");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: (error: unknown) => {
      // GH #1221: the list can be stale — the ticker may have moved the cert to
      // `pending` (already issuing) since the row rendered. Show the server's
      // real reason instead of a blanket "failed", and refetch so the row
      // reflects the true status.
      const resp = (error as { response?: { status?: number; data?: { detail?: string } } })?.response;
      const detail = resp?.data?.detail;
      if (resp?.status === 409) {
        feedback.message.info(detail ?? "This certificate isn't in a retryable state right now.");
      } else {
        feedback.message.error(detail ?? "Failed to queue retry");
      }
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
  });

  // Reissue mutation for per-domain mail certs (mail-cert:<domainID> rows).
  // Hits the mail-certificate endpoint, NOT the website-cert retry/renew.
  const reissueMailMutation = useMutation({
    mutationFn: async (domainId: string) => {
      await apiClient.post(`/domains/${domainId}/mail-certificate/reissue`);
    },
    onSuccess: () => {
      feedback.message.success("Mail certificate reissue queued");
      queryClient.invalidateQueries({ queryKey: ["ssl-manager", endpoint] });
    },
    onError: () => {
      feedback.message.error("Failed to reissue mail certificate");
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
            {(record.pending_sans?.length ?? 0) > 0 && (
              <Tooltip
                title={`Configured but not on the certificate yet — added automatically once their DNS resolves publicly: ${record.pending_sans!.join(", ")}`}
              >
                <Tag color="orange" style={{ fontSize: 11, marginTop: 2 }}>
                  {record.pending_sans!.length} SAN{record.pending_sans!.length === 1 ? "" : "s"} pending DNS
                </Tag>
              </Tooltip>
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
      title: "Mode",
      dataIndex: "ssl_mode",
      key: "ssl_mode",
      sorter: (a: SSLCertificate, b: SSLCertificate) => (a.ssl_mode ?? "").localeCompare(b.ssl_mode ?? ""),
      render: (_: unknown, record: SSLCertificate) => {
        if (record.id.startsWith("panel-cert:")) return <Tag color="purple">panel</Tag>;
        const tag = modeTag(record.ssl_mode);
        return tag ? <Tag color={tag.color}>{tag.label}</Tag> : <span>—</span>;
      },
    },
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
        // GH #1356: a cert that failed then succeeded keeps its stale last_error;
        // don't surface the error affordance once it's issued.
        const hasError = !!record.last_error && record.status !== "issued";
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
      title: "Expires",
      dataIndex: "expires_at",
      sorter: (a: SSLCertificate, b: SSLCertificate) => (a.expires_at ? +new Date(a.expires_at) : 0) - (b.expires_at ? +new Date(b.expires_at) : 0),
      key: "expires_at",
      render: (dateStr: string | null, record: SSLCertificate) => {
        // Issued + Last check fold into this column as a muted second
        // line — three near-empty date columns collapsed into one.
        const metaParts = [
          record.issued_at ? `issued ${formatDate(record.issued_at)}` : null,
          record.last_attempt_at
            ? `checked ${new Date(record.last_attempt_at).toLocaleString()}`
            : null,
        ].filter(Boolean);
        const meta = metaParts.length > 0 && (
          <Typography.Text type="secondary" style={{ fontSize: 12, display: "block" }}>
            {metaParts.join(" · ")}
          </Typography.Text>
        );
        const withMeta = (main: JSX.Element) => (
          <Space direction="vertical" size={0}>
            {main}
            {meta}
          </Space>
        );
        if (record.status === "self_signed") {
          return withMeta(
            <Typography.Text type="secondary">
              {formatDate(dateStr)} <em>(self-signed)</em>
            </Typography.Text>,
          );
        }
        // Failed / retry-pending rows have no meaningful expiry — show the
        // retry schedule instead (was buried in the status tooltip).
        if (record.status === "failed" || record.status === "pending_acme_retry") {
          const retryAt = record.next_retry_at
            ? new Date(record.next_retry_at).toLocaleString()
            : null;
          return withMeta(
            <Typography.Text type="secondary">
              {retryAt ? `retry ${retryAt}` : "—"}
              {record.retry_count ? ` · attempt ${record.retry_count}` : ""}
            </Typography.Text>,
          );
        }
        const days = daysUntilExpiry(dateStr);
        const fraction = expiryFraction(dateStr);
        if (days === null || fraction === null) return withMeta(formatExpiry(dateStr));
        // Days-left meter over a 90-day LE lifetime: green >30d, orange ≤30d,
        // red ≤14d/expired. Date + relative label stay on hover.
        const color = days <= 14 ? "#ff4d4f" : days <= 30 ? "#fa8c16" : "#52c41a";
        return withMeta(
          <Tooltip title={formatExpiry(dateStr)}>
            <Space size={8}>
              <span
                style={{
                  display: "inline-block",
                  width: 60,
                  height: 4,
                  borderRadius: 2,
                  background: "rgba(0,0,0,0.06)",
                }}
              >
                <span
                  style={{
                    display: "block",
                    width: `${Math.round(fraction * 100)}%`,
                    height: 4,
                    borderRadius: 2,
                    background: color,
                  }}
                />
              </span>
              <Typography.Text
                style={days <= 30 ? { color, fontWeight: 600 } : undefined}
              >
                {days < 0 ? "expired" : `${days} d`}
              </Typography.Text>
            </Space>
          </Tooltip>,
        );
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
              <RowActionButton
                icon={<RedoOutlined />}
                loading={reissueMailMutation.isPending}
                onClick={() => reissueMailMutation.mutate(record.domain_id)}
              >
                Reissue
              </RowActionButton>
            </Tooltip>
          );
        }
        const isRetryable = record.status === "failed" ||
          (record.status === "pending_acme_retry" && record.next_retry_at && new Date(record.next_retry_at) < new Date());
        // One labeled, state-appropriate primary action per row; the rest
        // live in the ⋯ overflow (Certificate console). Renew stays primary
        // for issued rows, Retry now for retryable failures.
        const menuItems = [
          // GH #1355: view the issued cert (only issued rows have one on disk).
          ...(record.status === "issued"
            ? [{ key: "view", icon: <SafetyCertificateOutlined />, label: "View certificate" }]
            : []),
          ...(record.status === "issued"
            ? [{ key: "revoke", danger: true, icon: <DeleteOutlined />, label: t("sslmanagertable.revoke_certificate") }]
            : []),
          ...(record.status === "pending_acme_retry" && !isRetryable
            ? [{ key: "retry", icon: <RedoOutlined />, label: "Force retry now" }]
            : []),
          ...(record.last_error && record.status !== "issued"
            ? [{ key: "error", icon: <ExclamationCircleOutlined />, label: t("sslmanagertable.show_last_error") }]
            : []),
        ];
        const onMenuClick = ({ key }: { key: string }) => {
          if (key === "retry") retryMutation.mutate(record.domain_id);
          else if (key === "view") setViewRow(record);
          else if (key === "error") setErrorRow(record);
          else if (key === "revoke") {
            feedback.modal.confirm({
              title: t("sslmanagertable.revoke_certificate"),
              content: t("sslmanagertable.are_you_sure_you_want_to_revoke_this_certifi"),
              okText: t("sslmanagertable.yes"),
              okButtonProps: { danger: true },
              cancelText: "No",
              onOk: () => revokeMutation.mutate(record.domain_id),
            });
          }
        };
        return (
          <Space>
            {record.status === "issued" && (
              <Tooltip title={t("sslmanagertable.renew_certificate")}>
                <RowActionButton
                  icon={<ReloadOutlined />}
                  loading={renewMutation.isPending}
                  onClick={() => renewMutation.mutate(record.domain_id)}
                >
                  Renew
                </RowActionButton>
              </Tooltip>
            )}
            {isRetryable && (
              <Tooltip title={t("sslmanagertable.force_acme_retry_now")}>
                <RowActionButton
                  icon={<RedoOutlined />}
                  loading={retryMutation.isPending}
                  onClick={() => retryMutation.mutate(record.domain_id)}
                >
                  Retry now
                </RowActionButton>
              </Tooltip>
            )}
            {menuItems.length > 0 && (
              <Dropdown menu={{ items: menuItems, onClick: onMenuClick }} placement="bottomRight">
                <RowActionButton color="default" icon={<MoreOutlined />} aria-label={`More actions for ${record.domain_name}`} />
              </Dropdown>
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
            pagination={{ defaultPageSize: 25, showSizeChanger: true }}
            scroll={{ x: "max-content" }}
          />
        </Space>
      )}
      <SSLCertViewModal
        domainId={viewRow?.domain_id ?? null}
        domainName={viewRow?.domain_name}
        onClose={() => setViewRow(null)}
      />
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
