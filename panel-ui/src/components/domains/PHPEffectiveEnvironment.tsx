// PHPEffectiveEnvironment — GH #1701. The read-only part of a domain's PHP
// Settings page: what the PHP pool serving the domain really runs with. The
// disabled functions (from the hosting package or the server's php.ini), what
// PHP Defense bans there, and include_path / session.save_path. The agent reads
// them from the files the pool loads, so this shows the configuration in force,
// not what the package editor meant to write. Shown to the admin and the domain
// owner alike; nothing here is editable.
import { useState } from "react";
import type { ReactNode } from "react";
import { Alert, Collapse, Descriptions, Spin, Table, Tag, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../apiClient";
import { functionRows, type EffectiveIni, type FunctionRow, type PHPEffective } from "./phpEffective";

const STATUS_TAG: Record<FunctionRow["status"], { color?: string; text: string }> = {
  disabled_package: { text: "Disabled by the hosting package" },
  disabled_server: { text: "Disabled server-wide (php.ini)" },
  blocked_defense: { color: "red", text: "Blocked by PHP Defense" },
  logged_defense: { color: "gold", text: "Allowed, logged by PHP Defense" },
  allowed: { color: "green", text: "Allowed" },
  allowed_unavailable: { text: "Allowed, not in this PHP build" },
};

function defenseNote(e: PHPEffective): string {
  const pd = e.php_defense;
  if (!pd.active) return `PHP Defense is not active for PHP ${e.php_version}.`;
  if (pd.mode === "off") return "PHP Defense is off.";
  let note =
    pd.mode === "simulation"
      ? "PHP Defense is in simulation mode: it logs the calls it would block but lets them run."
      : "PHP Defense is enforcing its rules.";
  if (pd.pool_rules) note += " This site's hosting package lifts its ban on the functions the package allows.";
  return note;
}

function iniValue(v: EffectiveIni, emptyText: string) {
  return (
    <>
      {v.value === "" ? <Typography.Text type="secondary">{emptyText}</Typography.Text> : <code>{v.value}</code>}{" "}
      <Tag>{v.source === "pool" ? "PHP pool setting" : "Server php.ini"}</Tag>
    </>
  );
}

export function PHPEffectiveEnvironment({ domainId }: { domainId: string }) {
  const [open, setOpen] = useState(false);
  const q = useQuery({
    queryKey: ["domain-php-effective", domainId],
    queryFn: async () => (await apiClient.get<PHPEffective>(`/domains/${domainId}/php-settings/effective`)).data,
    enabled: open,
    staleTime: 15_000,
  });

  let body: ReactNode;
  if (q.isLoading) {
    body = <Spin />;
  } else if (q.isError || !q.data) {
    body = <Alert type="error" showIcon message="Could not read this domain's PHP environment." />;
  } else if (!q.data.pool_found) {
    body = <Alert type="info" showIcon message="This domain's PHP pool has not been set up yet." />;
  } else {
    const e = q.data;
    const rows = functionRows(e);
    body = (
      <>
        {e.ini_read_error && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="The server's php.ini could not be read, so server-wide values are missing."
          />
        )}
        {e.availability_error && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="Could not check which functions this PHP build provides, so a function shown as Allowed may still be missing."
          />
        )}
        <Typography.Title level={5} style={{ marginTop: 0 }}>
          PHP functions
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {defenseNote(e)} The hosting package decides which functions are disabled; ask your
          administrator to change it.
        </Typography.Paragraph>
        <Table<FunctionRow>
          size="small"
          rowKey="name"
          pagination={false}
          scroll={{ x: "max-content" }}
          dataSource={rows}
          columns={[
            { title: "Function", dataIndex: "name", render: (n: string) => <code>{n}</code> },
            {
              title: "Status",
              dataIndex: "status",
              render: (s: FunctionRow["status"]) => <Tag color={STATUS_TAG[s].color}>{STATUS_TAG[s].text}</Tag>,
            },
          ]}
        />
        {rows.some((r) => r.status === "allowed_unavailable") && (
          <Typography.Paragraph type="secondary" style={{ marginTop: 8 }}>
            "Allowed, not in this PHP build" means the hosting package allows the function, but PHP{" "}
            {e.php_version} for websites (PHP-FPM) does not include it. The pcntl_ functions need the
            pcntl extension, which is often built into PHP's command-line version only, and dl() exists
            only in the command-line version. If a later PHP build includes the function, it becomes
            available with no change to the package.
          </Typography.Paragraph>
        )}
        <Typography.Title level={5}>Paths</Typography.Title>
        <Descriptions size="small" column={1} bordered>
          <Descriptions.Item label="include_path">{iniValue(e.include_path, "(empty)")}</Descriptions.Item>
          <Descriptions.Item label="session.save_path">
            {iniValue(e.session_save_path, "(empty: PHP's temp folder)")}
          </Descriptions.Item>
        </Descriptions>
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
          These apply to every site on this PHP pool. An administrator sets them per pool, under PHP
          Pools, ini overrides. An app can still change them for itself at run time.
        </Typography.Paragraph>
      </>
    );
  }

  return (
    <Collapse
      style={{ marginTop: 16 }}
      activeKey={open ? ["effective"] : []}
      onChange={(keys) => setOpen((Array.isArray(keys) ? keys : [keys]).includes("effective"))}
      items={[
        {
          key: "effective",
          label: "Disabled functions and paths",
          extra: <Tag>Read-only</Tag>,
          children: body,
        },
      ]}
    />
  );
}
