// DomainLogsPanel — the single-domain web-log viewer (JAB-296 / GH #1543). The
// admin DomainLogsTab and the tenant UserLogsPage both render a TABLE of
// domains whose rows open a stream; this is the one-domain counterpart, used by
// the tenant Web Domain page's Logs tab where the domain is already fixed by the
// route. It offers the three streams (Access / Error / Real Time) as direct
// buttons rather than a one-row table, and reuses the neutral stream lifecycle
// (useDomainLogStreams) and modal so the create/close contract — and its AC2
// guarantee that a per-domain request always carries the domain id — is shared,
// not re-implemented.
//
// Audience-neutral by design (props are just { domainId }): the admin Edit
// Domain page has no per-domain Logs tab either and would render exactly this.
import type { ReactNode } from "react";
import { Button, Space, Typography } from "antd";
import {
  DashboardOutlined,
  FileTextOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { LogStreamModal } from "../LogStreamModal";
import { LOG_STREAM_LABELS, type LogType } from "./domainLogStreams";
import { useDomainLogStreams } from "./useDomainLogStreams";

// The three streams, in the order the domain tables present them. Each opens
// with the domain id bound, so the request is always per-domain (never the
// admin aggregate).
const STREAMS: { logType: LogType; icon: ReactNode }[] = [
  { logType: "access", icon: <FileTextOutlined /> },
  { logType: "error", icon: <WarningOutlined /> },
  { logType: "goaccess", icon: <DashboardOutlined /> },
];

export interface DomainLogsPanelProps {
  domainId: string;
}

export const DomainLogsPanel = ({ domainId }: DomainLogsPanelProps) => {
  const streams = useDomainLogStreams();

  return (
    <>
      <Typography.Paragraph type="secondary">
        Live tail of this domain's web logs. Access and Error stream the raw nginx
        logs; Real Time opens the GoAccess dashboard.
      </Typography.Paragraph>
      <Space wrap>
        {STREAMS.map(({ logType, icon }) => (
          <Button
            key={logType}
            icon={icon}
            onClick={() => streams.openStream(logType, domainId)}
          >
            {LOG_STREAM_LABELS[logType]}
          </Button>
        ))}
      </Space>
      <LogStreamModal {...streams.modalProps} />
    </>
  );
};
