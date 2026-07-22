import { useTranslation } from "react-i18next";
import { useState, useEffect } from "react";
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  EditOutlined,
  LockOutlined,
  CheckOutlined,
  CloseOutlined,
  PlusOutlined,
} from "@icons";
import { RowActions } from "../../components/RowActions";
import {
  Button,
  Drawer,
  Flex,
  Space,
  Table,
  Tag,
  Typography,
  Card,
  Input,
  Select,
  InputNumber,
  Collapse,
  Switch,
  Row,
  Col,
  Spin,
  Empty,
  notification,
  Tooltip,
  Grid,
} from "antd";
import { useLocation, useNavigate, useParams } from "react-router";

// Post-M21 notify shim — matches the Refine useNotification().open
// contract so the call sites below need no other change.
//
// The `open` function is defined at module scope (not inside the hook)
// so it has a STABLE reference across renders. Returning a freshly-
// allocated { open: ... } object on every call would make `open`'s
// identity churn, and useEffects that include `open` in their deps
// would re-fire every render → infinite refetch loop → API 429s.
// That's exactly the bug the DNS Records page hit (rate-limited
// "Failed to load" notification stack).
type NotifyInput = {
  type?: "success" | "error" | "warning" | "info";
  message: string;
  description?: React.ReactNode;
};
const stableNotifyOpen = (input: NotifyInput) => {
  notification.open({
    message: input.message,
    description: input.description,
    type: input.type,
  });
};
const stableNotify = { open: stableNotifyOpen };
function useNotification() {
  return stableNotify;
}

import { apiClient } from "../../apiClient";

// Type definitions
type DNSRecordType = "A" | "AAAA" | "CNAME" | "MX" | "TXT" | "NS" | "SRV" | "CAA";

// recordTypeColor maps each DNS record type to an AntD Tag colour so
// the table scans visually — A/AAAA share the blue family (IPv4/IPv6
// address records), MX is orange (mail), TXT is green (metadata/SPF/
// DMARC), CNAME is purple (aliases), NS is magenta (delegation).
const recordTypeColor: Record<DNSRecordType, string> = {
  A: "blue",
  AAAA: "geekblue",
  CNAME: "purple",
  MX: "orange",
  TXT: "green",
  NS: "magenta",
  // SRV (service discovery) + CAA (cert-authority authorization) are
  // auto-created on email-enable (GH #134); give them colours so the
  // table renders them cleanly even though they're not in the manual
  // add-record dropdown.
  SRV: "cyan",
  CAA: "gold",
};

interface DNSZone {
  id: string;
  domain_id: string;
  is_enabled: boolean;
  serial: number;
  refresh_seconds: number;
  retry_seconds: number;
  expire_seconds: number;
  minimum_ttl: number;
  created_at: string;
  updated_at: string;
}

interface DNSRecord {
  id: string;
  zone_id: string;
  name: string;
  type: DNSRecordType;
  content: string;
  ttl: number;
  priority?: number;
  managed: boolean;
  is_enabled: boolean;
  created_at: string;
  updated_at: string;
}

// SystemRecord is the shape the panel-api returns from
// GET /domains/:id/dns/system-records — the SOA + NS rows that
// dnscompile.Compile injects at render time. Lives server-side,
// never in dns_records; read-only in the UI.
interface SystemRecord {
  name: string;
  type: "SOA" | "NS";
  content: string;
  ttl: number;
}

interface Domain {
  id: string;
  name: string;
}

