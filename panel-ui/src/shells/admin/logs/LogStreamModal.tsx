import { useTranslation } from "react-i18next";
import { useState, useEffect, useRef } from "react";
import { Modal, Typography, Button, Space, message, Spin, theme } from "antd";
import { PauseOutlined, PlayCircleOutlined, ClearOutlined } from "@ant-design/icons";

const { Text } = Typography;

interface LogStreamModalProps {
  visible: boolean;
  onClose: () => void;
  streamUrl: string | null;
  title: string;
  logType: "access" | "error" | "goaccess";
}

// Trim a single trailing CR/LF (or both) — accommodates servers that
// send "line\n", "line\r\n", or "line" interchangeably. Keeps inner
// newlines untouched in case a single frame carries a multi-line block.
const stripTrailingNewline = (s: string): string => {
  if (typeof s !== "string") return s;
  if (s.endsWith("\r\n")) return s.slice(0, -2);
  if (s.endsWith("\n") || s.endsWith("\r")) return s.slice(0, -1);
  return s;
};

// Convert the WebSocket stream URL (ws[s]://host/api/v1/logs/stream/<key>)
// into the HTTP goaccess render URL. The HTTP route serves the GoAccess
// HTML snapshot with its own relaxed CSP (script-src 'self' 'unsafe-inline'
// 'unsafe-eval'), which the previous srcdoc-via-WS path could not — srcdoc
// inherits the panel's strict parent CSP and meta CSP can only tighten.
//
// Returns null if streamUrl can't be parsed into the expected shape (caller
// then shows a "no stream" placeholder instead of crashing).
const buildGoAccessHttpUrl = (streamUrl: string): string | null => {
  try {
    const u = new URL(streamUrl);
    if (u.protocol === "ws:") u.protocol = "http:";
    else if (u.protocol === "wss:") u.protocol = "https:";
    // Append /goaccess.html if not already present (some callers may pre-build).
    if (!u.pathname.endsWith("/goaccess.html")) {
      u.pathname = u.pathname.replace(/\/+$/, "") + "/goaccess.html";
    }
    return u.toString();
  } catch {
    return null;
  }
};

