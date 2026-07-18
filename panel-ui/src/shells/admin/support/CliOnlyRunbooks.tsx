// CliOnlyRunbooks — JAB-14. A read-only reference of the `jabali …` subcommands
// that have no GUI affordance (escape hatches, recovery, one-time migrations,
// key-material ops). Per docs/operations/cli-only-commands.md, the admin UI
// should surface WHERE the recovery levers live, not grow one-click buttons for
// destructive ops — so this is a table of copyable commands + risk, never an
// action control. The list is sourced from config/cli-only-runbooks.ts and a
// drift test keeps it in sync with the doc.
import { Card, Space, Table, Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import { CodeOutlined, ExportOutlined, WarningOutlined } from "@icons";

import {
  ALL_CLI_ONLY_RUNBOOKS,
  CLI_ONLY_DOC_URL,
  RISK_COLORS,
  RISK_LABELS,
  type CliRunbook,
} from "../../../config/cli-only-runbooks";

const CATEGORY_LABELS: Record<CliRunbook["category"], string> = {
  "escape-hatch": "Escape hatch",
  "gui-candidate": "GUI candidate",
};

const columns: ColumnsType<CliRunbook> = [
  {
    title: "Command",
    dataIndex: "command",
    key: "command",
    render: (command: string) => (
      <Typography.Text
        code
        copyable={{ text: `jabali ${command}` }}
        style={{ whiteSpace: "nowrap" }}
      >
        jabali {command}
      </Typography.Text>
    ),
  },
  {
    title: "Risk",
    dataIndex: "risk",
    key: "risk",
    filters: Object.entries(RISK_LABELS).map(([value, text]) => ({ text, value })),
    onFilter: (value, rb) => rb.risk === value,
    render: (risk: CliRunbook["risk"]) => (
      <Tag color={RISK_COLORS[risk]}>{RISK_LABELS[risk]}</Tag>
    ),
  },
  {
    title: "Dry-run",
    dataIndex: "dryRun",
    key: "dryRun",
    render: (dryRun: boolean) =>
      dryRun ? (
        <Tag color="green">--dry-run</Tag>
      ) : (
        <Typography.Text type="secondary">—</Typography.Text>
      ),
  },
  {
    title: "Category",
    dataIndex: "category",
    key: "category",
    filters: Object.entries(CATEGORY_LABELS).map(([value, text]) => ({ text, value })),
    onFilter: (value, rb) => rb.category === value,
    render: (category: CliRunbook["category"]) => (
      <Tag color={category === "gui-candidate" ? "cyan" : "default"}>
        {CATEGORY_LABELS[category]}
      </Tag>
    ),
  },
  {
    title: "Purpose",
    dataIndex: "summary",
    key: "summary",
    render: (summary: string, rb: CliRunbook) => (
      <Space direction="vertical" size={2}>
        <Typography.Text>{summary}</Typography.Text>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          <CodeOutlined /> {rb.file}
          {rb.adr ? ` · ${rb.adr}` : ""}
        </Typography.Text>
      </Space>
    ),
  },
];

export const CliOnlyRunbooks = () => (
  <Card
    style={{ marginTop: 24 }}
    title={
      <Space>
        <WarningOutlined style={{ color: "#faad14" }} />
        <span>CLI-only recovery runbooks</span>
      </Space>
    }
    extra={
      <Typography.Link
        href={CLI_ONLY_DOC_URL}
        target="_blank"
        rel="noopener noreferrer"
      >
        Full doc <ExportOutlined />
      </Typography.Link>
    }
  >
    <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
      These operations run only from a root shell on the host. Most are
      deliberate escape hatches — destructive, one-time migrations, or
      key-material ops that don't belong behind a click. Run any{" "}
      <Tooltip title="Preview the effect before it mutates anything">
        <Tag color="green">--dry-run</Tag>
      </Tooltip>{" "}
      command first, and read the full doc for prerequisites and rollback notes.
    </Typography.Paragraph>
    <Table<CliRunbook>
      rowKey="command"
      size="small"
      columns={columns}
      dataSource={[...ALL_CLI_ONLY_RUNBOOKS]}
      pagination={false}
      scroll={{ x: "max-content" }}
    />
  </Card>
);