// Type-aware placeholder helpers
const getPlaceholders = (
  type: DNSRecordType
): { nameHelper: string; contentHelper: string } => {
  switch (type) {
    case "A":
      return {
        nameHelper: "e.g. www, @ (for root), blog",
        contentHelper: "IPv4 address, e.g. 192.0.2.1",
      };
    case "AAAA":
      return {
        nameHelper: "e.g. www, @ (for root), blog",
        contentHelper: "IPv6 address, e.g. 2001:db8::1",
      };
    case "CNAME":
      return {
        nameHelper: "e.g. blog (subdomain name)",
        contentHelper: "Target domain, e.g. example.com or target.example.com.",
      };
    case "MX":
      return {
        nameHelper: "Usually @ (root)",
        contentHelper: "Mail server, e.g. mail.example.com",
      };
    case "TXT":
      return {
        nameHelper: "e.g. @, _dmarc, _acme-challenge",
        contentHelper: 'Text value in quotes, e.g. "v=spf1 mx ~all"',
      };
    case "NS":
      return {
        nameHelper: "e.g. sub (subdomain)",
        contentHelper: "Nameserver, e.g. ns.external.com",
      };
    case "SRV":
      return {
        nameHelper: "e.g. _imap._tcp, _submission._tcp",
        contentHelper: "priority weight port target, e.g. 0 1 993 mail.example.com",
      };
    case "CAA":
      return {
        nameHelper: "Usually @ (root)",
        contentHelper: 'flags tag "value", e.g. 0 issue "letsencrypt.org"',
      };
  }
};