export const LogStreamModal = ({ visible, onClose, streamUrl, title, logType }: LogStreamModalProps) => {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const [logs, setLogs] = useState<string[]>([]);
  const [connected, setConnected] = useState(false);
  const [paused, setPaused] = useState(false);
  const [connecting, setConnecting] = useState(false);
  // GoAccess polling cache-buster: refresh the iframe by mutating ?t=<ts>
  // every 10s. Same cadence as the prior WS-driven render — operators see
  // identical update latency.
  const [goaccessTick, setGoaccessTick] = useState(() => Date.now());
  const wsRef = useRef<WebSocket | null>(null);
  const logsEndRef = useRef<HTMLDivElement>(null);
  const pausedLogsRef = useRef<string[]>([]);
  const goAccessFrameRef = useRef<HTMLIFrameElement>(null);
  const scrollPosRef = useRef<{ top: number; left: number }>({ top: 0, left: 0 });

  const scrollToBottom = () => {
    logsEndRef.current?.scrollIntoView({ behavior: "smooth" });
  };

  useEffect(() => {
    // GoAccess uses the HTTP-render route (URL-loaded iframe) so the
    // browser fetches a fresh response with its own relaxed CSP. WS is
    // for text streaming (access/error) only.
    if (logType === "goaccess") {
      return;
    }
    if (visible && streamUrl && !wsRef.current) {
      connectWebSocket();
    }

    return () => {
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [visible, streamUrl, logType]);

  // GoAccess polling effect: while the modal is open, bump goaccessTick
  // every 10s so the iframe's src changes and the browser re-fetches.
  // No-op when modal hidden or stream URL missing.
  useEffect(() => {
    if (logType !== "goaccess" || !visible || !streamUrl) return;
    const id = setInterval(() => {
      // Save scroll before refresh so onLoad can restore it.
      const iframe = goAccessFrameRef.current;
      const el = iframe?.contentDocument?.scrollingElement;
      if (el) {
        scrollPosRef.current = { top: el.scrollTop, left: el.scrollLeft };
      }
      setGoaccessTick(Date.now());
    }, 10000);
    return () => clearInterval(id);
  }, [logType, visible, streamUrl]);

  useEffect(() => {
    if (!paused) {
      scrollToBottom();
    }
  }, [logs, paused]);

  // GoAccess iframe content is set declaratively via srcDoc on the
  // iframe element below — no imperative doc.write needed (the
  // sandbox="allow-scripts" attribute makes the iframe a unique
  // origin, which blocks parent contentDocument access).

  const connectWebSocket = () => {
    if (!streamUrl) return;

    setConnecting(true);
    const ws = new WebSocket(streamUrl);

    ws.onopen = () => {
      setConnected(true);
      setConnecting(false);
      message.success("Connected to log stream");
      console.log("WebSocket connected");
    };

    ws.onmessage = (event) => {
      if (!paused) {
        // For goaccess, only keep the latest HTML message — the
        // iframe rerenders via srcDoc on the last entry, so growing
        // the array unbounded would just leak memory.
        const logLine = event.data;
        if (logType === "goaccess") {
          // Save scroll position before triggering the srcDoc update so
          // the onLoad handler can restore it after the iframe reloads.
          const iframe = goAccessFrameRef.current;
          const el = iframe?.contentDocument?.scrollingElement;
          if (el) {
            scrollPosRef.current = { top: el.scrollTop, left: el.scrollLeft };
          }
          setLogs([logLine]);
        } else {
          // Strip trailing CR/LF so the render-time join("\n") doesn't
          // double-space lines that arrived with their own newline.
          setLogs(prev => [...prev, stripTrailingNewline(logLine)]);
        }
      } else {
        pausedLogsRef.current.push(stripTrailingNewline(event.data));
      }
    };

    ws.onclose = (event) => {
      setConnected(false);
      setConnecting(false);
      if (event.code === 1000) {
        message.info("Log stream ended");
      } else {
        message.error("Connection lost");
      }
      console.log("WebSocket closed", event.code, event.reason);
    };

    ws.onerror = (error) => {
      setConnecting(false);
      message.error("WebSocket connection error");
      console.error("WebSocket error:", error);
    };

    wsRef.current = ws;
  };

  const handleClose = () => {
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }
    setLogs([]);
    setConnected(false);
    setPaused(false);
    pausedLogsRef.current = [];
    onClose();
  };

  const handlePauseToggle = () => {
    const newPaused = !paused;
    setPaused(newPaused);

    if (!newPaused && pausedLogsRef.current.length > 0) {
      // Resume: add paused logs to display
      setLogs(prev => [...prev, ...pausedLogsRef.current]);
      pausedLogsRef.current = [];
    }
  };

  const handleClearLogs = () => {
    setLogs([]);
    pausedLogsRef.current = [];
  };

  const renderLogContent = () => {
    if (logType === "goaccess") {
      // GoAccess iframe loaded via URL (not srcdoc) so the response's
      // own relaxed CSP applies — srcdoc inherits parent CSP which
      // forbids 'unsafe-eval' that GoAccess's templating requires.
      // Cache-busted by goaccessTick (refreshed every 10s by the
      // polling effect above); same cadence as the prior WS path.
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
      const src = `${httpUrl}${sep}t=${goaccessTick}`;
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
              const iframe = goAccessFrameRef.current;
              const el = iframe?.contentDocument?.scrollingElement;
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
        {logs.length === 0 ? (
          <div style={{ textAlign: "center", padding: "20px", color: "#888" }}>
            <Spin spinning={connecting}>
              <div>
                {connecting ? "Connecting to log stream..." : "Waiting for log data..."}
              </div>
            </Spin>
          </div>
        ) : (
          <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordWrap: "break-word" }}>
            {logs.join("\n")}
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
              type={paused ? "primary" : "default"}
              icon={paused ? <PlayCircleOutlined /> : <PauseOutlined />}
              onClick={handlePauseToggle}
              disabled={!connected}
            >
              {paused ? "Resume" : "Pause"}
            </Button>
            <Button
              icon={<ClearOutlined />}
              onClick={handleClearLogs}
            >
              Clear
            </Button>
            <Text type={connected ? "success" : "secondary"}>
              Status: {connecting ? "Connecting..." : connected ? "Connected" : "Disconnected"}
            </Text>
            {logs.length > 0 && (
              <Text type="secondary">
                {logs.length} lines {paused && pausedLogsRef.current.length > 0 &&
                  `(+${pausedLogsRef.current.length} paused)`}
              </Text>
            )}
          </Space>

          {renderLogContent()}
        </Space>
      )}
    </Modal>
  );
};