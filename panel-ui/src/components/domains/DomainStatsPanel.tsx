// DomainStatsPanel — a domain's web traffic: requests and bandwidth per day
// from the nightly access-log scan, and where the visitors came from by
// country. The Statistics tab on the tenant Web Domain page and on the admin
// Edit Domain page. Reads GET /domains/:id/bandwidth, which is owner-scoped.
//
// The scan counts a day the next day (yesterday's rotated log), so today is
// never in the chart. Requests include known crawlers; bandwidth and the
// country breakdown leave them out (goaccess --ignore-crawlers). Countries
// come only on hosts with the GeoLite2 database CrowdSec downloads.
//
// One measure at a time in the chart (Requests or Bandwidth), never two
// y-scales. Each column is its own hover/focus target; the same numbers are
// in the daily table, so the tooltip never gates a value.
import { useEffect, useMemo, useRef, useState } from "react";
import { Alert, Card, Col, Collapse, Empty, Row, Segmented, Space, Statistic, Table, Typography, theme } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { apiClient } from "../../apiClient";
import { humanBytes } from "../../utils/bytes";
import { shortDate } from "../../utils/datetime";

interface DayRow {
  day: string;
  bytes_total: number;
  requests_total: number;
}

interface CountryRow {
  country_code: string;
  bytes_total: number;
  requests_total: number;
}

interface BandwidthResponse {
  from: string;
  to: string;
  bytes_total: number;
  requests_total: number;
  daily: DayRow[];
  countries: CountryRow[];
}

type WindowDays = 7 | 30 | 90;
type Measure = "requests" | "bytes";

const DAY_MS = 86_400_000;

// isoDay is a UTC calendar day, YYYY-MM-DD, the form the ledger keys on.
function isoDay(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10);
}

// windowDays lists every day of the window, oldest first, so a day without a
// ledger row still has its place on the axis.
function windowDays(from: string, to: string): string[] {
  const out: string[] = [];
  const end = Date.parse(to + "T00:00:00Z");
  for (let t = Date.parse(from + "T00:00:00Z"); t <= end && out.length < 400; t += DAY_MS) {
    out.push(isoDay(t));
  }
  return out;
}

const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });

function formatValue(measure: Measure, n: number): string {
  return measure === "bytes" ? humanBytes(n) : n.toLocaleString();
}

function formatTick(measure: Measure, n: number): string {
  return measure === "bytes" ? humanBytes(n) : compact.format(n);
}

// niceMax rounds the axis top up to 1, 2 or 5 × a power of ten (bytes to a
// power of 1024 times the same), so the ticks are clean numbers.
function niceMax(measure: Measure, max: number): number {
  if (max <= 0) return 1;
  const base = measure === "bytes" ? 1024 : 10;
  const exp = measure === "bytes" ? Math.floor(Math.log(max) / Math.log(1024)) : Math.floor(Math.log10(max));
  const unit = Math.pow(base, Math.max(0, exp));
  for (const step of [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000]) {
    if (step * unit >= max) return step * unit;
  }
  return Math.ceil(max / unit) * unit;
}

