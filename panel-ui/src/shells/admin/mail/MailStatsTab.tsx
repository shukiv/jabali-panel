// MailStatsTab — GH #873: mail statistics with history and a storage
// drilldown (Global → User → Domain → Mailbox). Data comes from
// GET /admin/mail/stats: `points` are raw sampled values (gauges),
// `rates` are per-interval deltas (counters), both keyed by whatever
// metric names Stalwart's exporter emits (it registers metrics lazily,
// so charts appear as traffic flows).
import { useMemo, useState } from "react";
import { Alert, Card, Col, Radio, Row, Statistic, Table, Typography } from "antd";
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
    email: string;
    usage_bytes: number;
    quota_bytes: number;
  }[];
};

const fmtBytes = (n: number): string => {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KiB`;
  return `${n} B`;
};

const toSpark = (pts: Point[] | undefined): SparklinePoint[] =>
  (pts ?? []).map((p) => ({ x: p.t, y: p.v }));

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
  const storageTree = useMemo(() => {
    const users = new Map<
      string,
      { usage: number; domains: Map<string, { usage: number; boxes: MailStats["storage"] }> }
    >();
    for (const row of s?.storage ?? []) {
      const u = users.get(row.username) ?? { usage: 0, domains: new Map() };
      u.usage += row.usage_bytes;
      const d = u.domains.get(row.domain_name) ?? { usage: 0, boxes: [] };
      d.usage += row.usage_bytes;
      d.boxes.push(row);
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
        <Col xs={24} md={8}>
          <ChartCard
            title="Messages processed"
            data={toSpark(s?.rates["message_ingest"])}
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

      <Card size="small" title="Storage drilldown" loading={stats.isLoading}>
        <Table
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
              <Table
                size="small"
                rowKey="key"
                pagination={false}
                dataSource={user.domains}
                columns={[
                  { title: "Domain", dataIndex: "domain" },
                  {
                    title: "Usage",
                    dataIndex: "usage",
                    render: (v: number) => fmtBytes(v),
                    width: 140,
                  },
                ]}
                expandable={{
                  expandedRowRender: (dom) => (
                    <Table
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
