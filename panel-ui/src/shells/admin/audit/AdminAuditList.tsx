// AdminAuditList — admin "Audit Log" forensics view (M49 / ADR-0106).
//
// Read-only: same useTableURL + SearchableTableStringQ shape as
// AdminIPList, minus the create/edit/delete surface. Backed by
// GET /api/v1/admin/audit (RequireAdmin). The list envelope
// {data,total,page,page_size} is read via query.items/query.total.
//
// Timestamps render in the server timezone (Server Settings →
// Timezone). Actor shows the resolved username (API batch-resolves the
// ULID). The Action cell opens a modal with the full recorded detail.
import { useTranslation } from "react-i18next";
import {
  Alert,
  App,
  Button,
  Card,
  InputNumber,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useState } from "react";
import { useMutation } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { SearchableTableStringQ } from "../../../components/SearchableTable";
import {
  AuditActionLabel,
  AuditDetail,
  type AuditRow,
  dash,
  fmtTSInTz,
  resultTag,
  useServerTz,
} from "../../../components/AuditEventDetail";
import { useTableURL } from "../../../hooks/useTableURL";

// AuditMaintenanceCard — GH #571. Hash-chain integrity verification (read-only)
// and retention pruning (destructive), surfaced from the Audit Log. Same core
// as `jabali audit verify` / `jabali audit prune`, run in-process by panel-api.
interface VerifyResult {
  ok: boolean;
  checked: number;
  total: number;
  broken_id: string;
}
const AuditMaintenanceCard = () => {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [days, setDays] = useState<number>(365);
  const [verifyResult, setVerifyResult] = useState<VerifyResult | null>(null);

  const verify = useMutation({
    mutationFn: async () =>
      (await apiClient.post<VerifyResult>("/admin/audit/verify")).data,
    onSuccess: (d) => setVerifyResult(d),
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "verify failed"),
  });

  const prune = useMutation({
    mutationFn: async () =>
      (await apiClient.post<{ pruned: number; days: number }>("/admin/audit/prune", { days })).data,
    onSuccess: (d) => message.success(`Pruned ${d.pruned} audit row(s) older than ${d.days} days`),
    onError: (e: unknown) =>
      message.error(e instanceof Error ? e.message : "prune failed"),
  });

  return (
    <Card title={t("adminauditlist.integrity_retention")} size="small" style={{ marginBottom: 16 }}>
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <div>
          <Space wrap>
            <Button loading={verify.isPending} onClick={() => verify.mutate()}>
              Verify hash chain
            </Button>
            <Typography.Text type="secondary">
              Recomputes the tamper-evidence chain over every audit row (read-only).
            </Typography.Text>
          </Space>
          {verifyResult && (
            <div style={{ marginTop: 8 }}>
              {verifyResult.ok ? (
                <Alert
                  type="success"
                  showIcon
                  message={`Chain OK — ${verifyResult.checked} sealed row(s) verified (${verifyResult.total} total).`}
                />
              ) : (
                <Alert
                  type="error"
                  showIcon
                  message={t("adminauditlist.chain_broken_tamper_detected")}
                  description={
                    <span>
                      First broken row after {verifyResult.checked} verified:{" "}
                      <code>{verifyResult.broken_id}</code>
                    </span>
                  }
                />
              )}
            </div>
          )}
        </div>

        <div>
          <Space wrap align="center">
            <Typography.Text>Delete audit rows older than</Typography.Text>
            <InputNumber
              min={1}
              max={3650}
              value={days}
              onChange={(v) => setDays(typeof v === "number" ? v : 365)}
            />
            <Typography.Text>days</Typography.Text>
            <Popconfirm
              title={`Prune audit rows older than ${days} days?`}
              description={t("adminauditlist.this_permanently_deletes_old_audit_records_t")}
              okText={t("adminauditlist.prune")}
              okButtonProps={{ danger: true }}
              onConfirm={() => prune.mutate()}
            >
              <Button danger loading={prune.isPending}>
                Prune
              </Button>
            </Popconfirm>
          </Space>
        </div>
      </Space>
    </Card>
  );
};

export const AdminAuditList = () => {
  const { t } = useTranslation();
  const tz = useServerTz();
  const query = useTableURL<AuditRow>({
    resource: "admin/audit",
    defaultSort: "ts",
    defaultOrder: "desc",
  });

  return (
    <div>
      <AuditMaintenanceCard />
      <Card>
        <SearchableTableStringQ<AuditRow>
          rowKey="id"
          loading={query.isLoading}
          dataSource={query.items}
          initialSearch={query.params.q}
          searchPlaceholder="Search action / target / actor / result"
          onSearchChange={(q) => query.setParams({ q, page: 1 })}
          pagination={{
            current: query.params.page,
            pageSize: query.params.pageSize,
            total: query.total,
            showSizeChanger: true,
            // GH #232: controlled pagination needs onChange or page/size
            // clicks are inert (current/pageSize are pinned to query state).
            onChange: (page, pageSize) => query.setParams({ page, pageSize }),
          }}
          scroll={{ x: "max-content" }}
          expandable={{
            expandedRowRender: (r: AuditRow) => (
              <AuditDetail row={r} tz={tz} />
            ),
          }}
        >
          <Table.Column
            dataIndex="ts"
            title={`When (${tz})`}
            key="ts"
            sorter={{ multiple: 1 }}
            defaultSortOrder="descend"
            render={(ts: string) => <code>{fmtTSInTz(ts, tz)}</code>}
          />
          <Table.Column
            title={t("adminauditlist.actor")}
            key="actor"
            render={(_: unknown, r: AuditRow) => (
              <span>
                <Tag>{r.actor_kind}</Tag>
                {r.actor_name ? (
                  r.actor_name
                ) : r.actor_user_id ? (
                  <code>{r.actor_user_id}</code>
                ) : null}
              </span>
            )}
          />
          <Table.Column
            title={t("adminauditlist.action")}
            key="action"
            sorter={{ multiple: 1 }}
            render={(_: unknown, r: AuditRow) => <AuditActionLabel row={r} />}
          />
          <Table.Column
            title={t("adminauditlist.target")}
            key="target"
            render={(_: unknown, r: AuditRow) =>
              r.target_type || r.target_id ? (
                <code>
                  {r.target_type}/{r.target_id}
                </code>
              ) : (
                dash
              )
            }
          />
          <Table.Column
            dataIndex="result"
            title={t("adminauditlist.result")}
            key="result"
            sorter={{ multiple: 1 }}
            render={resultTag}
          />
        </SearchableTableStringQ>
      </Card>
    </div>
  );
};
