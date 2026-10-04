// DNSSECKeysModal — a signed zone's keys and the DS records to publish at the
// registrar. Opened from "View DS & keys" in the DNS zone list's row menu
// (GH #1918); it replaced the separate DNSSEC tab. Both lists are read live
// through the agent, never cached.
import { useMemo, type ReactNode } from "react";
import { Alert, Button, Empty, Modal, Space, Spin, Table, Tag, Typography } from "antd";

import {
  algorithmLabel,
  digestTypeLabel,
  useDNSSECState,
  useDSRecords,
} from "../../hooks/useDNSSEC";

interface DNSSECKeysModalProps {
  domainID: string | null;
  domainName: string;
  open: boolean;
  onClose: () => void;
  // note is extra audience copy under the keys (the admin adapter explains how
  // signing is done; the tenant passes nothing).
  note?: ReactNode;
}

export function DNSSECKeysModal({ domainID, domainName, open, onClose, note }: DNSSECKeysModalProps) {
  const state = useDNSSECState(open ? (domainID ?? undefined) : undefined);
  const enabled = !!state.data?.enabled;
  const ds = useDSRecords(domainID ?? undefined, open && enabled);
  const keys = state.data?.keys ?? [];
  const records = useMemo(() => ds.data?.ds_records ?? [], [ds.data]);

  return (
    <Modal
      title={`DNSSEC keys · ${domainName}`}
      open={open}
      onCancel={onClose}
      footer={[
        <Button key="close" onClick={onClose}>
          Close
        </Button>,
      ]}
      width="min(720px, calc(100vw - 32px))"
      destroyOnHidden
    >
      {state.isLoading ? (
        <Spin />
      ) : state.isError ? (
        <Alert
          type="error"
          showIcon
          title="Failed to load DNSSEC state"
          description={(state.error as Error)?.message ?? "Unknown error"}
        />
      ) : !enabled ? (
        <Alert
          type="warning"
          showIcon
          title="DNSSEC is not enabled for this domain."
          description="Enable DNSSEC from the domain's ⋯ menu, then come back here for the DS record to publish at your registrar."
        />
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <div>
            <Typography.Title level={5}>Keys</Typography.Title>
            {keys.length === 0 ? (
              <Typography.Text type="secondary">Keys are still being generated.</Typography.Text>
            ) : (
              <Table
                rowKey={(k) => `${k.key_tag}-${k.key_type}`}
                dataSource={keys}
                pagination={false}
                size="small"
                scroll={{ x: "max-content" }}
                columns={[
                  {
                    title: "Type",
                    dataIndex: "key_type",
                    render: (v: string) => <Tag color={v === "KSK" ? "blue" : "geekblue"}>{v}</Tag>,
                  },
                  { title: "Key tag", dataIndex: "key_tag" },
                  {
                    title: "Algorithm",
                    dataIndex: "algorithm",
                    render: (v: number) => `${v} (${algorithmLabel(v)})`,
                  },
                  {
                    title: "State",
                    dataIndex: "active",
                    render: (v: boolean) => (v ? <Tag color="green">Active</Tag> : <Tag>Pending</Tag>),
                  },
                ]}
              />
            )}
            {note && (
              <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
                {note}
              </Typography.Paragraph>
            )}
          </div>
          <div>
            <Typography.Title level={5}>DS records</Typography.Title>
            {ds.isLoading ? (
              <Spin />
            ) : ds.isError ? (
              <Alert
                type="error"
                showIcon
                title="Failed to fetch DS records"
                description={(ds.error as Error)?.message ?? "Unknown error"}
              />
            ) : records.length === 0 ? (
              <Empty description="No DS records — the key may still be provisioning" />
            ) : (
              <>
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 16 }}
                  title="Publish these DS records at your registrar"
                  description="The registrar typically needs key tag, algorithm, digest type, and digest. Until the DS is in the parent zone, validators will not trust your signed data."
                />
                <Table
                  rowKey={(r) => `${r.key_tag}-${r.digest_type}`}
                  dataSource={records}
                  pagination={false}
                  size="small"
                  scroll={{ x: "max-content" }}
                  columns={[
                    { title: "Key tag", dataIndex: "key_tag" },
                    {
                      title: "Algorithm",
                      dataIndex: "algorithm",
                      render: (v: number) => `${v} (${algorithmLabel(v)})`,
                    },
                    {
                      title: "Digest type",
                      dataIndex: "digest_type",
                      render: (v: number) => `${v} (${digestTypeLabel(v)})`,
                    },
                    {
                      title: "Digest",
                      dataIndex: "digest",
                      render: (v: string) => (
                        <Typography.Text code copyable={{ text: v }}>
                          {v}
                        </Typography.Text>
                      ),
                    },
                  ]}
                />
              </>
            )}
          </div>
        </Space>
      )}
    </Modal>
  );
}
