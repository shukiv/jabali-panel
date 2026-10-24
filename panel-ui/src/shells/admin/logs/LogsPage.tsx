// LogsPage — consolidated Logs & Statistics for admins. One tabbed home
// for every log surface that used to be scattered:
//   Domains  — per-domain web access/error/GoAccess streams (was this page)
//   Audit    — the admin audit log (moved off its own sidebar entry)
//   Backups  — backup job logs (reused from the Backups page)
//   Mail     — the mail trace log (admin sees all domains)
// Laid out like Security and Backups (GH #2251): the heading with the
// sidebar's icon, then one card whose tab bar holds the log surfaces.
import { useTabParam } from "../../../hooks/useTabParam";
import { Card, Space, Typography } from "antd";
import type { ReactNode } from "react";
import {
  DownloadOutlined,
  FileTextOutlined,
  GlobalOutlined,
  MailOutlined,
  SafetyCertificateOutlined,
  SaveOutlined,
} from "@icons";

import { DomainLogsTab } from "./DomainLogsTab";
import { AdminAuditList } from "../audit/AdminAuditList";
import { BackupLogsTab } from "../backups/BackupLogsTab";
import { LogsTab as MailLogsTab } from "../../user/mail/tabs/LogsTab";
import { UpdatesLogTab } from "./UpdatesLogTab";

const { Title } = Typography;

const TABS: { key: string; label: string; icon: ReactNode; render: () => ReactNode }[] = [
  { key: "domains", label: "Domains", icon: <GlobalOutlined />, render: () => <DomainLogsTab /> },
  { key: "audit", label: "Audit Log", icon: <SafetyCertificateOutlined />, render: () => <AdminAuditList /> },
  { key: "backups", label: "Backups", icon: <SaveOutlined />, render: () => <BackupLogsTab /> },
  { key: "updates", label: "Updates", icon: <DownloadOutlined />, render: () => <UpdatesLogTab /> },
  { key: "mail", label: "Mail", icon: <MailOutlined />, render: () => <MailLogsTab /> },
];

export const LogsPage = () => {
  const [activeTab, setActiveTab] = useTabParam<string>("domains");
  // An unknown ?tab= opens the first tab rather than an empty card.
  const active = TABS.find((tab) => tab.key === activeTab) ?? TABS[0];

  return (
    <div>
      <Title level={3} style={{ marginTop: 0, marginBottom: 16 }}>
        <FileTextOutlined /> Logs & Statistics
      </Title>

      <Card
        // nowrap keeps two-word tabs on one line at phone width (as Notifications).
        tabList={TABS.map((tab) => ({
          key: tab.key,
          tab: (
            <Space size={6} style={{ whiteSpace: "nowrap" }}>
              {tab.icon}
              {tab.label}
            </Space>
          ),
        }))}
        activeTabKey={active.key}
        onTabChange={setActiveTab}
      >
        {active.render()}
      </Card>
    </div>
  );
};
