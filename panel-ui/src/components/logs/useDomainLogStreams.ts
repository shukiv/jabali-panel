// useDomainLogStreams — the audience-neutral stream lifecycle for the
// per-domain log viewer (JAB-296). Owns the create/close of a stream key and
// the modal wiring so the admin DomainLogsTab and the tenant UserLogsPage no
// longer duplicate the same open/close state machine (which had drifted: the
// admin copy allowed an aggregate open, the tenant copy did not, and both
// carried the same close bug below).
import { useRef, useState } from "react";
import { apiClient } from "../../apiClient";
import { feedback } from "../../lib/feedback"; // GH #970: themed toasts
import {
  type LogType,
  LOG_STREAM_TITLES,
  buildLogStreamPayload,
} from "./domainLogStreams";

// The props the neutral LogStreamModal needs. Mirrors its component contract
// so an adapter can spread {...streams.modalProps} straight onto it.
export interface LogStreamModalProps {
  visible: boolean;
  onClose: () => void;
  streamUrl: string | null;
  title: string;
  logType: LogType;
}

export interface DomainLogStreams {
  modalProps: LogStreamModalProps;
  openStream: (logType: LogType, domainId?: string) => Promise<void>;
}

export function useDomainLogStreams(): DomainLogStreams {
  // The active stream key lives in a ref, not state: closeStream reads and
  // clears it synchronously (see AC3), which a state setter cannot guarantee
  // between two rapid closes.
  const streamKeyRef = useRef<string | null>(null);
  const [streamUrl, setStreamUrl] = useState<string | null>(null);
  const [streamLogType, setStreamLogType] = useState<LogType>("access");
  const [modalVisible, setModalVisible] = useState(false);

  const openStream = async (logType: LogType, domainId?: string) => {
    try {
      const response = await apiClient.post(
        "/logs/access",
        buildLogStreamPayload(logType, domainId),
      );
      const { stream_key, websocket_url } = response.data;
      streamKeyRef.current = stream_key;
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

  const closeStream = async () => {
    // AC3: delete the stream key exactly once. The modal can fire its close in
    // more than one way (Esc, mask click, the X, a StrictMode double-invoke),
    // so read the key and null it out BEFORE the await — a second close then
    // sees no key and issues no DELETE. Tolerate an already-expired stream:
    // the server may have reaped it, and a 404/410 there is harmless.
    const key = streamKeyRef.current;
    streamKeyRef.current = null;
    setModalVisible(false);
    setStreamUrl(null);
    if (!key) return;
    try {
      await apiClient.delete(`/logs/access/${key}`);
    } catch {
      // Stream already gone server-side; nothing to clean up.
    }
  };

  return {
    modalProps: {
      visible: modalVisible,
      onClose: closeStream,
      streamUrl,
      title: LOG_STREAM_TITLES[streamLogType],
      logType: streamLogType,
    },
    openStream,
  };
}
