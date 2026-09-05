// useLogStream — the audience-neutral WebSocket + GoAccess-polling lifecycle
// behind the LogStreamModal (JAB-296, AC5). Pulled out of the .tsx so the
// newline handling, the ring-buffer cap, pause/resume buffering, the
// connect/close transitions, and the GoAccess polling cadence can be exercised
// with a fake WebSocket and fake timers instead of only living as untestable
// closures inside a modal. The DOM (auto-scroll, the GoAccess iframe and its
// scroll preservation) stays in the component; this hook is lifecycle only.
import { useEffect, useRef, useState } from "react";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  MAX_LOG_LINES,
  capLogLines,
  stripTrailingNewline,
} from "../../utils/logStream";
import { type LogType } from "./domainLogStreams";

export type { LogType };

export interface UseLogStreamOptions {
  streamUrl: string | null;
  logType: LogType;
  // Whether the stream should be live (the modal's `visible`). When it goes
  // false the socket is torn down and the polling interval stops.
  active: boolean;
  // Fired synchronously just before each GoAccess poll bumps the tick, so the
  // component can snapshot the iframe scroll position before the src changes.
  onBeforeGoAccessRefresh?: () => void;
}

export interface LogStream {
  logs: string[];
  connected: boolean;
  connecting: boolean;
  paused: boolean;
  bufferedCount: number;
  goAccessTick: number;
  togglePause: () => void;
  clear: () => void;
  // Tear down the socket and reset all display state. The component calls this
  // when the modal closes, before invoking its own onClose.
  reset: () => void;
}

export function useLogStream({
  streamUrl,
  logType,
  active,
  onBeforeGoAccessRefresh,
}: UseLogStreamOptions): LogStream {
  const [logs, setLogs] = useState<string[]>([]);
  const [connected, setConnected] = useState(false);
  const [connecting, setConnecting] = useState(false);
  const [paused, setPaused] = useState(false);
  const [bufferedCount, setBufferedCount] = useState(0);
  const [goAccessTick, setGoAccessTick] = useState(() => Date.now());

  const wsRef = useRef<WebSocket | null>(null);
  const pausedLogsRef = useRef<string[]>([]);
  // The socket's onmessage closure is created once at connect time; reading the
  // `paused` STATE there would forever see its connect-time value (the original
  // bug: pausing only relabelled the button, frames kept appending). Read a ref
  // instead so pause/resume actually gates the live socket.
  const pausedRef = useRef(false);
  // The GoAccess pre-refresh callback can change identity every render; hold it
  // in a ref so the polling effect does not resubscribe (which would reset the
  // 10s interval before it ever fires).
  const beforeRefreshRef = useRef(onBeforeGoAccessRefresh);
  beforeRefreshRef.current = onBeforeGoAccessRefresh;

  useEffect(() => {
    // GoAccess renders through an HTTP iframe, not a socket — no WS for it.
    if (logType === "goaccess") return;
    if (active && streamUrl && !wsRef.current) {
      setConnecting(true);
      const ws = new WebSocket(streamUrl);

      ws.onopen = () => {
        setConnected(true);
        setConnecting(false);
        feedback.message.success("Connected to log stream");
      };

      ws.onmessage = (event) => {
        const line = stripTrailingNewline(event.data);
        if (!pausedRef.current) {
          setLogs((prev) => capLogLines([...prev, line]));
        } else {
          // The paused buffer needs the same ceiling: a stream paused on a busy
          // log otherwise grows unbounded and then floods the view on resume.
          pausedLogsRef.current.push(line);
          if (pausedLogsRef.current.length > MAX_LOG_LINES) {
            pausedLogsRef.current = pausedLogsRef.current.slice(-MAX_LOG_LINES);
          }
          setBufferedCount(pausedLogsRef.current.length);
        }
      };

      ws.onclose = (event) => {
        setConnected(false);
        setConnecting(false);
        if (event.code === 1000) {
          feedback.message.info("Log stream ended");
        } else {
          feedback.message.error("Connection lost");
        }
      };

      ws.onerror = () => {
        setConnecting(false);
        feedback.message.error("WebSocket connection error");
      };

      wsRef.current = ws;
    }

    return () => {
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [active, streamUrl, logType]);

  // GoAccess polling: while live, bump the tick every 10s so the iframe src
  // changes and the browser re-fetches. Same cadence as the prior WS path.
  useEffect(() => {
    if (logType !== "goaccess" || !active || !streamUrl) return;
    const id = setInterval(() => {
      beforeRefreshRef.current?.();
      setGoAccessTick(Date.now());
    }, 10000);
    return () => clearInterval(id);
  }, [logType, active, streamUrl]);

  const togglePause = () => {
    const next = !pausedRef.current;
    pausedRef.current = next;
    setPaused(next);
    if (!next && pausedLogsRef.current.length > 0) {
      // Resume: flush the buffered frames into the view, capped.
      const buffered = pausedLogsRef.current;
      pausedLogsRef.current = [];
      setBufferedCount(0);
      setLogs((prev) => capLogLines([...prev, ...buffered]));
    }
  };

  const clear = () => {
    setLogs([]);
    pausedLogsRef.current = [];
    setBufferedCount(0);
  };

  const reset = () => {
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }
    setLogs([]);
    setConnected(false);
    setPaused(false);
    pausedRef.current = false;
    pausedLogsRef.current = [];
    setBufferedCount(0);
  };

  return {
    logs,
    connected,
    connecting,
    paused,
    bufferedCount,
    goAccessTick,
    togglePause,
    clear,
    reset,
  };
}
