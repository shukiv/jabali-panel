// AdminSecurityAide — admin Security tab "AIDE" sub-tab (M42, ADR-0087).
// Read-only FIM status + manual recheck trigger.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { apiClient } from "../../../apiClient";
import { Alert, Badge, Button, Card, Popconfirm, Select, Space, Statistic, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import {
  type AideSampleRow,
  useAideStatus,
  useRunAideCheck,
  useRunAideRebuild,
} from "../../../hooks/useSecurityAide";

const CATEGORY_COLOR: Record<string, string> = {
  "security-control": "red",
  unknown: "volcano",
  "jabali-managed": "blue",
  certificate: "purple",
  "system-package": "default",
  "log-state": "default",
  "user-content": "green",
};
const CHANGE_COLOR: Record<AideSampleRow["change_type"], string> = {
  added: "green",
  changed: "orange",
  removed: "red",
};

function humanizeAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export const AdminSecurityAide = () => {
  const { t } = useTranslation();
  const { data, isLoading, refetch } = useAideStatus();
  const [aideCatFilter, setAideCatFilter] = useState<string | undefined>(undefined);
  const [aideTypeFilter, setAideTypeFilter] = useState<string | undefined>(undefined);
  const runCheck = useRunAideCheck();
  const runRebuild = useRunAideRebuild();

  if (isLoading) {
    return (
      <Card title={t("adminsecurityaide.aide_file_integrity_monitor")} size="small">
        <Typography.Text type="secondary">Loading…</Typography.Text>
      </Card>
    );
  }

  if (!data?.enabled) {
    return (
      <Card title={t("adminsecurityaide.aide_file_integrity_monitor")} size="small">
        <Alert
          type="warning"
          showIcon
          message={t("adminsecurityaide.aide_not_active")}
          description={data?.reason || "AIDE not installed or DB missing. install_aide() runs on jabali update."}
        />
      </Card>
    );
  }

  const total = data.summary.added + data.summary.changed + data.summary.removed;

  return (
    <Card
      title={t("adminsecurityaide.aide_file_integrity_monitor")}
      size="small"
      extra={
        <Space>
          <Button size="small" onClick={() => refetch()}>
            Refresh
          </Button>
          <Button
            size="small"
            onClick={async () => {
              // GH #714: full-diff export — download the raw AIDE report.
              try {
                const { data } = await apiClient.get<{ report: string; available: boolean }>(
                  "/admin/security/aide/report",
                );
                if (!data.available || !data.report) {
                  void feedback.message.warning("No AIDE report available yet");
                  return;
                }
                const blob = new Blob([data.report], { type: "text/plain" });
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = `aide-report-${new Date().toISOString().slice(0, 10)}.txt`;
                a.click();
                URL.revokeObjectURL(url);
              } catch {
                void feedback.message.error("Report download failed");
              }
            }}
          >
            Download full diff
          </Button>
          <Button
            size="small"
            type="primary"
            loading={runCheck.isPending}
            onClick={() =>
              runCheck.mutate(undefined, {
                onSuccess: () => feedback.message.success("Check complete"),
                onError: () => feedback.message.error("Check failed — see agent logs"),
              })
            }
          >
            Run check now
          </Button>
          <Button
            size="small"
            loading={runRebuild.isPending}
            onClick={() =>
              runRebuild.mutate(true, {
                onSuccess: (d) => feedback.message.info(`Dry run: would run ${d.plan ?? "aideinit -y -f"}`),
                onError: () => feedback.message.error("Dry run failed — see agent logs"),
              })
            }
          >
            Rebuild (dry run)
          </Button>
          <Popconfirm
            title={t("adminsecurityaide.rebuild_the_aide_baseline")}
            description={t("adminsecurityaide.this_replaces_the_integrity_baseline_with_th")}
            okText={t("adminsecurityaide.rebuild")}
            okButtonProps={{ danger: true }}
            onConfirm={() =>
              runRebuild.mutate(false, {
                onSuccess: () => feedback.message.success("Baseline rebuilt"),
                onError: () => feedback.message.error("Rebuild failed — see agent logs"),
              })
            }
          >
            <Button size="small" danger loading={runRebuild.isPending}>
              Rebuild baseline
            </Button>
          </Popconfirm>
        </Space>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Daily SHA-256 hash check on system binaries + configs
        (<code>/bin /sbin /usr/bin /usr/sbin /lib /etc /boot /root</code>).
        Excludes paths the panel writes to (
        <code>/etc/jabali/</code>, <code>/etc/letsencrypt/live/</code>,
        reconciler-managed configs). Daily timer
        <code>jabali-aide-check.timer</code> runs at 04:30 UTC + jitter.
      </Typography.Paragraph>

      <Space size="large" style={{ marginBottom: 16 }}>
        <Statistic title={t("adminsecurityaide.db_age")} value={humanizeAge(data.db_age_seconds)} />
        <Statistic title={t("adminsecurityaide.last_check")} value={data.last_check_ts || "—"} />
        <Statistic title={t("adminsecurityaide.added")} value={data.summary.added} />
        <Statistic title={t("adminsecurityaide.changed")} value={data.summary.changed} />
        <Statistic title={t("adminsecurityaide.removed")} value={data.summary.removed} />
      </Space>

      {total > 0 ? (
        <Alert
          type="warning"
          showIcon
          message={`${total} file(s) changed since the last baseline`}
          description={t("adminsecurityaide.review_the_sample_below_if_the_changes_are_e")}
          style={{ marginBottom: 12 }}
        />
      ) : (
        <Alert
          type="success"
          showIcon
          message={t("adminsecurityaide.no_tamper_detected")}
          description={t("adminsecurityaide.all_watched_paths_match_the_baseline_checksu")}
          style={{ marginBottom: 12 }}
        />
      )}

      {data.categories && Object.keys(data.categories).length > 0 && (
        <div style={{ marginBottom: 16 }}>
          <Typography.Text strong>Changes by source (GH #714): </Typography.Text>
          <Space wrap style={{ marginTop: 8 }}>
            {Object.entries(data.categories)
              .sort((a, b) => b[1] - a[1])
              .map(([cat, n]) => (
                <Tag key={cat} color={CATEGORY_COLOR[cat] ?? "default"}>
                  {cat}: {n}
                </Tag>
              ))}
          </Space>
          {(data.categories["unknown"] || data.categories["security-control"]) ? (
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 12 }}
              message={t("adminsecurityaide.prioritize_these")}
              description="`security-control` and `unknown/unowned` changes deserve scrutiny before re-baselining — the rest is usually package/kernel churn."
            />
          ) : null}
        </div>
      )}
      <Space wrap style={{ marginBottom: 12 }}>
        <Select
          allowClear
          placeholder={t("adminsecurityaide.category")}
          style={{ width: 180 }}
          value={aideCatFilter}
          onChange={setAideCatFilter}
          options={Object.keys(data.categories ?? {}).map((c) => ({ label: c, value: c }))}
        />
        <Select
          allowClear
          placeholder={t("adminsecurityaide.change_type")}
          style={{ width: 150 }}
          value={aideTypeFilter}
          onChange={setAideTypeFilter}
          options={["added", "changed", "removed"].map((c) => ({ label: c, value: c }))}
        />
      </Space>
      <Table
        expandable={{
          // GH #714: per-file old/new metadata (sha/size/perm/mtime...) when the
          // AIDE report carries the detail section. rowExpandable hides the
          // chevron for rows without metadata.
          rowExpandable: (r) => (r.meta?.length ?? 0) > 0,
          expandedRowRender: (r) => (
            <Table
              size="small"
              pagination={false}
              rowKey="attr"
              dataSource={r.meta ?? []}
              columns={[
                { title: "Attribute", dataIndex: "attr", width: 120 },
                { title: "Old", dataIndex: "old", ellipsis: true, render: (v: string) => <code>{v || "—"}</code> },
                { title: "New", dataIndex: "new", ellipsis: true, render: (v: string) => <code>{v || "—"}</code> },
              ]}
            />
          ),
        }}
        rowKey={(r, idx) => `${r.change_type}-${r.path}-${idx}`}
        dataSource={data.sample.filter(
          (r) =>
            (!aideCatFilter || r.category === aideCatFilter) &&
            (!aideTypeFilter || r.change_type === aideTypeFilter),
        )}
        size="small"
        tableLayout="fixed"
        scroll={{ x: "max-content" }}
        pagination={{ pageSize: 25 }}
        columns={[
          {
            title: "Change",
            dataIndex: "change_type",
            width: 110,
            render: (v: AideSampleRow["change_type"]) => (
              <Tag color={CHANGE_COLOR[v]}>{v}</Tag>
            ),
          },
          {
            title: "Category",
            dataIndex: "category",
            width: 150,
            render: (v: string) => <Tag color={CATEGORY_COLOR[v] ?? "default"}>{v || "?"}</Tag>,
          },
          {
            title: "Path",
            dataIndex: "path",
            ellipsis: { showTitle: false } as const,
            render: (v: string) => (
              <Tooltip title={v}>
                <code>{v}</code>
              </Tooltip>
            ),
          },
        ]}
      />

      <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
        <Badge status="processing" /> Re-baseline after a deliberate change with{" "}
        <code>jabali aide rebuild --full</code>. <code>jabali update</code>{" "}
        re-baselines panel binaries automatically.
      </Typography.Paragraph>
    </Card>
  );
};