function countryName(code: string, locale: string): string {
  if (code === "ZZ") return "Unknown / other";
  try {
    // The UI's language codes use "_" (pt_BR, zh_Hans); Intl wants BCP 47.
    return new Intl.DisplayNames([locale.replace("_", "-"), "en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function useWidth<T extends HTMLElement>(): [React.RefObject<T>, number] {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const apply = () => setWidth(el.clientWidth);
    apply();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(apply);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width];
}

const CHART_H = 200;
const PAD_LEFT = 56;
const PAD_BOTTOM = 22;
const PAD_TOP = 8;

function DailyChart({ days, byDay, measure }: { days: string[]; byDay: Map<string, DayRow>; measure: Measure }) {
  const { token } = theme.useToken();
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);

  const values = days.map((d) => {
    const row = byDay.get(d);
    return row ? (measure === "bytes" ? row.bytes_total : row.requests_total) : null;
  });
  const top = niceMax(measure, Math.max(0, ...values.map((v) => v ?? 0)));
  const plotW = Math.max(0, width - PAD_LEFT);
  const plotH = CHART_H - PAD_TOP - PAD_BOTTOM;
  const band = days.length ? plotW / days.length : 0;
  const barW = Math.max(2, Math.min(24, band - 2));
  const y = (v: number) => PAD_TOP + plotH - (v / top) * plotH;
  const ticks = [0, top / 2, top];
  // A label every few days, so they never collide.
  const labelEvery = Math.max(1, Math.ceil(days.length / Math.max(1, Math.floor(plotW / 72))));

  const hovered = hover !== null ? { day: days[hover], value: values[hover] } : null;

  return (
    <div ref={ref} style={{ position: "relative", width: "100%" }}>
      {width > 0 && (
        <svg
          width={width}
          height={CHART_H}
          role="img"
          aria-label={`${measure === "bytes" ? "Bandwidth" : "Requests"} per day, ${days.length} days`}
          onMouseLeave={() => setHover(null)}
        >
          {ticks.map((t) => (
            <g key={t}>
              <line x1={PAD_LEFT} x2={width} y1={y(t)} y2={y(t)} stroke={token.colorBorderSecondary} strokeWidth={1} />
              <text
                x={PAD_LEFT - 8}
                y={y(t)}
                textAnchor="end"
                dominantBaseline="middle"
                fontSize={11}
                fill={token.colorTextTertiary}
                style={{ fontVariantNumeric: "tabular-nums" }}
              >
                {formatTick(measure, t)}
              </text>
            </g>
          ))}
          {days.map((d, i) => {
            const v = values[i];
            const cx = PAD_LEFT + band * i + band / 2;
            const x = cx - barW / 2;
            const h = v ? Math.max(1, (v / top) * plotH) : 0;
            const yTop = PAD_TOP + plotH - h;
            const r = Math.min(4, barW / 2, h);
            // Rounded data end, square at the baseline.
            const path =
              h > 0
                ? `M${x},${PAD_TOP + plotH} V${yTop + r} Q${x},${yTop} ${x + r},${yTop} H${x + barW - r} Q${x + barW},${yTop} ${x + barW},${yTop + r} V${PAD_TOP + plotH} Z`
                : "";
            return (
              <g key={d}>
                {path && (
                  <path d={path} fill={token.colorPrimary} opacity={hover === null || hover === i ? 1 : 0.55} />
                )}
                {i % labelEvery === 0 && (
                  <text
                    x={cx}
                    y={CHART_H - 6}
                    textAnchor="middle"
                    fontSize={11}
                    fill={token.colorTextTertiary}
                  >
                    {shortDate(d)}
                  </text>
                )}
                <rect
                  x={PAD_LEFT + band * i}
                  y={PAD_TOP}
                  width={band}
                  height={plotH}
                  fill="transparent"
                  tabIndex={0}
                  aria-label={`${shortDate(d)}: ${v === null ? "no data" : formatValue(measure, v)}`}
                  onMouseEnter={() => setHover(i)}
                  onFocus={() => setHover(i)}
                  onBlur={() => setHover(null)}
                  style={{ outline: "none", cursor: "default" }}
                />
              </g>
            );
          })}
        </svg>
      )}
      {hovered && hover !== null && (
        <div
          role="status"
          style={{
            position: "absolute",
            top: 0,
            left: Math.min(Math.max(PAD_LEFT, PAD_LEFT + band * hover + band / 2 - 70), Math.max(PAD_LEFT, width - 140)),
            width: 140,
            pointerEvents: "none",
            background: token.colorBgElevated,
            boxShadow: token.boxShadowSecondary,
            borderRadius: token.borderRadius,
            padding: "6px 10px",
          }}
        >
          <div style={{ fontWeight: 600 }}>
            {hovered.value === null ? "No data" : formatValue(measure, hovered.value)}
          </div>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {shortDate(hovered.day)}
          </Typography.Text>
        </div>
      )}
    </div>
  );
}

export function DomainStatsPanel({ domainId }: { domainId: string }) {
  const { i18n } = useTranslation();
  const { token } = theme.useToken();
  const [days, setDays] = useState<WindowDays>(30);
  const [measure, setMeasure] = useState<Measure>("requests");

  // Yesterday is the newest day the nightly scan can have counted.
  const to = isoDay(Date.now() - DAY_MS);
  const from = isoDay(Date.now() - days * DAY_MS);

  const q = useQuery<BandwidthResponse>({
    queryKey: ["domain-bandwidth", domainId, from, to],
    queryFn: async () => {
      const r = await apiClient.get<BandwidthResponse>(`/domains/${domainId}/bandwidth`, { params: { from, to } });
      return r.data;
    },
    placeholderData: (prev) => prev,
    staleTime: 5 * 60_000,
  });

  const data = q.data;
  const byDay = useMemo(() => {
    const m = new Map<string, DayRow>();
    for (const r of data?.daily ?? []) m.set(r.day.slice(0, 10), r);
    return m;
  }, [data]);
  const axis = useMemo(() => windowDays(data?.from ?? from, data?.to ?? to), [data, from, to]);
  const countries = data?.countries ?? [];
  const countryRequests = countries.reduce((acc, c) => acc + c.requests_total, 0);
  const daysWithData = byDay.size;

  if (q.isError) {
    return <Alert type="error" showIcon message="Could not load this domain's statistics." />;
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%", opacity: q.isFetching && data ? 0.6 : 1 }}>
      <Segmented<WindowDays>
        value={days}
        onChange={setDays}
        options={[
          { label: "Last 7 days", value: 7 },
          { label: "Last 30 days", value: 30 },
          { label: "Last 90 days", value: 90 },
        ]}
      />

      <Row gutter={[16, 16]}>
        <Col xs={24} sm={8}>
          <Card size="small" loading={q.isLoading}>
            <Statistic title="Requests" value={data?.requests_total ?? 0} />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              All requests, known bots included
            </Typography.Text>
          </Card>
        </Col>
        <Col xs={24} sm={8}>
          <Card size="small" loading={q.isLoading}>
            <Statistic title="Bandwidth" value={humanBytes(data?.bytes_total ?? 0)} />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Sent to visitors; known bots not counted
            </Typography.Text>
          </Card>
        </Col>
        <Col xs={24} sm={8}>
          <Card size="small" loading={q.isLoading}>
            <Statistic
              title="Top country"
              value={countries.length ? countryName(countries[0].country_code, i18n.language) : "—"}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {countries.length && countryRequests
                ? `${Math.round((countries[0].requests_total / countryRequests) * 100)}% of visitor requests`
                : "No country data yet"}
            </Typography.Text>
          </Card>
        </Col>
      </Row>

      <Card
        size="small"
        title={measure === "bytes" ? "Bandwidth per day" : "Requests per day"}
        extra={
          <Segmented<Measure>
            size="small"
            value={measure}
            onChange={setMeasure}
            options={[
              { label: "Requests", value: "requests" },
              { label: "Bandwidth", value: "bytes" },
            ]}
          />
        }
        loading={q.isLoading}
      >
        {daysWithData === 0 ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="No traffic counted yet. Each day is counted the next day, from the site's access log."
          />
        ) : (
          <>
            <DailyChart days={axis} byDay={byDay} measure={measure} />
            <Collapse
              ghost
              size="small"
              items={[
                {
                  key: "table",
                  label: "Daily values",
                  children: (
                    <Table<DayRow>
                      size="small"
                      rowKey="day"
                      pagination={false}
                      scroll={{ x: "max-content", y: 320 }}
                      dataSource={[...(data?.daily ?? [])].reverse()}
                      columns={[
                        { title: "Day", dataIndex: "day", render: (d: string) => shortDate(d.slice(0, 10)) },
                        {
                          title: "Requests",
                          dataIndex: "requests_total",
                          align: "right",
                          render: (n: number) => n.toLocaleString(),
                        },
                        { title: "Bandwidth", dataIndex: "bytes_total", align: "right", render: humanBytes },
                      ]}
                    />
                  ),
                },
              ]}
            />
          </>
        )}
      </Card>

      <Card size="small" title="Visitors by country" loading={q.isLoading}>
        {countries.length === 0 ? (
          <Typography.Text type="secondary">
            No country data for this period. It is counted with each day&apos;s traffic on servers that have the
            GeoIP database CrowdSec downloads.
          </Typography.Text>
        ) : (
          <Space direction="vertical" size="small" style={{ width: "100%" }}>
            <Table<CountryRow>
              size="small"
              rowKey="country_code"
              pagination={countries.length > 10 ? { pageSize: 10, size: "small" } : false}
              scroll={{ x: "max-content" }}
              dataSource={countries}
              columns={[
                {
                  title: "Country",
                  dataIndex: "country_code",
                  render: (c: string) => countryName(c, i18n.language),
                },
                {
                  title: "Requests",
                  dataIndex: "requests_total",
                  align: "right",
                  render: (n: number) => n.toLocaleString(),
                },
                {
                  title: "Share",
                  key: "share",
                  width: 180,
                  render: (_: unknown, r: CountryRow) => {
                    const pct = countryRequests ? (r.requests_total / countryRequests) * 100 : 0;
                    return (
                      <Space size={8}>
                        <div
                          aria-hidden
                          style={{ width: 100, height: 8, borderRadius: 4, background: token.colorFillSecondary }}
                        >
                          <div
                            style={{
                              width: `${pct}%`,
                              height: 8,
                              borderRadius: 4,
                              background: token.colorPrimary,
                            }}
                          />
                        </div>
                        <span style={{ fontVariantNumeric: "tabular-nums" }}>
                          {pct < 1 && pct > 0 ? "<1" : Math.round(pct)}%
                        </span>
                      </Space>
                    );
                  },
                },
                { title: "Bandwidth", dataIndex: "bytes_total", align: "right", render: humanBytes },
              ]}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              Requests from visitors, known bots not counted. This product includes GeoLite2 data created by
              MaxMind, available from{" "}
              <Typography.Link href="https://www.maxmind.com" target="_blank" rel="noopener noreferrer">
                https://www.maxmind.com
              </Typography.Link>
              .
            </Typography.Text>
          </Space>
        )}
      </Card>
    </Space>
  );
}
