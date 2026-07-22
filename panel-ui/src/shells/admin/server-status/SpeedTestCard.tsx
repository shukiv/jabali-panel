// SpeedTestCard — LibreSpeed-style connection speed test for the Server
// Status page (GH #252). Measures the link between the operator's browser
// and the panel host: ping + jitter, then multi-stream download and upload
// throughput against three tiny admin endpoints (/admin/speedtest/*).
//
// The measurement runs on the main thread with fetch + a streaming reader
// (no Web Worker / vendored asset) — parallel streams over a fixed time
// window approximate LibreSpeed's accuracy without the worker plumbing.
import { useTranslation } from "react-i18next";
import { useRef, useState } from "react";
import { Button, Card, Space, Statistic, Typography } from "antd";
import {
  ClockCircleOutlined,
  DownloadOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from "@icons";

const BASE = "/api/v1/admin/speedtest";
const STREAMS = 4;
const TEST_MS = 8000; // per-direction measurement window
const DL_BYTES = 100 * 1024 * 1024; // request big; the time window stops it
const UL_CHUNK = 4 * 1024 * 1024; // 4 MiB per upload POST
const PING_SAMPLES = 12;

type Phase = "idle" | "ping" | "download" | "upload" | "done" | "error";

interface Results {
  ping: number; // ms
  jitter: number; // ms
  download: number; // Mbps
  upload: number; // Mbps
}

const opts = (signal?: AbortSignal): RequestInit => ({
  cache: "no-store",
  credentials: "include",
  signal,
});

async function measurePing(): Promise<{ ping: number; jitter: number }> {
  const times: number[] = [];
  for (let i = 0; i < PING_SAMPLES; i++) {
    const t0 = performance.now();
    await fetch(`${BASE}/ping?n=${i}`, opts());
    times.push(performance.now() - t0);
  }
  const ping = Math.min(...times);
  let acc = 0;
  for (let i = 1; i < times.length; i++) acc += Math.abs(times[i] - times[i - 1]);
  const jitter = times.length > 1 ? acc / (times.length - 1) : 0;
  return { ping, jitter };
}

async function measureDownload(onLive: (mbps: number) => void): Promise<number> {
  let total = 0;
  const start = performance.now();
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), TEST_MS);
  const stream = async () => {
    while (!controller.signal.aborted) {
      try {
        const res = await fetch(
          `${BASE}/download?bytes=${DL_BYTES}&r=${Math.random()}`,
          opts(controller.signal),
        );
        const reader = res.body?.getReader();
        if (!reader) break;
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          total += value?.length ?? 0;
          onLive((total * 8) / ((performance.now() - start) / 1000) / 1e6);
          if (controller.signal.aborted) {
            await reader.cancel().catch(() => {});
            break;
          }
        }
      } catch {
        if (controller.signal.aborted) break;
      }
    }
  };
  await Promise.all(Array.from({ length: STREAMS }, stream));
  window.clearTimeout(timer);
  return (total * 8) / ((performance.now() - start) / 1000) / 1e6;
}

async function measureUpload(onLive: (mbps: number) => void): Promise<number> {
  const blob = new Blob([new Uint8Array(UL_CHUNK)]);
  let total = 0;
  const start = performance.now();
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), TEST_MS);
  const stream = async () => {
    while (!controller.signal.aborted) {
      try {
        await fetch(`${BASE}/upload`, {
          ...opts(controller.signal),
          method: "POST",
          body: blob,
          headers: { "Content-Type": "application/octet-stream" },
        });
        total += UL_CHUNK;
        onLive((total * 8) / ((performance.now() - start) / 1000) / 1e6);
      } catch {
        if (controller.signal.aborted) break;
      }
    }
  };
  await Promise.all(Array.from({ length: STREAMS }, stream));
  window.clearTimeout(timer);
  return (total * 8) / ((performance.now() - start) / 1000) / 1e6;
}

// Throttle live updates so a per-chunk callback (hundreds/sec across
// streams) doesn't thrash React with re-renders during the test.
function throttle<T>(fn: (v: T) => void, ms: number): (v: T) => void {
  let last = 0;
  return (v: T) => {
    const now = performance.now();
    if (now - last >= ms) {
      last = now;
      fn(v);
    }
  };
}

const fmtMbps = (v: number | undefined) =>
  v === undefined ? "—" : v >= 100 ? v.toFixed(0) : v.toFixed(1);
const fmtMs = (v: number | undefined) => (v === undefined ? "—" : v.toFixed(1));

export const SpeedTestCard = () => {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<Phase>("idle");
  const [results, setResults] = useState<Partial<Results>>({});
  const [live, setLive] = useState<number | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  const running = useRef(false);

  const run = async () => {
    if (running.current) return;
    running.current = true;
    setResults({});
    setLive(undefined);
    setErr(null);
    try {
      setPhase("ping");
      const { ping, jitter } = await measurePing();
      setResults({ ping, jitter });

      const liveTick = throttle(setLive, 120);

      setPhase("download");
      setLive(0);
      const download = await measureDownload(liveTick);
      setResults((r) => ({ ...r, download }));
      setLive(undefined);

      setPhase("upload");
      setLive(0);
      const upload = await measureUpload(liveTick);
      setResults((r) => ({ ...r, upload }));
      setLive(undefined);

      setPhase("done");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Speed test failed");
      setPhase("error");
    } finally {
      running.current = false;
    }
  };

  const busy = phase === "ping" || phase === "download" || phase === "upload";

  return (
    <Card
      title={
        <>
          <ThunderboltOutlined /> Speed Test
        </>
      }
      extra={
        <Button type="primary" size="small" loading={busy} onClick={run}>
          {phase === "idle" ? "Run test" : busy ? "Testing…" : "Run again"}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        Measures the connection between your browser and this server.
      </Typography.Paragraph>

      <Space size="large" wrap>
        <Statistic
          title={
            <>
              <ClockCircleOutlined /> Ping
            </>
          }
          value={fmtMs(results.ping)}
          suffix="ms"
        />
        <Statistic title={t("speedtestcard.jitter")} value={fmtMs(results.jitter)} suffix="ms" />
        <Statistic
          title={
            <>
              <DownloadOutlined /> Download
            </>
          }
          value={
            phase === "download" && live !== undefined
              ? fmtMbps(live)
              : fmtMbps(results.download)
          }
          suffix="Mbps"
        />
        <Statistic
          title={
            <>
              <UploadOutlined /> Upload
            </>
          }
          value={
            phase === "upload" && live !== undefined
              ? fmtMbps(live)
              : fmtMbps(results.upload)
          }
          suffix="Mbps"
        />
      </Space>

      {err && (
        <Typography.Paragraph type="danger" style={{ marginTop: 12, marginBottom: 0 }}>
          {err}
        </Typography.Paragraph>
      )}
    </Card>
  );
};
