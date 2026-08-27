// MailStatsTab — GH #873: mail statistics with history and a storage
// drilldown (Global → User → Domain → Mailbox). Data comes from
// GET /admin/mail/stats: `points` are raw sampled values (gauges),
// `rates` are per-interval deltas (counters), both keyed by whatever
// metric names Stalwart's exporter emits (it registers metrics lazily,
// so charts appear as traffic flows).
//
// Metric names are VERIFIED against a live exporter under real traffic —
// received mail lands as message_ingest_ham / message_ingest_spam (there
// is no plain "message_ingest"), tenant submissions count via
// queue_authenticated_message_queued, and outcomes via delivery_completed /
// delivery_dsn_perm_fail. Do not add a chart for a name you have not seen
// in mail_stats_samples.
import { useMemo, useState } from "react";
import { Alert, Card, Col, Radio, Row, Space, Statistic, Table, Tag, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";

import { apiClient } from "../../../apiClient";
import { Sparkline, type SparklinePoint } from "../../../components/Sparkline";

type Point = { t: string; v: number };

type MailStats = {
  points: Record<string, Point[]>;
  rates: Record<string, Point[]>;
  current: Record<string, number>;
  storage: {
    username: string;
    domain_name: string;
    // GH #1234: the drilldown is domain-anchored, so a domain with no mailboxes
    // arrives as a single row with an empty email + 0 usage. mail_enabled tags
    // the mail-off ones.
    mail_enabled: boolean;
    email: string;
    usage_bytes: number;
    quota_bytes: number;
  }[];
  // GH #873 round 3: per-domain traffic totals over the selected range,
  // busiest first. Empty until the per-domain sampler has collected a window.
  traffic: DomainTraffic[];
  // GH #873 round 4: the same traffic rolled up per owning user, each with
  // their domains nested. Busiest user first.
  by_user: {
    username: string | null;
    user_id: string;
    sent: number;
    received: number;
    delivered: number;
    failed: number;
    domains: DomainTraffic[];
  }[];
};

type DomainTraffic = {
  domain: string;
  sent: number;
  received: number;
  delivered: number;
  failed: number;
};

// Storage drilldown nodes (User → Domain → Mailbox), grouped client-side.
type MailboxNode = MailStats["storage"][number];
type StorageDomainNode = {
  key: string;
  domain: string;
  usage: number;
  mailEnabled: boolean;
  boxes: MailboxNode[];
};
type StorageUserNode = {
  key: string;
  username: string;
  usage: number;
  domains: StorageDomainNode[];
};

const TRAFFIC_COLUMNS = [
  { title: "Sent", dataIndex: "sent", width: 90, align: "right" as const },
  { title: "Received", dataIndex: "received", width: 100, align: "right" as const },
  { title: "Delivered", dataIndex: "delivered", width: 100, align: "right" as const },
  { title: "Failed", dataIndex: "failed", width: 90, align: "right" as const },
];

const fmtBytes = (n: number): string => {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KiB`;
  return `${n} B`;
};

const toSpark = (pts: Point[] | undefined): SparklinePoint[] =>
  (pts ?? []).map((p) => ({ x: p.t, y: p.v }));

// sumSeries merges rate series by timestamp (samples share tick times, but a
// metric only starts existing once Stalwart first increments it — union, not
// zip). Used for "received" = ham + spam classifications (GH #873 round 2;
// metric names verified against a live exporter, message_ingest_* register
// lazily per outcome).
const sumSeries = (...series: (Point[] | undefined)[]): SparklinePoint[] => {
  const byT = new Map<string, number>();
  for (const pts of series) {
    for (const p of pts ?? []) byT.set(p.t, (byT.get(p.t) ?? 0) + p.v);
  }
  return [...byT.entries()]
    .sort(([a], [b]) => (a < b ? -1 : 1))
    .map(([x, y]) => ({ x, y }));
};

// Total over the charted window (rates are per-interval deltas).
const sumTotal = (pts: Point[] | undefined): number =>
  (pts ?? []).reduce((acc, p) => acc + p.v, 0);

const ChartCard = ({
  title,
  data,
  formatY,
}: {
  title: string;
  data: SparklinePoint[];
  formatY?: (n: number) => string;
}) => (
  <Card size="small" title={title}>
    {data.length > 1 ? (
      <Sparkline data={data} width={320} height={64} formatY={formatY} />
    ) : (
      <Typography.Text type="secondary">
        Collecting — appears after a few samples.
      </Typography.Text>
    )}
  </Card>
);

export const MailStatsTab = () => {
  const [hours, setHours] = useState(24);
  const stats = useQuery<MailStats>({
    queryKey: ["admin", "mail", "stats", hours],
    queryFn: async () =>
      (await apiClient.get<MailStats>(`/admin/mail/stats?hours=${hours}`)).data,
    refetchInterval: 60_000,
  });

  const s = stats.data;

  // Storage drilldown: rows grouped client-side User → Domain → Mailbox.
  // GH #1234: the payload is domain-anchored, so a domain with no mailboxes
  // arrives as one row with an empty email — keep the domain (tagged by
  // mailEnabled) but don't push that placeholder as a mailbox leaf.
  const storageTree = useMemo(() => {
    const users = new Map<
      string,
      {
        usage: number;
        domains: Map<string, { usage: number; mailEnabled: boolean; boxes: MailStats["storage"] }>;
      }
    >();
    for (const row of s?.storage ?? []) {
      const u = users.get(row.username) ?? { usage: 0, domains: new Map() };
      u.usage += row.usage_bytes;
      const d = u.domains.get(row.domain_name) ?? {
        usage: 0,
        mailEnabled: row.mail_enabled,
        boxes: [],
      };
      d.usage += row.usage_bytes;
      d.mailEnabled = row.mail_enabled;
      if (row.email) d.boxes.push(row); // skip the empty-email domain placeholder
      u.domains.set(row.domain_name, d);
      users.set(row.username, u);
    }
    return [...users.entries()].map(([username, u]) => ({
      key: username,
      username,
      usage: u.usage,
      domains: [...u.domains.entries()].map(([domain, d]) => ({
        key: `${username}/${domain}`,
        domain,
        usage: d.usage,
        mailEnabled: d.mailEnabled,
        boxes: d.boxes,
      })),
    }));
  }, [s?.storage]);

  const totalStorage = storageTree.reduce((acc, u) => acc + u.usage, 0);

  if (stats.isError) {
    return <Alert type="error" message="Failed to load mail statistics" />;
  }

  return (
    <>
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="Queue now" value={s?.current["queue_size"] ?? 0} />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title="SMTP connections"
              value={s?.current["smtp_active_connections"] ?? 0}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title="IMAP connections"
              value={s?.current["imap_active_connections"] ?? 0}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic title="Mailbox storage" value={fmtBytes(totalStorage)} />
          </Card>
        </Col>
      </Row>

      <Radio.Group
        value={hours}
        onChange={(e) => setHours(e.target.value)}
        style={{ marginBottom: 16 }}
        options={[
          { label: "24h", value: 24 },
          { label: "7d", value: 168 },
          { label: "30d", value: 720 },
        ]}
        optionType="button"
      />

      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title={`Received (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
              value={sumTotal(s?.rates["message_ingest_ham"]) + sumTotal(s?.rates["message_ingest_spam"])}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title={`Sent (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
              value={sumTotal(s?.rates["queue_authenticated_message_queued"])}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title={`Spam blocked (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
              value={sumTotal(s?.rates["message_ingest_spam"])}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card size="small">
            <Statistic
              title={`Delivery failures (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
              value={sumTotal(s?.rates["delivery_dsn_perm_fail"])}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} md={8}>
          <ChartCard
            title="Messages received"
            data={sumSeries(
              s?.rates["message_ingest_ham"],
              s?.rates["message_ingest_spam"],
            )}
          />
        </Col>
        <Col xs={24} md={8}>
          <ChartCard
            title="Messages sent"
            data={toSpark(s?.rates["queue_authenticated_message_queued"])}
          />
        </Col>
        <Col xs={24} md={8}>
          <ChartCard
            title="Spam blocked"
            data={toSpark(s?.rates["message_ingest_spam"])}
          />
        </Col>
      </Row>

      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} md={8}>
          <ChartCard
            title="Deliveries completed"
            data={toSpark(s?.rates["delivery_completed"])}
          />
        </Col>
        <Col xs={24} md={8}>
          <ChartCard title="Queue size" data={toSpark(s?.points["queue_size"])} />
        </Col>
        <Col xs={24} md={8}>
          <ChartCard
            title="Successful logins"
            data={toSpark(s?.rates["auth_success"])}
          />
        </Col>
      </Row>

      <Card
        size="small"
        title={`Traffic by domain (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
        style={{ marginBottom: 16 }}
        loading={stats.isLoading}
      >
        {(s?.traffic?.length ?? 0) === 0 ? (
          <Typography.Text type="secondary">
            Collecting — per-domain counts appear after the first sample once
            mail flows.
          </Typography.Text>
        ) : (
          <>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0, fontSize: 12 }}>
            Counted from the delivery log by envelope sender/recipient, so totals
            won't exactly match the server-wide tiles above.
          </Typography.Paragraph>
          <Table
            size="small"
            rowKey="domain"
            pagination={false}
            dataSource={s?.traffic ?? []}
            columns={[
              { title: "Domain", dataIndex: "domain" },
              { title: "Sent", dataIndex: "sent", width: 100, align: "right" },
              {
                title: "Received",
                dataIndex: "received",
                width: 110,
                align: "right",
              },
              {
                title: "Delivered",
                dataIndex: "delivered",
                width: 110,
                align: "right",
              },
              {
                title: "Failed",
                dataIndex: "failed",
                width: 100,
                align: "right",
              },
            ]}
          />
          </>
        )}
      </Card>

      <Card
        size="small"
        title={`Traffic by user (${hours >= 168 ? `${hours / 24}d` : `${hours}h`})`}
        style={{ marginBottom: 16 }}
        loading={stats.isLoading}
      >
        {(s?.by_user?.length ?? 0) === 0 ? (
          <Typography.Text type="secondary">
            Collecting — per-user totals appear once mail flows for a user's
            domains.
          </Typography.Text>
        ) : (
          <Table
            size="small"
            rowKey="user_id"
            pagination={false}
            dataSource={s?.by_user ?? []}
            columns={[
              {
                title: "User",
                dataIndex: "username",
                render: (u: string | null) => u ?? "(admin)",
              },
              ...TRAFFIC_COLUMNS,
            ]}
            expandable={{
              expandedRowRender: (u) => (
                <Table
                  size="small"
                  rowKey="domain"
                  pagination={false}
                  dataSource={u.domains}
                  columns={[
                    { title: "Domain", dataIndex: "domain" },
                    ...TRAFFIC_COLUMNS,
                  ]}
                />
              ),
            }}
          />
        )}
      </Card>

      <Card size="small" title="Storage drilldown" loading={stats.isLoading}>
        <Table<StorageUserNode>
          size="small"
          rowKey="key"
          pagination={false}
          dataSource={storageTree}
          columns={[
            { title: "User", dataIndex: "username" },
            {
              title: "Usage",
              dataIndex: "usage",
              render: (v: number) => fmtBytes(v),
              width: 140,
            },
          ]}
          expandable={{
            expandedRowRender: (user) => (
              <Table<StorageDomainNode>
                size="small"
                rowKey="key"
                pagination={false}
                dataSource={user.domains}
                columns={[
                  {
                    title: "Domain",
                    dataIndex: "domain",
                    // GH #1234: mail-disabled domains now appear (0 usage); tag
                    // them so they're not mistaken for empty mail consumers.
                    render: (v: string, row: StorageDomainNode) => (
                      <Space size={8}>
                        <span>{v}</span>
                        {!row.mailEnabled && <Tag>Mail off</Tag>}
                      </Space>
                    ),
                  },
                  {
                    title: "Usage",
                    dataIndex: "usage",
                    render: (v: number) => fmtBytes(v),
                    width: 140,
                  },
                ]}
                expandable={{
                  // No mailbox leaf to open for a mail-off / empty domain.
                  rowExpandable: (dom) => dom.boxes.length > 0,
                  expandedRowRender: (dom) => (
                    <Table<MailboxNode>
                      size="small"
                      rowKey="email"
                      pagination={false}
                      dataSource={dom.boxes}
                      columns={[
                        { title: "Mailbox", dataIndex: "email" },
                        {
                          title: "Usage",
                          dataIndex: "usage_bytes",
                          render: (v: number) => fmtBytes(v),
                          width: 140,
                        },
                        {
                          title: "Quota",
                          dataIndex: "quota_bytes",
                          render: (v: number) => fmtBytes(v),
                          width: 140,
                        },
                      ]}
                    />
                  ),
                }}
              />
            ),
          }}
        />
      </Card>
    </>
  );
};
