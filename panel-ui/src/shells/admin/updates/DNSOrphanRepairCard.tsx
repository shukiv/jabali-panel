// DNSOrphanRepairCard — GH #1620. Surfaces the one-time PowerDNS orphan-record
// cleanup in the admin Repair Center: `jabali dns prune-orphan-records` (scan is
// a dry-run; delete is guarded + purges the pdns caches). #1629 already stopped
// NEW zone deletes stranding child rows, so this is a one-shot cleanup for rows a
// pre-#1629 delete left behind — a Repair Center action, not a recurring sweep.
// Both proxy the agent `dns.reap-orphans` verb via
// GET/POST /admin/updates/repair/dns/orphans*; nothing is re-implemented here.
import { useTranslation } from "react-i18next";
import {
  Alert,
  App,
  Button,
  Card,
  List,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { DatabaseOutlined, ReloadOutlined } from "@icons";
import { useQuery, useMutation } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";

const { Text } = Typography;

interface DNSOrphansResult {
  applied: boolean;
  counts: Record<string, number> | null;
  deleted: Record<string, number> | null;
  names: string[] | null;
}

// sum totals the per-table row counts; the agent returns one entry per PowerDNS
// backend table (records, domainmetadata, comments, cryptokeys).
function sum(counts: Record<string, number> | null | undefined): number {
  if (!counts) return 0;
  return Object.values(counts).reduce((a, b) => a + b, 0);
}

export function DNSOrphanRepairCard() {
  const { t } = useTranslation();
  const { message } = App.useApp();

  const orphans = useQuery({
    queryKey: ["admin", "dns-orphan-records"],
    enabled: false, // manual — only after the admin clicks Scan
    queryFn: async () => {
      const { data } = await apiClient.get<DNSOrphansResult>(
        "/admin/updates/repair/dns/orphans",
      );
      return data;
    },
  });

  const prune = useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<DNSOrphansResult>(
        "/admin/updates/repair/dns/orphans/prune",
      );
      return data;
    },
    onSuccess: (data) => {
      message.success(
        `Removed ${sum(data.deleted)} orphan row(s) and purged the pdns caches`,
      );
      orphans.refetch();
    },
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "prune failed"),
  });

  const counts = orphans.data?.counts ?? null;
  const names = orphans.data?.names ?? [];
  const totalRows = sum(counts);

  const countRows = counts
    ? Object.entries(counts)
        .filter(([, n]) => n > 0)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([table, rows]) => ({ key: table, table, rows }))
    : [];

  return (
    <Card
      title={
        <Space>
          <DatabaseOutlined />
          PowerDNS orphan records
        </Space>
      }
    >
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <Text type="secondary">
          Rows in the PowerDNS backend (records, DNSSEC keys, metadata) whose
          domain was deleted before the #1629 teardown fix. They keep answering
          off the pdns caches and collide as duplicates when the domain is
          re-added. Scanning is a dry-run; deletion is irreversible and purges
          the pdns caches for the affected names.
        </Text>

        <Space wrap>
          <Button
            icon={<ReloadOutlined />}
            loading={orphans.isFetching}
            onClick={() => orphans.refetch()}
          >
            Scan for orphan records
          </Button>
          <Popconfirm
            title={t("dnsorphanrepaircard.delete_all_orphan_records")}
            description={t("dnsorphanrepaircard.this_is_irreversible_it_removes_the_pdns_rows")}
            okText={t("dnsorphanrepaircard.delete_permanently")}
            okButtonProps={{ danger: true }}
            cancelText={t("dnsorphanrepaircard.cancel")}
            onConfirm={() => prune.mutate()}
            disabled={totalRows === 0}
          >
            <Button danger loading={prune.isPending} disabled={totalRows === 0}>
              Delete {totalRows > 0 ? `${totalRows} ` : ""}record
              {totalRows === 1 ? "" : "s"}
            </Button>
          </Popconfirm>
        </Space>

        {orphans.isFetched && totalRows === 0 && (
          <Alert
            type="success"
            showIcon
            message={t("dnsorphanrepaircard.no_orphan_records_found")}
          />
        )}

        {totalRows > 0 && (
          <>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {totalRows} orphan row(s) across {names.length} distinct record
              name(s).
            </Text>
            <Table
              size="small"
              bordered
              pagination={false}
              dataSource={countRows}
              columns={[
                { title: "Table", dataIndex: "table", key: "table" },
                {
                  title: "Orphan rows",
                  dataIndex: "rows",
                  key: "rows",
                  align: "right",
                },
              ]}
            />
            {names.length > 0 && (
              <List
                size="small"
                bordered
                header={<Text strong>Affected names</Text>}
                dataSource={names}
                pagination={names.length > 20 ? { pageSize: 20 } : false}
                renderItem={(name) => (
                  <List.Item>
                    <Tag color="warning">orphan</Tag>
                    <Text code>{name}</Text>
                  </List.Item>
                )}
              />
            )}
          </>
        )}
      </Space>
    </Card>
  );
}