export const DNSRecordsPage = () => {
  const { t } = useTranslation();
  const { id: domainId } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const { open } = useNotification();
  const screens = Grid.useBreakpoint();
  const isCompact = !screens.md;

  // Back-link target: DNS list in the same shell we're currently in.
  // Admin path prefix is /jabali-admin; user path prefix is /jabali-panel.
  const dnsListPath = location.pathname.startsWith("/jabali-admin")
    ? "/jabali-admin/dns"
    : "/jabali-panel/dns";

  // State
  const [domain, setDomain] = useState<Domain | null>(null);
  const [zone, setZone] = useState<DNSZone | null>(null);
  const [records, setRecords] = useState<DNSRecord[]>([]);
  const [systemRecords, setSystemRecords] = useState<SystemRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [zoneNotProvisioned, setZoneNotProvisioned] = useState(false);
  const [savingZone, setSavingZone] = useState(false);
  const [deletingRecordId, setDeletingRecordId] = useState<string | null>(null);

  // Single Drawer drives both add + edit — open with editingRecord=null
  // for create, with a record for edit. Mirrors UserDrawer pattern.
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingRecord, setEditingRecord] = useState<DNSRecord | null>(null);
  const [formType, setFormType] = useState<DNSRecordType>("A");
  const [formName, setFormName] = useState("");
  const [formContent, setFormContent] = useState("");
  const [formPriority, setFormPriority] = useState<number | null>(null);
  const [formTTL, setFormTTL] = useState(300);
  const [submitting, setSubmitting] = useState(false);
  // DNS record-type permissions (GH #466). Fetched from /dns/policy; admins
  // get an all-true matrix. On fetch failure we fall OPEN in the UI (server
  // still enforces) so a hiccup never blocks the page.
  const [recordPolicy, setRecordPolicy] = useState<Record<string, { create?: boolean; edit?: boolean; delete?: boolean }>>({});
  const [policyIsAdmin, setPolicyIsAdmin] = useState(false);
  const [policyLoaded, setPolicyLoaded] = useState(false);

  // Load domain data
  useEffect(() => {
    const loadDomain = async () => {
      try {
        const res = await apiClient.get(`/domains/${domainId}`);
        setDomain(res.data);
      } catch (err) {
        open?.({
          type: "error",
          message: "Failed to load domain",
        });
      }
    };

    if (domainId) {
      loadDomain();
    }
  }, [domainId, open]);

  // Load zone and records
  useEffect(() => {
    const loadDnsData = async () => {
      if (!domainId) return;

      setLoading(true);
      try {
        // Load zone data
        try {
          const zoneRes = await apiClient.get(`/domains/${domainId}/dns/zone`);
          setZone(zoneRes.data.zone);
          setZoneNotProvisioned(false);
        } catch (err: unknown) {
          const e = err as {
            response?: { status?: number; data?: { error?: string } };
          };
          if (e.response?.status === 404 && e.response?.data?.error === "zone_not_provisioned") {
            setZoneNotProvisioned(true);
            setZone(null);
          } else {
            throw err;
          }
        }

        // Load records
        const recordsRes = await apiClient.get(
          `/domains/${domainId}/dns/records`
        );
        setRecords(recordsRes.data.records);

        // Load system records (SOA + NS, read-only). Non-fatal: a
        // fresh install before server_settings is seeded returns an
        // empty-ish zone and that's fine — the panel just shows
        // whatever dnscompile.SystemRecords returns for a nil srv.
        try {
          const sysRes = await apiClient.get(
            `/domains/${domainId}/dns/system-records`
          );
          setSystemRecords(sysRes.data.system_records ?? []);
        } catch {
          setSystemRecords([]);
        }
      } catch (err) {
        const e = err as {
          response?: { data?: { detail?: string } };
          message?: string;
        };
        open?.({
          type: "error",
          message: "Failed to load DNS data",
          description:
            e.response?.data?.detail ?? e.message ?? "Unknown error",
        });
      } finally {
        setLoading(false);
      }
    };

    loadDnsData();
  }, [domainId, open]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await apiClient.get<{ policy?: Record<string, { create?: boolean; edit?: boolean; delete?: boolean }>; is_admin?: boolean }>(
          "/dns/policy",
        );
        if (!cancelled) {
          setRecordPolicy(res.data.policy ?? {});
          setPolicyIsAdmin(!!res.data.is_admin);
          setPolicyLoaded(true);
        }
      } catch {
        // Fall open in the UI; the server enforces the policy regardless.
        if (!cancelled) setPolicyLoaded(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // allows reports whether the current user may op on a record type. Admins and
  // the not-yet-loaded/failed state fall open (server-side enforcement is the
  // real gate).
  const allowsRecord = (t: string, op: "create" | "edit" | "delete") =>
    policyIsAdmin || !policyLoaded || !!recordPolicy[t]?.[op];

  const ALL_TYPE_OPTIONS = [
    { value: "A", label: "A" },
    { value: "AAAA", label: "AAAA" },
    { value: "CNAME", label: "CNAME" },
    { value: "MX", label: "MX" },
    { value: "TXT", label: "TXT" },
    { value: "NS", label: "NS" },
  ];
  const creatableTypeOptions = ALL_TYPE_OPTIONS.filter((o) => allowsRecord(o.value, "create"));

  // Drawer open helpers
  const openAddDrawer = () => {
    setEditingRecord(null);
    setFormType((creatableTypeOptions[0]?.value as DNSRecordType) ?? "A");
    setFormName("");
    setFormContent("");
    setFormPriority(null);
    setFormTTL(300);
    setDrawerOpen(true);
  };

  const openEditDrawer = (record: DNSRecord) => {
    setEditingRecord(record);
    setFormType(record.type);
    setFormName(record.name);
    setFormContent(record.content);
    setFormPriority(record.priority ?? null);
    setFormTTL(record.ttl);
    setDrawerOpen(true);
  };

  const closeDrawer = () => {
    setDrawerOpen(false);
    setEditingRecord(null);
  };

  // Handle zone settings save
  const handleZoneSave = async () => {
    if (!zone || !domainId) return;

    setSavingZone(true);
    try {
      const res = await apiClient.patch(`/domains/${domainId}/dns/zone`, {
        refresh_seconds: zone.refresh_seconds,
        retry_seconds: zone.retry_seconds,
        expire_seconds: zone.expire_seconds,
        minimum_ttl: zone.minimum_ttl,
        is_enabled: zone.is_enabled,
      });

      setZone(res.data.zone);
      open?.({
        type: "success",
        message: "Zone settings saved",
      });
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      open?.({
        type: "error",
        message: "Failed to save zone settings",
        description:
          e.response?.data?.detail ?? e.message ?? "Unknown error",
      });
    } finally {
      setSavingZone(false);
    }
  };

  // Submit drawer form — branches on editingRecord for POST vs PATCH.
  const handleSubmit = async () => {
    if (!domainId || !formName || !formContent) {
      open?.({
        type: "error",
        message: "Name and content are required",
      });
      return;
    }

    setSubmitting(true);
    try {
      if (editingRecord) {
        const res = await apiClient.patch(
          `/dns/records/${editingRecord.id}`,
          {
            name: formName,
            content: formContent,
            ttl: formTTL,
            ...(editingRecord.type === "MX" && formPriority !== null
              ? { priority: formPriority }
              : {}),
          }
        );
        setRecords(
          records.map((r) =>
            r.id === editingRecord.id ? res.data.record : r
          )
        );
        open?.({ type: "success", message: "Record updated" });
      } else {
        const res = await apiClient.post(
          `/domains/${domainId}/dns/records`,
          {
            name: formName,
            type: formType,
            content: formContent,
            ttl: formTTL,
            ...(formType === "MX" && formPriority !== null
              ? { priority: formPriority }
              : {}),
          }
        );
        setRecords([...records, res.data.record]);
        open?.({ type: "success", message: "Record added" });
      }
      closeDrawer();
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      open?.({
        type: "error",
        message: editingRecord ? "Failed to update record" : "Failed to add record",
        description:
          e.response?.data?.detail ?? e.message ?? "Unknown error",
      });
    } finally {
      setSubmitting(false);
    }
  };

  // Handle record delete
  const handleDeleteRecord = async (recordId: string) => {
    setDeletingRecordId(recordId);
    try {
      await apiClient.delete(`/dns/records/${recordId}`);
      setRecords(records.filter((r) => r.id !== recordId));
      open?.({
        type: "success",
        message: "Record deleted",
      });
    } catch (err) {
      const e = err as {
        response?: { data?: { detail?: string } };
        message?: string;
      };
      open?.({
        type: "error",
        message: "Failed to delete record",
        description:
          e.response?.data?.detail ?? e.message ?? "Unknown error",
      });
    } finally {
      setDeletingRecordId(null);
    }
  };

  const placeholders = getPlaceholders(formType);

  // Check if a record should be read-only
  const isRecordReadOnly = (record: DNSRecord) => {
    const recordType = record.type as string;
    return record.managed && (recordType === "SOA" || recordType === "NS");
  };

  // Filter out SOA records from display (type guard)
  const displayRecords = records.filter((r) => {
    const recordType = r.type as string;
    return recordType !== "SOA";
  });

  if (loading) {
    return (
      <div style={{ textAlign: "center" }}>
        <Spin />
      </div>
    );
  }

  if (zoneNotProvisioned) {
    return (
      <div >
        <Button
          type="text"
          icon={<ArrowLeftOutlined />}
          onClick={() => navigate(dnsListPath)}
          style={{ marginBottom: 16 }}
        >
          Back to DNS
        </Button>

        <Card
          style={{
            maxWidth: 600,
            margin: "0 auto",
            textAlign: "center",
            marginTop: 60,
          }}
        >
          <Typography.Title level={4}>
            DNS Zone Not Provisioned
          </Typography.Title>
          <Typography.Paragraph>
            The DNS zone for this domain has not yet been provisioned. This
            normally happens automatically on domain creation. If you just
            created this domain, give the reconciler ~60 seconds. Otherwise, try
            saving the domain from the domain list to re-trigger provisioning.
          </Typography.Paragraph>
        </Card>
      </div>
    );
  }

  return (
    <div >
      {/* Header */}
      <Button
        type="text"
        icon={<ArrowLeftOutlined />}
        onClick={() => navigate(dnsListPath)}
        style={{ marginBottom: 16 }}
      >
        Back to DNS
      </Button>

      <Flex
        wrap
        gap="middle"
        justify="space-between"
        align="center"
        style={{ marginBottom: 16 }}
      >
        <Typography.Title level={3} style={{ margin: 0, wordBreak: "break-word" }}>
          DNS Records for {domain?.name}
        </Typography.Title>
        <Tooltip title={creatableTypeOptions.length === 0 ? "Your administrator does not allow creating any DNS record types." : ""}>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={openAddDrawer}
            disabled={creatableTypeOptions.length === 0}
          >
            Add Record
          </Button>
        </Tooltip>
      </Flex>

      {zone && (
        <Typography.Text
          type="secondary"
          style={{ display: "block", marginBottom: 24 }}
        >
          Zone serial {zone.serial} • {displayRecords.length} records
        </Typography.Text>
      )}

      {/* Records Table */}
      {displayRecords.length === 0 ? (
        <Card style={{ marginBottom: 24 }}>
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("dnsrecordspage.no_dns_records_yet")} />
        </Card>
      ) : (
        <Card style={{ marginBottom: 24 }}>
          <Table<DNSRecord>
            dataSource={displayRecords}
            rowKey="id"
            pagination={false}
            scroll={{ x: "max-content" }}
            columns={[
              // Pixel widths instead of percentages: scroll.x="max-content"
              // forces table width to content, and percentages resolved
              // against an inflated container made Name/Type swell while
              // Content/TTL/Priority/Actions pushed off the right edge.
              // No width on Content → flex-fills remaining viewport. Actions
              // fixed:"right" so Edit/Delete stay reachable on narrow tabs.
              {
                title: "Name",
                dataIndex: "name",
                key: "name",
                width: isCompact ? 110 : 200,
                ellipsis: true,
                render: (text: string) => text || "@",
              },
              {
                title: "Type",
                dataIndex: "type",
                key: "type",
                width: 90,
                render: (_: unknown, record: DNSRecord) => (
                  <Tag color={recordTypeColor[record.type] ?? "default"}>
                    {record.type}
                  </Tag>
                ),
              },
              {
                title: "Content",
                dataIndex: "content",
                key: "content",
                // Bounded width + ellipsis so long records (DKIM/RSA keys,
                // SPF) truncate with "…" instead of stretching the table
                // under the fixed Actions column. Full value on hover.
                width: 420,
                ellipsis: { showTitle: false },
                render: (text: string) => (
                  <Tooltip title={text} placement="topLeft">
                    <Typography.Text
                      style={{ fontFamily: "monospace" }}
                      ellipsis
                    >
                      {text}
                    </Typography.Text>
                  </Tooltip>
                ),
              },
              {
                title: "TTL",
                dataIndex: "ttl",
                key: "ttl",
                width: 100,
              },
              {
                title: "Priority",
                dataIndex: "priority",
                key: "priority",
                width: 110,
                render: (priority: number | undefined) =>
                  priority !== undefined ? priority : "—",
              },
              {
                title: "Actions",
                key: "actions",
                width: isCompact ? 100 : 180,
                fixed: "right" as const,
                render: (_: unknown, record: DNSRecord) => {
                  const readonly = isRecordReadOnly(record);

                  if (readonly) {
                    return (
                      <Space>
                        <LockOutlined />
                        {!isCompact && (
                          <Typography.Text type="secondary">
                            Managed
                          </Typography.Text>
                        )}
                      </Space>
                    );
                  }

                  // Gate per the admin's DNS record-type policy (GH #466).
                  const canEdit = allowsRecord(record.type as string, "edit");
                  const canDelete = allowsRecord(record.type as string, "delete");
                  if (!canEdit && !canDelete) {
                    return (
                      <Tooltip title={t("dnsrecordspage.your_administrator_has_restricted_changes_to")}>
                        <Space>
                          <LockOutlined />
                          {!isCompact && <Typography.Text type="secondary">Restricted</Typography.Text>}
                        </Space>
                      </Tooltip>
                    );
                  }
                  const rowActions = [];
                  if (canEdit) {
                    rowActions.push({ key: "edit", label: "Edit", icon: <EditOutlined />, onClick: () => openEditDrawer(record) });
                  }
                  if (canDelete) {
                    rowActions.push({
                      key: "delete",
                      label: "Delete",
                      icon: <DeleteOutlined />,
                      danger: true,
                      loading: deletingRecordId === record.id,
                      onClick: () => handleDeleteRecord(record.id),
                      confirm: { title: "Delete record?", description: "This action cannot be undone.", okText: "Delete" },
                    });
                  }
                  return (
                    <RowActions
                      actions={rowActions}
                    />
                  );
                },
              },
            ]}
          />
        </Card>
      )}

      {/* System Records — auto-generated SOA + NS that pdns serves
          but are never stored in dns_records. Read-only; editable
          only by changing server-wide nameserver / admin-email
          settings on the Server Settings page. */}
      {systemRecords.length > 0 && (
        <Card
          title={t("dnsrecordspage.system_records")}
          style={{ marginBottom: 24 }}
          extra={
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Auto-generated — change via Server Settings
            </Typography.Text>
          }
        >
          <Table<SystemRecord>
            dataSource={systemRecords}
            rowKey={(r) => `${r.type}-${r.name}-${r.content}`}
            pagination={false}
            size="small"
            scroll={{ x: "max-content" }}
            columns={[
              // Pixel widths — see editable-table comment above for the
              // pct-vs-max-content interaction this avoids.
              {
                title: "Name",
                dataIndex: "name",
                key: "name",
                width: 240,
                ellipsis: true,
                render: (text: string) => text || "@",
              },
              {
                title: "Type",
                dataIndex: "type",
                key: "type",
                width: 90,
                render: (t: string) => (
                  <Tag color={t === "NS" ? "magenta" : "default"}>{t}</Tag>
                ),
              },
              {
                title: "Content",
                dataIndex: "content",
                key: "content",
                width: 420,
                ellipsis: { showTitle: false },
                render: (text: string) => (
                  <Tooltip title={text} placement="topLeft">
                    <Typography.Text
                      style={{ fontFamily: "monospace" }}
                      ellipsis
                    >
                      {text}
                    </Typography.Text>
                  </Tooltip>
                ),
              },
              {
                title: "TTL",
                dataIndex: "ttl",
                key: "ttl",
                width: 100,
              },
            ]}
          />
        </Card>
      )}

      {/* Zone Settings — collapsed by default, advanced controls. */}
      {zone && (
        <Collapse
          style={{ marginBottom: 24 }}
          items={[
            {
              key: "zone-settings",
              label: "Zone Settings",
              children: (
                <div>
                  <Row gutter={16} style={{ marginBottom: 16 }}>
                    <Col span={12}>
                      <div style={{ marginBottom: 8 }}>
                        <Typography.Text>Refresh (seconds)</Typography.Text>
                      </div>
                      <InputNumber
                        min={0}
                        value={zone.refresh_seconds}
                        onChange={(v) =>
                          setZone({ ...zone, refresh_seconds: v ?? 0 })
                        }
                        style={{ width: "100%" }}
                      />
                    </Col>
                    <Col span={12}>
                      <div style={{ marginBottom: 8 }}>
                        <Typography.Text>Retry (seconds)</Typography.Text>
                      </div>
                      <InputNumber
                        min={0}
                        value={zone.retry_seconds}
                        onChange={(v) =>
                          setZone({ ...zone, retry_seconds: v ?? 0 })
                        }
                        style={{ width: "100%" }}
                      />
                    </Col>
                  </Row>

                  <Row gutter={16} style={{ marginBottom: 16 }}>
                    <Col span={12}>
                      <div style={{ marginBottom: 8 }}>
                        <Typography.Text>Expire (seconds)</Typography.Text>
                      </div>
                      <InputNumber
                        min={0}
                        value={zone.expire_seconds}
                        onChange={(v) =>
                          setZone({ ...zone, expire_seconds: v ?? 0 })
                        }
                        style={{ width: "100%" }}
                      />
                    </Col>
                    <Col span={12}>
                      <div style={{ marginBottom: 8 }}>
                        <Typography.Text>Minimum TTL (seconds)</Typography.Text>
                      </div>
                      <InputNumber
                        min={0}
                        value={zone.minimum_ttl}
                        onChange={(v) =>
                          setZone({ ...zone, minimum_ttl: v ?? 0 })
                        }
                        style={{ width: "100%" }}
                      />
                    </Col>
                  </Row>

                  <Row gutter={16} style={{ marginBottom: 16 }}>
                    <Col span={24}>
                      <Space>
                        <Typography.Text>Enabled</Typography.Text>
                        <Switch
                          checkedChildren={<CheckOutlined />}
                          unCheckedChildren={<CloseOutlined />}
                          checked={zone.is_enabled}
                          onChange={(v) =>
                            setZone({ ...zone, is_enabled: v })
                          }
                        />
                      </Space>
                    </Col>
                  </Row>

                  <Button
                    type="primary"
                    onClick={handleZoneSave}
                    loading={savingZone}
                  >
                    Save Zone Settings
                  </Button>
                </div>
              ),
            },
          ]}
        />
      )}

      {/* Add / Edit Record drawer */}
      <Drawer
        title={editingRecord ? "Edit Record" : "Add Record"}
        open={drawerOpen}
        onClose={closeDrawer}
        width={520}
        destroyOnHidden
        footer={
          <Space>
            <Button
              type="primary"
              loading={submitting}
              onClick={handleSubmit}
            >
              {editingRecord ? "Save" : "Add Record"}
            </Button>
            <Button onClick={closeDrawer}>Cancel</Button>
          </Space>
        }
      >
        <Row gutter={16} style={{ marginBottom: 16 }}>
          <Col span={12}>
            <div style={{ marginBottom: 8 }}>
              <Typography.Text>Type</Typography.Text>
            </div>
            <Select
              value={formType}
              onChange={setFormType}
              disabled={!!editingRecord}
              options={editingRecord ? ALL_TYPE_OPTIONS : creatableTypeOptions}
              style={{ width: "100%" }}
            />
          </Col>
          <Col span={12}>
            <div style={{ marginBottom: 8 }}>
              <Typography.Text>Name</Typography.Text>
            </div>
            <Input
              placeholder={placeholders.nameHelper}
              value={formName}
              onChange={(e) => setFormName(e.target.value)}
            />
          </Col>
        </Row>

        <Row gutter={16} style={{ marginBottom: 16 }}>
          <Col span={24}>
            <div style={{ marginBottom: 8 }}>
              <Typography.Text>Content</Typography.Text>
            </div>
            <Input
              placeholder={placeholders.contentHelper}
              value={formContent}
              onChange={(e) => setFormContent(e.target.value)}
            />
          </Col>
        </Row>

        <Row gutter={16} style={{ marginBottom: 16 }}>
          <Col span={12}>
            <div style={{ marginBottom: 8 }}>
              <Typography.Text>TTL (seconds)</Typography.Text>
            </div>
            <InputNumber
              min={0}
              value={formTTL}
              onChange={(v) => setFormTTL(v ?? 300)}
              style={{ width: "100%" }}
            />
          </Col>
          {formType === "MX" && (
            <Col span={12}>
              <div style={{ marginBottom: 8 }}>
                <Typography.Text>Priority</Typography.Text>
              </div>
              <InputNumber
                min={0}
                value={formPriority}
                onChange={setFormPriority}
                placeholder="e.g. 10"
                style={{ width: "100%" }}
              />
            </Col>
          )}
        </Row>
      </Drawer>
    </div>
  );
};
