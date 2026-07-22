// M45 root web terminal (ADR-0096). xterm.js ↔ WSS ↔ panel-api ↔
// root PTY broker. Off by default; this page shows enable-instructions
// when the gate is closed. Every byte of an open session is recorded
// server-side to /var/log/jabali/terminal/<id>.cast.
import { useTranslation } from "react-i18next";
import { useEffect, useRef, useState } from "react";
import { Alert, Card, Result, Spin, Typography } from "antd";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

import { apiClient } from "../../../apiClient";
import { useRootTerminalEnabled } from "../../../hooks/useRootTerminalEnabled";

// Wire opcodes — must match panel-api terminal.go / agent terminal_pty.go.
const OP_STDOUT = 0x00;
const OP_STDIN = 0x01;
const OP_RESIZE = 0x02;
const OP_EXIT = 0x03;

type MintResponse = { token: string; websocket_url: string; expires_at: string };

function frame(op: number, payload: Uint8Array): Uint8Array {
  const out = new Uint8Array(payload.length + 1);
  out[0] = op;
  out.set(payload, 1);
  return out;
}

export function AdminTerminal() {
  const { t } = useTranslation();
  const gate = useRootTerminalEnabled();
  const hostRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<"idle" | "connecting" | "open" | "closed" | "error">("idle");
  const [errMsg, setErrMsg] = useState<string>("");

  useEffect(() => {
    if (!gate.enabled || !hostRef.current) return;
    let disposed = false;
    let ws: WebSocket | null = null;
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "Menlo, Consolas, 'DejaVu Sans Mono', monospace",
      fontSize: 13,
      theme: { background: "#0b0e14" },
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();

    const enc = new TextEncoder();
    const dec = new TextDecoder();

    (async () => {
      setStatus("connecting");
      try {
        const { data } = await apiClient.post<MintResponse>("/admin/terminal/session");
        if (disposed) return;
        const url = new URL(data.websocket_url);
        url.searchParams.set("cols", String(term.cols));
        url.searchParams.set("rows", String(term.rows));
        ws = new WebSocket(url.toString());
        ws.binaryType = "arraybuffer";

        ws.onopen = () => {
          if (disposed) return;
          setStatus("open");
          term.focus();
        };
        ws.onmessage = (ev) => {
          const buf = new Uint8Array(ev.data as ArrayBuffer);
          if (buf.length < 1) return;
          const op = buf[0];
          const body = buf.subarray(1);
          if (op === OP_STDOUT) {
            term.write(dec.decode(body));
          } else if (op === OP_EXIT) {
            term.write("\r\n\x1b[33m[session ended]\x1b[0m\r\n");
            setStatus("closed");
            ws?.close();
          }
        };
        ws.onerror = () => {
          if (!disposed) {
            setStatus("error");
            setErrMsg("WebSocket error — see browser console / panel-api logs.");
          }
        };
        ws.onclose = () => {
          if (!disposed && status !== "error") setStatus("closed");
        };

        term.onData((d) => {
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(frame(OP_STDIN, enc.encode(d)));
          }
        });
        term.onResize(({ cols, rows }) => {
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(frame(OP_RESIZE, enc.encode(JSON.stringify({ cols, rows }))));
          }
        });
      } catch (e: unknown) {
        if (disposed) return;
        const er = e as { response?: { status?: number; data?: { error?: string; detail?: string } } };
        setStatus("error");
        setErrMsg(
          er.response?.status === 403
            ? "Root terminal is disabled. Enable it in Server Settings (off by default)."
            : er.response?.data?.detail ?? er.response?.data?.error ?? "Failed to start session.",
        );
      }
    })();

    const onWinResize = () => {
      try {
        fit.fit();
      } catch {
        /* element gone */
      }
    };
    window.addEventListener("resize", onWinResize);

    return () => {
      disposed = true;
      window.removeEventListener("resize", onWinResize);
      try {
        ws?.close();
      } catch {
        /* noop */
      }
      term.dispose();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gate.enabled]);

  if (gate.isLoading) {
    return (
      <div style={{ display: "flex", justifyContent: "center", padding: 48 }}>
        <Spin />
      </div>
    );
  }

  if (!gate.enabled) {
    return (
      <Result
        status="warning"
        title={t("adminterminal.root_terminal_is_disabled")}
        subTitle="This is a true unrestricted root shell and is off by default. Enable it in Server Settings → root_terminal_enabled. Every session is fully recorded."
      />
    );
  }

  return (
    <Card
      title={
        <Typography.Text strong>
          Root Terminal <Typography.Text type="secondary">— uid 0</Typography.Text>
        </Typography.Text>
      }
      styles={{ body: { padding: 0 } }}
    >
      <Alert
        type="error"
        showIcon
        banner
        message={t("adminterminal.root_shell_every_keystroke_and_all_output_is")}
      />
      {status === "error" && (
        <Alert type="error" showIcon style={{ margin: 12 }} message={errMsg} />
      )}
      <div
        ref={hostRef}
        style={{ height: "70vh", background: "#0b0e14", padding: 8 }}
        data-testid="root-terminal"
      />
    </Card>
  );
}
