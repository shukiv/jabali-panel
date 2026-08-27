// CacheOverviewPage — GH #617 admin cache visibility. Ranks cache-enabled
// domains by nginx page-cache hit ratio so low-hit / high-bypass / noisy sites
// are visible at a glance. Reads GET /api/v1/admin/cache/overview.
import { useTranslation } from "react-i18next";
import { App, Card, Table, Tag, Typography } from "antd";
import { useEffect, useState } from "react";

import { apiClient } from "../../../apiClient";
import { FleetCacheDoctor } from "./FleetCacheDoctor";
import { extractApiError } from "../../../apiErrors";

type Row = {
  domain: string;
  hit_ratio: number;
  total: number;
  bypass: number;
  available: boolean;
};

export function CacheOverviewPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [rows, setRows] = useState<Row[]>([]);
  const [loading, setLoading] = useState(false);
  const [size, setSize] = useState<{ used_bytes?: number; max_bytes?: number } | null>(null);

  useEffect(() => {
    setLoading(true);
    apiClient
      .get<{ domains: Row[]; cache_size?: { used_bytes?: number; max_bytes?: number } }>("/admin/cache/overview")
      .then((res) => { setRows(res.data.domains ?? []); setSize((res.data as { cache_size?: { used_bytes?: number; max_bytes?: number } }).cache_size ?? null); })
      .catch((e) => message.error(extractApiError(e) ?? "Failed to load cache overview"))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <>
      <FleetCacheDoctor />
      <Card title={t("cacheoverviewpage.cache_overview_page_cache_hit_ratio_by_domai")}>
      <Typography.Paragraph type="secondary">
        Cache-enabled WordPress domains, ranked with the lowest hit ratio (most in
        need of attention) first. A low ratio with high traffic suggests a
        cookie/plugin forcing bypass, or a site that hasn&apos;t been warmed.
      </Typography.Paragraph>
      {size ? (
        <Typography.Paragraph>
          FastCGI cache zone: <b>{Math.round((size.used_bytes ?? 0) / (1024 * 1024))} MB</b> /{" "}
          {Math.round((size.max_bytes ?? 0) / (1024 * 1024 * 1024))} GB max (shared across all domains)
        </Typography.Paragraph>
      ) : null}
      <Table<Row>
        rowKey="domain"
        loading={loading}
        dataSource={rows}
        pagination={{ defaultPageSize: 20, hideOnSinglePage: true }}
        scroll={{ x: "max-content" }}
        columns={[
          { title: "Domain", dataIndex: "domain" },
          {
            title: "Hit ratio",
            dataIndex: "hit_ratio",
            render: (v: number, r) =>
              !r.available || r.total === 0 ? (
                <Tag>no data</Tag>
              ) : (
                <Tag color={v >= 70 ? "green" : v >= 30 ? "orange" : "red"}>
                  {v.toFixed(1)}%
                </Tag>
              ),
          },
          { title: "Requests", dataIndex: "total" },
          { title: "Bypass", dataIndex: "bypass" },
        ]}
      />
      </Card>
    </>
  );
}
