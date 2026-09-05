import { useTranslation } from "react-i18next";
import { useEffect, useRef } from "react";
import { Modal, Typography, Button, Space, Spin, theme } from "antd";
import { PauseOutlined, PlayCircleOutlined, ClearOutlined } from "@ant-design/icons";
import { buildGoAccessHttpUrl } from "../utils/logStream";
import { useLogStream } from "./logs/useLogStream";

const { Text } = Typography;

interface LogStreamModalProps {
  visible: boolean;
  onClose: () => void;
  streamUrl: string | null;
  title: string;
  logType: "access" | "error" | "goaccess";
}

// The stream lifecycle (WebSocket connect/close, pause/resume buffering, the
// ring-buffer cap, and GoAccess polling) lives in ./logs/useLogStream so it can
// be unit-tested with a fake socket and fake timers (JAB-296, AC5). This
// component owns only the DOM: auto-scroll for text logs, and the GoAccess
// iframe plus its scroll preservation across the 10s auto-refresh.

export const LogStreamModal = ({ visible, onClose, streamUrl, title, logType }: LogStreamModalProps) => {
  const { t } = useTranslation();
  const { token } = theme.useToken();

  const logsEndRef = useRef<HTMLDivElement>(null);
  const goAccessFrameRef = useRef<HTMLIFrameElement>(null);
  const scrollPosRef = useRef<{ top: number; left: number }>({ top: 0, left: 0 });

  // Snapshot the iframe scroll before each GoAccess poll swaps the src, so the
  // onLoad handler below can restore it after the reload.
  const saveGoAccessScroll = () => {
    const el = goAccessFrameRef.current?.contentDocument?.scrollingElement;
    if (el) {
      scrollPosRef.current = { top: el.scrollTop, left: el.scrollLeft };
    }
  };

  const stream = useLogStream({
    streamUrl,
    logType,
    active: visible,
    onBeforeGoAccessRefresh: saveGoAccessScroll,
  });

  const scrollToBottom = () => {
    logsEndRef.current?.scrollIntoView({ behavior: "smooth" });
  };

  useEffect(() => {
    if (!stream.paused) {
      scrollToBottom();
    }
  }, [stream.logs, stream.paused]);

  const handleClose = () => {
    stream.reset();
    onClose();
  };

  const renderLogContent = () => {
    if (logType === "goaccess") {
      // GoAccess iframe loaded via URL (not srcdoc) so the response's
      // own relaxed CSP applies — srcdoc inherits parent CSP which
      // forbids 'unsafe-eval' that GoAccess's templating requires.
      // Cache-busted by goAccessTick (refreshed every 10s by the
      // polling in useLogStream); same cadence as the prior WS path.
      const httpUrl = streamUrl ? buildGoAccessHttpUrl(streamUrl) : null;
      if (!httpUrl) {
        return (
          <div style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            height: "100%",
            backgroundColor: token.colorBgLayout,
          }}>
            <Text type="secondary">No stream URL — open a log stream first.</Text>
          </div>
        );
      }
      const sep = httpUrl.includes("?") ? "&" : "?";
      const src = `${httpUrl}${sep}t=${stream.goAccessTick}`;
      return (
        <div style={{ width: "100%", height: "100%", position: "relative" }}>
          <iframe
            ref={goAccessFrameRef}
            src={src}
            style={{
              width: "100%",
              height: "100%",
              border: "none",
              display: "block",
            }}
            title={t("logstreammodal.goaccess_dashboard")}
            sandbox="allow-scripts allow-same-origin"
            onLoad={() => {
              const el = goAccessFrameRef.current?.contentDocument?.scrollingElement;
              if (el && scrollPosRef.current.top > 0) {
                el.scrollTop = scrollPosRef.current.top;
                el.scrollLeft = scrollPosRef.current.left;
              }
            }}
          />
        </div>
      );
    }

    // For access and error logs, show as text
    return (
      <div
        style={{
          height: "calc(95vh - 230px)", minHeight: "300px",
          overflow: "auto",
          backgroundColor: "#1f1f1f",
          color: "#ffffff",
          fontFamily: "Monaco, Consolas, monospace",
          fontSize: "12px",
          padding: "10px",
          border: "1px solid #d9d9d9",
          borderRadius: "4px"
        }}
      >
        {stream.logs.length === 0 ? (
          <div style={{ textAlign: "center", padding: "20px", color: "#888" }}>
            <Spin spinning={stream.connecting}>
              <div>
                {stream.connecting ? "Connecting to log stream..." : "Waiting for log data..."}
              </div>
            </Spin>
          </div>
        ) : (
          <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordWrap: "break-word" }}>
            {stream.logs.join("\n")}
            <div ref={logsEndRef} />
          </pre>
        )}
      </div>
    );
  };

  const isGoAccess = logType === "goaccess";

  return (
    <Modal
      title={isGoAccess ? null : title}
      open={visible}
      onCancel={handleClose}
      width="95vw"
      style={{ top: "2.5vh", maxWidth: "95vw", paddingBottom: 0 }}
      styles={
        isGoAccess
          ? { body: { height: "calc(95vh - 12px)", overflow: "hidden", padding: 0 } }
          : { body: { height: "calc(95vh - 110px)", overflow: "hidden" } }
      }
      className={isGoAccess ? "goaccess-modal" : undefined}
      footer={null}
      destroyOnClose
    >
      {isGoAccess ? (
        renderLogContent()
      ) : (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Space>
            <Button
              type={stream.paused ? "primary" : "default"}
              icon={stream.paused ? <PlayCircleOutlined /> : <PauseOutlined />}
              onClick={stream.togglePause}
              disabled={!stream.connected}
            >
              {stream.paused ? "Resume" : "Pause"}
            </Button>
            <Button
              icon={<ClearOutlined />}
              onClick={stream.clear}
            >
              Clear
            </Button>
            <Text type={stream.connected ? "success" : "secondary"}>
              Status: {stream.connecting ? "Connecting..." : stream.connected ? "Connected" : "Disconnected"}
            </Text>
            {stream.logs.length > 0 && (
              <Text type="secondary">
                {stream.logs.length} lines {stream.paused && stream.bufferedCount > 0 &&
                  `(+${stream.bufferedCount} paused)`}
              </Text>
            )}
          </Space>

          {renderLogContent()}
        </Space>
      )}
    </Modal>
  );
};
