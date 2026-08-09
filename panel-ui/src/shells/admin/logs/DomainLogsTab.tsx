// DomainLogsTab — per-domain web log streams (access / error / GoAccess
// real-time). Extracted from the old single-purpose LogsPage so it can be
// one tab of the consolidated Logs & Statistics page.
import { useState } from "react";
import { RowActions } from "../../../components/RowActions";
import { Card, Typography, Button, Space, Table } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import {
  ReloadOutlined,
  FileTextOutlined,
  WarningOutlined,
  DashboardOutlined,
} from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../../../apiClient";
import { LogStreamModal } from "./LogStreamModal";

const { Text } = Typography;

interface Domain {
  id: string;
  name: string;
  status: string;
}

type LogType = "access" | "error" | "goaccess";

const titleFor: Record<LogType, string> = {
  access: "Access Log Stream",
  error: "Error Log Stream",
  goaccess: "GoAccess Real-Time Dashboard",
};

const labelFor: Record<LogType, string> = {
  access: "Access Log",
  error: "Error Log",
  goaccess: "Real Time",
};

export const DomainLogsTab = () => {
  const [refreshTrigger, setRefreshTrigger] = useState(0);
  const [streamKey, setStreamKey] = useState<string | null>(null);
  const [streamUrl, setStreamUrl] = useState<string | null>(null);
  const [streamLogType, setStreamLogType] = useState<LogType>("access");
  const [modalVisible, setModalVisible] = useState(false);

  const { data: domainsData, isLoading } = useQuery({
    queryKey: ["domains", refreshTrigger],
    queryFn: async () => {
      const response = await apiClient.get("/domains");
      return response.data;
    },
  });

  const domains: Domain[] = domainsData?.data || [];

  const openStream = async (logType: LogType, domainId?: string) => {
    try {
      const payload: { log_type: LogType; domain_id?: string } = { log_type: logType };
      if (domainId) payload.domain_id = domainId;

      const response = await apiClient.post("/logs/access", payload);
      const { stream_key, websocket_url } = response.data;

      setStreamKey(stream_key);
      setStreamUrl(websocket_url);
      setStreamLogType(logType);
      setModalVisible(true);
    } catch (error: unknown) {
      const msg =
        error && typeof error === "object" && "response" in error
          ? // @ts-expect-error axios error shape
            error.response?.data?.error
          : undefined;
      feedback.message.error(msg || "Failed to create log stream");
    }
  };

  const handleStreamClose = async () => {
    if (streamKey) {
      try {
        await apiClient.delete(`/logs/access/${streamKey}`);
      } catch {
        // Stream may have expired server-side; harmless.
      }
      setStreamKey(null);
      setStreamUrl(null);
    }
    setModalVisible(false);
  };

  const tableData: Domain[] = [
    { id: "", name: "All Domains", status: "system" },
    ...domains,
  ];

  const columns = [
    {
      title: "Domain",
      dataIndex: "name",
      key: "name",
      render: (name: string, record: Domain) => (
        <Space>
          <Text strong>{name}</Text>
          <Text type="secondary">({record.status})</Text>
        </Space>
      ),
    },
    {
      title: "Actions",
      key: "actions",
      render: (_: unknown, record: Domain) => (
        <RowActions
          actions={[
            { key: "access", label: labelFor.access, icon: <FileTextOutlined />, onClick: () => openStream("access", record.id || undefined) },
            { key: "error", label: labelFor.error, icon: <WarningOutlined />, onClick: () => openStream("error", record.id || undefined) },
            { key: "goaccess", label: labelFor.goaccess, icon: <DashboardOutlined />, onClick: () => openStream("goaccess", record.id || undefined) },
          ]}
        />
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 16, width: "100%", justifyContent: "flex-end" }}>
        <Button
          icon={<ReloadOutlined />}
          onClick={() => setRefreshTrigger((p) => p + 1)}
        >
          Refresh
        </Button>
      </Space>

      <Card>
        <Table
          columns={columns}
          dataSource={tableData}
          rowKey="id"
          loading={isLoading}
          pagination={false}
          size="middle"
          scroll={{ x: "max-content" }}
        />
      </Card>

      <LogStreamModal
        visible={modalVisible}
        onClose={handleStreamClose}
        streamUrl={streamUrl}
        title={titleFor[streamLogType]}
        logType={streamLogType}
      />
    </div>
  );
};
