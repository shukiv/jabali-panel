// AdminSecuritySnuffleupagus — Security tab card for M41 Snuffleupagus.
// Wave D — full surface: mode toggle, php-version load matrix, recent
// incidents table, per-rule kill switch.
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Descriptions, Drawer, Button, Card, Input, Modal, Popconfirm, Radio, Space, Switch, Table, Tag, Tooltip, Typography } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts

import {
  type SnuffleupagusIncident,
  type SnuffleupagusMode,
  type SnuffleupagusRule,
  useSetSnuffleupagusMode,
  useSnuffleupagusIncidents,
  useSnuffleupagusRules,
  useSnuffleupagusStatus,
  useToggleSnuffleupagusRule,
} from "../../../hooks/useSecuritySnuffleupagus";

const { Text, Paragraph } = Typography;

const MODE_COLOR: Record<SnuffleupagusMode, string> = {
  off: "default",
  simulation: "warning",
  enforce: "success",
};

const ACTION_COLOR: Record<string, string> = {
  block: "error",
  simulated_block: "warning",
  log: "default",
};

export function AdminSecuritySnuffleupagus() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const status = useSnuffleupagusStatus();
  const incidents = useSnuffleupagusIncidents({ limit: 50 });
  const rules = useSnuffleupagusRules();
  const [detailIncident, setDetailIncident] = useState<SnuffleupagusIncident | null>(null); // GH #717
  const [disabling, setDisabling] = useState<{ name: string; reason: string } | null>(null); // GH #717 disable-reason
  const setMode = useSetSnuffleupagusMode();
  const toggleRule = useToggleSnuffleupagusRule();

  const [rulesOpen, setRulesOpen] = useState(false);

  if (status.isLoading) {
    return (
      <Card title={t("adminsecuritysnuffleupagus.php_defense")} size="small">
        <Text type="secondary">Loading…</Text>
      </Card>
    );
  }

  const data = status.data;
  const loadedCount = data?.php_versions_loaded.filter((v) => v.loaded).length ?? 0;
  const totalCount = data?.php_versions_loaded.length ?? 0;

  return (
    <Card
      title={t("adminsecuritysnuffleupagus.php_defense")}
      size="small"
      extra={
        <Space>
          {data && <Tag color={MODE_COLOR[data.mode]}>{data.mode}</Tag>}
          <a
            onClick={() => {
              void status.refetch();
              void incidents.refetch();
            }}
          >
            Refresh
          </a>
        </Space>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <Paragraph type="secondary" style={{ marginBottom: 0 }}>
          PHP Defense (Snuffleupagus) across {loadedCount}/{totalCount} installed PHP minor
          {totalCount === 1 ? "" : "s"}. Rules are maintained by Jabali; mode applies server-wide.
          {data?.last_applied_at && (
            <>
              <br />
              <Text type="secondary">
                Last applied: {new Date(data.last_applied_at).toLocaleString()}
              </Text>
            </>
          )}
        </Paragraph>

        <Space wrap>
          {data?.php_versions_loaded.map((v) => (
            <Tooltip key={v.minor} title={v.extension_so}>
              <Tag color={v.loaded ? "success" : "default"}>
                PHP {v.minor} {v.loaded ? "✓" : "—"}
              </Tag>
            </Tooltip>
          ))}
        </Space>

        <div>
          <Text strong style={{ marginRight: 12 }}>
            Mode:
          </Text>
          <Radio.Group
            value={data?.mode ?? "off"}
            onChange={(e) => {
              const next = e.target.value as SnuffleupagusMode;
              if (next === "enforce") {
                feedback.modal.confirm({
                  title: "Switch PHP Defense to enforce mode?",
                  content:
                    "Tenant requests that match a hardening rule will be blocked. Run a soak in simulation first if you haven't already.",
                  okText: "Enforce",
                  okType: "danger",
                  onOk: () =>
                    setMode.mutateAsync(next).then(() => {
                      void feedback.message.success("Mode set to enforce");
                      void qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] });
                    }),
                });
                return;
              }
              setMode.mutateAsync(next).then(() => {
                void feedback.message.success(`Mode set to ${next}`);
                void qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] });
              });
            }}
            disabled={setMode.isPending}
          >
            <Radio.Button value="off">Off</Radio.Button>
            <Radio.Button value="simulation">Simulation</Radio.Button>
            <Radio.Button value="enforce">Enforce</Radio.Button>
          </Radio.Group>
          <Button type="link" onClick={() => setRulesOpen(true)} style={{ marginLeft: 12 }}>
            Manage rules ({rules.data?.length ?? 0})
          </Button>
        </div>

        {data?.mode === "simulation" && (
          <Alert
            type="info"
            showIcon
            message={t("adminsecuritysnuffleupagus.simulation_mode_active")}
            description={t("adminsecuritysnuffleupagus.rules_log_incidents_below_without_blocking_s")}
          />
        )}

        <div>
          <Text strong>Recent incidents</Text>
          <Table<SnuffleupagusIncident>
            size="small"
            rowKey="id"
            loading={incidents.isLoading}
            dataSource={incidents.data?.data ?? []}
            pagination={{
              total: incidents.data?.total ?? 0,
              defaultPageSize: 50,
            }}
            columns={[
              {
                title: "When",
                dataIndex: "ts",
                width: 160,
                render: (ts: string) => new Date(ts).toLocaleString(),
              },
              {
                title: "Action",
                dataIndex: "action",
                width: 130,
                render: (a: string) => <Tag color={ACTION_COLOR[a] ?? "default"}>{a}</Tag>,
              },
              { title: "Rule", dataIndex: "rule_name", ellipsis: true },
              {
                title: "PHP",
                dataIndex: "php_version",
                width: 70,
                render: (v?: string) => v || <Text type="secondary">-</Text>,
              },
              {
                title: "Source",
                dataIndex: "source_ip",
                width: 140,
                render: (v?: string) =>
                  v && v !== "0.0.0.0" ? v : <Text type="secondary">CLI</Text>,
              },
              {
                title: "URI",
                dataIndex: "request_uri",
                ellipsis: true,
                render: (v?: string) => v || <Text type="secondary">-</Text>,
              },
            ]}
            scroll={{ x: "max-content" }}
            locale={{ emptyText: "No incidents yet" }}
          />
        </div>
      </Space>

      <Modal
        title={t("adminsecuritysnuffleupagus.php_defense_rules")}
        open={rulesOpen}
        onCancel={() => setRulesOpen(false)}
        footer={<Button onClick={() => setRulesOpen(false)}>Close</Button>}
        width={900}
      >
        <Paragraph type="secondary">
          Toggle a rule off when triaging a false positive. Disabled rules are excluded from the
          active.rules render on the next reconcile.
        </Paragraph>
        <Table<SnuffleupagusRule>
          size="small"
          rowKey="name"
          loading={rules.isLoading}
          dataSource={rules.data ?? []}
          pagination={{ defaultPageSize: 25 }}
          columns={[
            { title: "Rule", dataIndex: "name", ellipsis: true },
            {
              // GH #717: enforcement-readiness signal — how noisy this rule is,
              // from the loaded incident window. Quiet -> safe; many -> review
              // for false positives before enforcing / disabling.
              title: "Readiness",
              key: "readiness",
              width: 130,
              render: (_: unknown, row: SnuffleupagusRule) => {
                const n = (incidents.data?.data ?? []).filter((i) => i.rule_name === row.name).length;
                const color = n === 0 ? "green" : n < 5 ? "orange" : "red";
                return <Tag color={color}>{n === 0 ? "quiet" : `${n} incident${n > 1 ? "s" : ""}`}</Tag>;
              },
            },
            { title: "Source", dataIndex: "source_file", width: 180 },
            {
              title: "Enabled",
              dataIndex: "enabled",
              width: 110,
              render: (_: boolean, row: SnuffleupagusRule) =>
                row.enabled ? (
                  // GH #717: disabling a rule requires a reason (audited via the
                  // override table). Open a prompt instead of a bare confirm.
                  <Switch
                    checked
                    loading={toggleRule.isPending}
                    onChange={() => setDisabling({ name: row.name, reason: "" })}
                  />
                ) : (
                  <Popconfirm
                    title={t("adminsecuritysnuffleupagus.re_enable_this_rule")}
                    onConfirm={() =>
                      toggleRule
                        .mutateAsync({ name: row.name, enabled: true })
                        .then(() => {
                          void feedback.message.success("Rule enabled");
                          void qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] });
                        })
                    }
                  >
                    <Switch checked={false} loading={toggleRule.isPending} />
                  </Popconfirm>
                ),
            },
            { title: "Reason", dataIndex: "reason", ellipsis: true },
          ]}
          scroll={{ x: "max-content" }}
        />
      </Modal>
      <Drawer
        title={detailIncident ? `Incident #${detailIncident.id}` : ""}
        width={620}
        open={!!detailIncident}
        onClose={() => setDetailIncident(null)}
      >
        {detailIncident ? (
          <>
            <Descriptions column={1} size="small" bordered>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.when")}>{new Date(detailIncident.ts).toLocaleString()}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.action")}>{detailIncident.action}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.rule")}>{detailIncident.rule_name}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.domain_tenant")}>{detailIncident.domain || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.request_uri")}>{detailIncident.request_uri || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.source_ip")}>{detailIncident.source_ip || "—"}</Descriptions.Item>
              <Descriptions.Item label={t("adminsecuritysnuffleupagus.php_version")}>{detailIncident.php_version || "—"}</Descriptions.Item>
            </Descriptions>
            {(() => {
              // GH #717: per-rule incident history from the loaded window — how
              // often this rule fired, so you can judge false-positive vs real.
              const hist = (incidents.data?.data ?? []).filter(
                (i) => i.rule_name === detailIncident.rule_name,
              );
              return (
                <>
                  <Typography.Title level={5} style={{ marginTop: 16 }}>
                    History for this rule ({hist.length} in the loaded window)
                  </Typography.Title>
                  <Table<SnuffleupagusIncident>
                    size="small"
                    pagination={false}
                    scroll={{ x: "max-content" }}
                    rowKey="id"
                    dataSource={hist}
                    columns={[
                      { title: "When", dataIndex: "ts", width: 170, render: (t: string) => new Date(t).toLocaleString() },
                      { title: "Action", dataIndex: "action", width: 110, render: (a: string) => <Tag color={ACTION_COLOR[a] ?? "default"}>{a}</Tag> },
                      { title: "Domain", dataIndex: "domain", render: (v?: string) => v || "—" },
                      { title: "URI", dataIndex: "request_uri", ellipsis: true, render: (v?: string) => v || "—" },
                    ]}
                  />
                </>
              );
            })()}
            {(() => {
              const r = (rules.data ?? []).find((x) => x.name === detailIncident.rule_name);
              return (
                <Alert
                  style={{ marginTop: 12 }}
                  type={detailIncident.action === "block" ? "error" : "info"}
                  showIcon
                  message={detailIncident.action === "block" ? "This request was BLOCKED" : "Logged only (not blocked)"}
                  description={
                    r
                      ? `Rule '${r.name}' is from ${r.source_file} and is currently ${r.enabled ? "ENABLED" : "DISABLED"}. ${r.reason ? r.reason + " " : ""}Before disabling a rule for a false positive, confirm the request URI/tenant above is legitimate — a block on a webshell-drop or exec sink is usually a real hit, not a false positive.`
                      : "Rule metadata not found in the current rule set (it may be from a base policy). Review the request URI + tenant before treating it as a false positive."
                  }
                />
              );
            })()}
          </>
        ) : null}
      </Drawer>
      <Modal
        title={t("adminsecuritysnuffleupagus.disable_rule_reason_required")}
        open={disabling !== null}
        okText={t("adminsecuritysnuffleupagus.disable")}
        okButtonProps={{ danger: true, disabled: !disabling?.reason.trim() }}
        onCancel={() => setDisabling(null)}
        onOk={() => {
          if (!disabling) return;
          void toggleRule
            .mutateAsync({ name: disabling.name, enabled: false, reason: disabling.reason.trim() })
            .then(() => {
              void feedback.message.success("Rule disabled");
              void qc.invalidateQueries({ queryKey: ["security", "snuffleupagus"] });
              setDisabling(null);
            });
        }}
      >
        <Typography.Paragraph type="secondary">
          Disabling <code>{disabling?.name}</code>. A reason is recorded (who/when/why) for the audit trail.
        </Typography.Paragraph>
        <Input.TextArea
          rows={2}
          placeholder="e.g. false positive on tenant X's plugin update"
          value={disabling?.reason ?? ""}
          onChange={(e) => setDisabling((d) => (d ? { ...d, reason: e.target.value } : d))}
        />
      </Modal>
    </Card>
  );
}
