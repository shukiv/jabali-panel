// buildDomainLogColumns — the shared Domain + Actions columns for the
// per-domain log viewer (JAB-296, ADR-0083). Both the admin DomainLogsTab and
// the tenant UserLogsPage render the exact same two columns; only the request
// scope differs, and that difference is carried by the column context, not by
// each shell hand-rolling its own openStream call.
//
// AC1/AC2 live in the ctx discriminant. An AggregateColumnCtx (admin) can open
// a cross-domain stream on the synthetic aggregate row; a DomainLogColumnCtx
// (tenant) has no onOpenAggregate member, so no code path here can ever emit an
// aggregate request in the tenant scope — the capability is absent, not merely
// unused.
import { Space, Typography, type TableProps } from "antd";
import {
  FileTextOutlined,
  WarningOutlined,
  DashboardOutlined,
} from "@ant-design/icons";
import { RowActions } from "../RowActions";
import {
  type LogType,
  type DomainLogRow,
  LOG_STREAM_LABELS,
  isAggregateRow,
} from "./domainLogStreams";

const { Text } = Typography;

type DomainLogColumns = NonNullable<TableProps<DomainLogRow>["columns"]>;

// The tenant capability: open a stream for a specific domain, identity always
// included.
export interface DomainLogColumnCtx {
  onOpenDomain: (logType: LogType, domainId: string) => void;
}

// The admin capability adds the aggregate open. Its presence is what lets the
// aggregate row do anything; absence (tenant) makes an aggregate request
// unreachable.
export interface AggregateColumnCtx extends DomainLogColumnCtx {
  onOpenAggregate: (logType: LogType) => void;
}

export function buildDomainLogColumns(
  ctx: DomainLogColumnCtx | AggregateColumnCtx,
): DomainLogColumns {
  const open = (logType: LogType, record: DomainLogRow) => {
    if ("onOpenAggregate" in ctx && isAggregateRow(record)) {
      ctx.onOpenAggregate(logType);
    } else {
      ctx.onOpenDomain(logType, record.id);
    }
  };

  return [
    {
      title: "Domain",
      dataIndex: "name",
      key: "name",
      render: (name: string, record: DomainLogRow) => (
        <Space>
          <Text strong>{name}</Text>
          <Text type="secondary">({record.status})</Text>
        </Space>
      ),
    },
    {
      title: "Actions",
      key: "actions",
      render: (_: unknown, record: DomainLogRow) => (
        <RowActions
          actions={[
            {
              key: "access",
              label: LOG_STREAM_LABELS.access,
              icon: <FileTextOutlined />,
              onClick: () => open("access", record),
            },
            {
              key: "error",
              label: LOG_STREAM_LABELS.error,
              icon: <WarningOutlined />,
              onClick: () => open("error", record),
            },
            {
              key: "goaccess",
              label: LOG_STREAM_LABELS.goaccess,
              icon: <DashboardOutlined />,
              onClick: () => open("goaccess", record),
            },
          ]}
        />
      ),
    },
  ];
}
